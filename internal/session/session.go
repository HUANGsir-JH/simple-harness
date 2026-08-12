package session

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/events"
	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
)

// FileAgentState 是会话目录下的 state 文件名。
const FileAgentState = "agentstate.json"

// Session 是一个会话：conversation + AgentState + 异步 transcript writer。
// 目录布局见 Store 注释（session 目录下 historys/ + agentstate.json + plans/）。
type Session struct {
	ID           string
	dir          string
	historyDir   string
	statePath    string
	conversation *messages.Conversation
	state        *agentstate.AgentState
	writer       *TranscriptWriter
}

// Create 新建一个会话：建目录、写 meta 首行、初始化 agentstate.json。
// cwd 是会话启动的进程工作目录（state.CWD 存它，resume 后模型可知会话
// 从哪启动，ADR-028）；bucket 归属 = FindProject 对启动目录的精确匹配
// （2026-08-09 起不做向上归并），故 state.CWD 与 bucket 都指向启动目录。
// mode 是默认审批模式（config approval.mode 播种值；空 = 不固化，审批回退
// 默认）。创建时固化进 AgentState.Permission.Mode，之后 /permission 切换改
// 会话 state（resume 恢复）——审批模式完全由会话决定（ADR-029）。
func (p *Project) Create(model, cwd, mode string) (*Session, error) {
	sid := newSessionID()
	dir := filepath.Join(p.Dir, sid)
	historyDir := filepath.Join(dir, DirHistorys)
	if err := os.MkdirAll(historyDir, 0o755); err != nil {
		return nil, fmt.Errorf("session: mkdir %s: %w", historyDir, err)
	}
	if err := os.MkdirAll(filepath.Join(dir, DirPlans), 0o755); err != nil {
		return nil, fmt.Errorf("session: mkdir plans: %w", err)
	}
	statePath := filepath.Join(dir, FileAgentState)
	st := agentstate.New(sid, model, cwd)
	if mode != "" {
		st.Permission = &agentstate.PermissionState{Mode: mode}
	}
	if err := agentstate.SaveFile(statePath, st); err != nil {
		return nil, err
	}
	w, err := NewTranscriptWriter(historyDir)
	if err != nil {
		return nil, err
	}
	s := &Session{
		ID:           sid,
		dir:          dir,
		historyDir:   historyDir,
		statePath:    statePath,
		conversation: messages.NewConversation(),
		state:        st,
		writer:       w,
	}
	// meta 首行（会话元数据，resume 可读）。
	w.Write(Line{Type: "meta", SessionID: sid, CWD: cwd, Model: model, CreatedAt: st.CreatedAt})
	return s, nil
}

// Resume 加载已有会话：重建 conversation + 恢复 state + 打开最新 transcript 继续追加。
func (p *Project) Resume(info SessionInfo) (*Session, error) {
	historyDir := filepath.Join(info.Path, DirHistorys)
	statePath := filepath.Join(info.Path, FileAgentState)
	conv, err := LoadConversation(historyDir)
	if err != nil {
		return nil, err
	}
	st, err := agentstate.LoadFile(statePath)
	if err != nil {
		return nil, err
	}
	w, err := NewTranscriptWriter(historyDir)
	if err != nil {
		return nil, err
	}
	return &Session{
		ID:           info.ID,
		dir:          info.Path,
		historyDir:   historyDir,
		statePath:    statePath,
		conversation: conv,
		state:        st,
		writer:       w,
	}, nil
}

// Dir 返回会话目录。
func (s *Session) Dir() string { return s.dir }

// Conversation 返回会话的消息序列（resume 后含历史）。
func (s *Session) Conversation() *messages.Conversation { return s.conversation }

// State 返回 AgentState（经 rc.State 注入 agent；SessionMiddleware after 落盘）。
func (s *Session) State() *agentstate.AgentState { return s.state }

// StatePath 返回 agentstate.json 路径（SessionMiddleware 用）。
func (s *Session) StatePath() string { return s.statePath }

// Model 返回会话使用的模型（来自 AgentState；空 = 未设置）。
func (s *Session) Model() string { return s.state.Model }

// Name 返回会话名（首消息自动命名或 /rename；空 = 未命名，展示时短 ID 兜底）。
func (s *Session) Name() string { return s.state.Name }

// SetName 更新会话名并立即落盘（/rename；首消息自动命名也走这里）。
// 写字段经 AgentState 带锁方法，落盘经 SaveFile（内部 Marshal 加锁）——
// 顺序加锁不嵌套（锁下沉，ADR-036 修订）。
func (s *Session) SetName(name string) error {
	s.state.SetName(name)
	return agentstate.SaveFile(s.statePath, s.state)
}

// RuntimeContext 从会话构建 per-call 上下文（无状态 agent 对位 ADR-026：
// agent 不持有会话状态，一切经 rc 传入）。每次 agent.Run 调用新建；
// 切换会话 = 换 Session 再取 rc，并行 = 每 goroutine 一个 rc。
func (s *Session) RuntimeContext() *middleware.RuntimeContext {
	rc := middleware.NewRuntimeContext()
	rc.SessionID = s.ID
	rc.Messages = s.conversation
	rc.State = s.state
	rc.StatePath = s.statePath
	rc.Model = s.state.Model
	rc.ThinkingEffort = s.state.ThinkingEffort
	if s.state.ThinkingEnabled != nil {
		v := *s.state.ThinkingEnabled
		rc.ThinkingEnabled = &v
	}
	return rc
}

// SetModel 更新会话模型并立即落盘（/model 运行时切换）。
func (s *Session) SetModel(model string) error {
	s.state.SetModel(model)
	return agentstate.SaveFile(s.statePath, s.state)
}

// SetThinkingEnabled 更新 thinking 开关并立即落盘（--thinking/--no-thinking、
// 运行时切换）。nil 表示恢复继承 client 默认。
func (s *Session) SetThinkingEnabled(enabled *bool) error {
	s.state.SetThinkingEnabled(enabled)
	return agentstate.SaveFile(s.statePath, s.state)
}

// SetThinkingEffort 更新推理档位并立即落盘（/effort 运行时切换）。
func (s *Session) SetThinkingEffort(effort string) error {
	s.state.SetThinkingEffort(effort)
	return agentstate.SaveFile(s.statePath, s.state)
}

// SetPermissionMode 更新会话审批模式并立即落盘（/permission 运行时切换，
// ADR-029）。切换后下一轮 Run 生效（ApprovalMiddleware 从 rc.State 读模式）。
func (s *Session) SetPermissionMode(mode string) error {
	s.state.SetPermissionMode(mode)
	return agentstate.SaveFile(s.statePath, s.state)
}

// SetPlanMode 更新会话 plan 模式开关并立即落盘（/plan 运行时切换、plan_enter/
// plan_done 工具也直接改 rc.State.PlanMode，ADR-036）。切换后下一轮采样生效
// （ApprovalMiddleware 从 rc.State 读 plan 分支；plan 指令注入在进入点单独处理）。
func (s *Session) SetPlanMode(on bool) error {
	s.state.SetPlanMode(on)
	return agentstate.SaveFile(s.statePath, s.state)
}

// PlanFile 返回计划文件路径（state.Plan.Path 优先，否则 <会话>/plans/plan.md，
// ADR-036）。/plan view 与 write_plan 同源。
func (s *Session) PlanFile() string {
	if p := s.state.PlanPath(); p != "" {
		return p
	}
	return filepath.Join(s.dir, DirPlans, "plan.md")
}

// AddUser 添加一条用户消息：写入 conversation（模型可见）并记录 transcript（user 行）。
func (s *Session) AddUser(content string) {
	msg := messages.NewUserMessage(content)
	s.conversation.Add(msg)
	s.WriteUser(msg)
}

// AddCommand 记录一条斜杠命令（transcript command 行；不进 conversation，
// 模型不可见——对齐 thinking 存但不重放，ADR-030）。TUI resume 渲染系统行。
// 命令低频，落盘同步（Flush）保证 resume/切换立即可见。
func (s *Session) AddCommand(content string) {
	s.writer.Write(Line{Type: "command", Content: content})
	s.writer.Flush()
}

// Commands 返回本会话历史中的斜杠命令（resume 时 TUI 渲染系统行）。
func (s *Session) Commands() ([]string, error) {
	return loadCommands(s.historyDir)
}

// TranscriptLines returns the latest transcript segment for timeline UIs.
// 第二返回值 = 跳过的坏行数（读侧容错，Bug08），调用方可提示用户。
func (s *Session) TranscriptLines() ([]Line, int, error) {
	return LoadLines(s.historyDir)
}

// Writer 返回 transcript writer（CLI 转发 agent 事件用）。
func (s *Session) Writer() *TranscriptWriter { return s.writer }

// WriteUser 记录一条用户消息（写 user 行）。msg 须先 Add 进 conversation。
func (s *Session) WriteUser(msg *messages.Message) {
	s.writer.Write(Line{Type: "user", MsgID: msg.ID, Content: msg.Content})
}

// OnAgentEvent 把 agent 回合级事件转发给 transcript writer（块级实时落盘）。
func (s *Session) OnAgentEvent(ev events.Event) {
	s.writer.OnAgentEvent(ev)
}

// Close flush transcript writer。
func (s *Session) Close() error { return s.writer.Close() }

// newSessionID 生成目录名即会话 id：<时间戳>-<8 位随机 hex>（可读可排序）。
func newSessionID() string {
	return time.Now().Format("20060102T150405") + "-" + randHex(8)
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err == nil {
		return hex.EncodeToString(b)[:n]
	}
	// 兜底（crypto/rand 极不可能失败）。
	return fmt.Sprintf("%08x", time.Now().UnixNano())[:n]
}
