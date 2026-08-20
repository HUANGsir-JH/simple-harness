// Package subagent 实现子 agent（阶段 5，ADR-045）：进程内 Manager + 控制工具
// + 按类型装配。核心机制复用 completion 队列——子完成事件 Append 进父会话
// Queue，父 BackgroundCompletionMiddleware 路径 A 注入（在途采样前）/ TUI 路径 B
// 唤醒（空闲），主 agent 无需 wait_agent 工具。
//
// 依赖方向（无环）：subagent → agent/tools/session（装配与工具实现都在本包；
// 子装配 buildSubagent 独立于 agent.Build，见 build.go）；
// agent 保持通用（仅主装配经 BuildOptions.Tools），不感知 subagent；
// app/tui → subagent。
package subagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/agent-project/harness/internal/agent"
	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/completion"
	"github.com/agent-project/harness/internal/config"
	"github.com/agent-project/harness/internal/events"
	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/provider"
	"github.com/agent-project/harness/internal/session"
	"github.com/agent-project/harness/internal/tools"
)

// 子 agent 类型（按类型配置工具集，定案 subagent.md 第 8 条）。
const (
	KindGeneralPurpose = "general-purpose" // 内置 − ask_user + 控制工具 + wait_task
	KindExplore        = "explore"         // 只读：read_file/list_dir/glob/skill
)

// MaxDepth 是嵌套深度上限（用户拍板：硬编码 2，不做 config）。
const MaxDepth = 2

// 子 agent 生命周期状态（运行态挂 Manager 注册表；持久态在子会话
// agentstate.json 的 Status 字段）。
const (
	StatusPending     = "pending"
	StatusRunning     = "running"
	StatusCompleted   = "completed"
	StatusFailed      = "failed"
	StatusInterrupted = "interrupted"
)

// Options 是 Manager 的装配参数（app 层传入，对齐项目 Options 惯例）。
// Provider 必填；路径空 = 对应能力不装配。
type Options struct {
	Provider        *config.ProviderConfig
	DefaultMode     string
	GlobalAgentsMD  string
	GlobalSkillsDir string
	// Client 覆盖 provider client（测试注入 FakeClient 用；空 = agent.Build
	// 内部 NewClient）。
	Client provider.Client
	// BaseInstructions 覆盖 general-purpose 子 agent 链首 persona（空 =
	// 默认 DefaultBaseInstructions；评测对齐官方 minimal 用 config 覆盖，
	// 2026-08-20）。explore 子 agent 固定用专属只读提示词，不受影响。
	BaseInstructions string
}

// Entry 是一个子 agent 的运行态注册条目（进程内；持久态在子会话目录）。
type Entry struct {
	ID       string // = 子会话 id（session.NewID("sub")，目录名即 id）
	Name     string
	Type     string
	Status   string
	ParentID string // 父会话 id
	Depth    int
	Dir      string   // 子会话目录
	session  *session.Session
	queue    *completion.Queue // 父会话 Queue（完成注入目标）
	cancel   context.CancelFunc
	done     chan struct{} // close = 子 goroutine 收尾完成
}

// EntryView 是 list_agents / /subagents 展示用的快照。
type EntryView struct {
	ID       string
	Name     string
	Type     string
	Status   string
	Depth    int
	ParentID string
	Running  bool // 进程内运行中（磁盘历史 false）
}

// Manager 是进程内子 agent 注册表 + 生命周期管理。
// 只存运行态（goroutine/cancel/状态迁移），进程退出即清；持久态全部在
// 子会话目录（agentstate.json 血缘/状态 + 父 completions.json 通知）。
type Manager struct {
	mu      sync.Mutex
	entries map[string]*Entry // 按子 agent id（= 子会话 id）
	agents  map[string]*agent.Agent // 按 kind 缓存共享的装配实例（无状态 ADR-026）
	opts    Options
	subs    map[string][]func(events.Event) // 子事件订阅（TUI 查看模式）
	closed  bool // Shutdown 后拒绝新 spawn/resume
}

// NewManager 创建子 agent 管理器（app 层传装配参数，不传函数）。
func NewManager(o Options) *Manager {
	return &Manager{
		entries: make(map[string]*Entry),
		agents:  make(map[string]*agent.Agent),
		opts:    o,
		subs:    make(map[string][]func(events.Event)),
	}
}

// SpawnRequest 是 spawn_agent 工具的请求。
type SpawnRequest struct {
	Name           string
	Message        string
	Type           string
	Model          string
	ThinkingEffort string
}

// Spawn 创建并异步启动一个子 agent：建子会话（<父会话目录>/subagents/<子id>/，
// 血缘 + 权限快照播种）→ Entry 注册 → goroutine 跑子 Run → 立即返回子 id。
// 子 ctx 独立（WithCancel(Background)）：父 Esc 不级联；退出清理 = Shutdown。
func (m *Manager) Spawn(rc *middleware.RuntimeContext, req SpawnRequest) (string, error) {
	req.Type = normalizeKind(req.Type)
	if req.Message == "" {
		return "", &tools.ToolError{RespondToModel: true, Message: "spawn_agent: message 必填（子 agent 的任务描述）"}
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return "", &tools.ToolError{RespondToModel: true, Message: "spawn_agent: harness 正在退出，无法创建子 agent"}
	}
	// 深度检查：当前 agent 深度（主会话不在注册表 = 0）→ 子 = 父 + 1。
	depth := 0
	if rc != nil && rc.SessionID != "" {
		if e, ok := m.entries[rc.SessionID]; ok {
			depth = e.Depth
		}
	}
	m.mu.Unlock()
	if depth >= MaxDepth {
		return "", &tools.ToolError{RespondToModel: true, Message: fmt.Sprintf(
			"spawn_agent: 嵌套深度已达上限（%d 层），不能再创建子 agent", MaxDepth)}
	}

	// 父会话目录 = rc.StatePath 所在目录（StatePath 空 = 非会话，子 agent 不可用）。
	if rc == nil || rc.StatePath == "" {
		return "", &tools.ToolError{RespondToModel: true, Message: "spawn_agent: 当前不在会话中，无法创建子 agent"}
	}
	parentDir := filepath.Dir(rc.StatePath)

	// 子会话：id/目录 + 血缘/name/权限快照播种。
	sid := session.NewID("sub")
	st := agentstate.New(sid, m.resolveModel(rc, req.Model), m.resolveCWD(rc))
	st.SetSubagent(rc.SessionID, req.Type, depth+1)
	st.SetStatus(StatusPending)
	if req.Name != "" {
		st.SetName(req.Name)
	} else {
		st.SetName(defaultName(req.Type, sid))
	}
	if req.ThinkingEffort != "" {
		st.SetThinkingEffort(req.ThinkingEffort)
	}
	if rc.State != nil && rc.State.PermissionMode() != "" {
		// 权限继承（定案第 9 条）：Mode + Approved 快照播种进子 AgentState。
		st.Permission = &agentstate.PermissionState{Mode: rc.State.PermissionMode(), Approved: rc.State.Approved()}
	}
	sub, err := session.CreateIn(filepath.Join(parentDir, session.DirSubagents, sid), st)
	if err != nil {
		return "", &tools.ToolError{RespondToModel: true, Message: "spawn_agent: " + err.Error()}
	}
	// fork 过滤（定案第 3 条）：子会话起点 = 仅 spawn 的 message。
	sub.AddUser(req.Message)

	entry := &Entry{
		ID:       sid,
		Name:     st.Name,
		Type:     req.Type,
		Status:   StatusPending,
		ParentID: rc.SessionID,
		Depth:    depth + 1,
		Dir:      sub.Dir(),
		session:  sub,
		queue:    rc.Completions,
		done:     make(chan struct{}),
	}
	m.mu.Lock()
	m.entries[sid] = entry
	m.mu.Unlock()

	go m.runChild(entry, rc)
	return sid, nil
}

// runChild 是子 agent 的 goroutine：子装配 → 子 rc → agent.Run → 收尾通知。
func (m *Manager) runChild(entry *Entry, parentRC *middleware.RuntimeContext) {
	// cancel 先于 running 状态赋值：Interrupt 看到 running 时 cancel 必可用
	// （否则"等 running 后中断"会落在 cancel nil 窗口静默失败）。读写经
	// Manager 锁（与 Interrupt/Shutdown 并发读无 data race）。
	childCtx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	entry.cancel = cancel
	sess := entry.session // 启动时捕获（运行中 Resume 被状态检查拒绝，不会换）
	m.mu.Unlock()
	m.setStatus(entry, StatusRunning)
	defer close(entry.done)

	a, err := m.buildSubagent(entry.Type)
	if err != nil {
		m.finish(entry, StatusFailed, "子 agent 装配失败："+err.Error(), "")
		return
	}

	// 子 rc 从【子会话自己】RuntimeContext() 构建：Segment/Completions/
	// AppendUser/State/Model 全部绑定子会话（压缩切子的段、send_message 注入
	// 子的队列），不传递父 rc 的任何字段。唯一例外：Approver 包装父的
	//（审批转发用户 + AgentID 归属标识，定案第 9 条）。
	rc := sess.RuntimeContext()
	if parentRC != nil && parentRC.Approver != nil {
		rc.Approver = &subagentApprover{inner: parentRC.Approver, agentID: entry.ID}
	}
	// 子 onEvent：落盘（子会话 transcript）固定 + Manager 分发（TUI 查看订阅）。
	onEvent := func(ev events.Event) {
		sess.OnAgentEvent(ev)
		m.dispatch(entry.ID, ev)
	}

	err = a.Run(childCtx, rc, onEvent)
	// Run 结束，中断目标消失（锁内清，防 Interrupt 并发读）。
	m.mu.Lock()
	entry.cancel = nil
	m.mu.Unlock()

	switch {
	case err == nil:
		m.finish(entry, StatusCompleted, extractAnswer(rc.Messages), "")
	case errors.Is(err, context.Canceled):
		m.finish(entry, StatusInterrupted, extractAnswer(rc.Messages), "")
	default:
		m.finish(entry, StatusFailed, err.Error(), extractAnswer(rc.Messages))
	}
}

// finish 收尾三分支（完成/失败/中断）：Status 落盘 + 会话 Close + 通知 Append
// 进父 Queue（父下轮采样前注入 / TUI 唤醒，复用 ADR-040 通道）。
// detail = 最终答案 / 错误消息；partial = 已产出文本（失败/中断通知带）。
func (m *Manager) finish(entry *Entry, status, detail, partial string) {
	m.mu.Lock()
	entry.Status = status
	sess := entry.session // Resume 可能并发换会话（锁内读）
	m.mu.Unlock()
	sess.State().SetStatus(status)
	if err := agentstate.SaveFile(sess.StatePath(), sess.State()); err != nil {
		fmt.Fprintf(os.Stderr, "subagent: save state: %v\n", err)
	}
	if err := sess.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "subagent: close session: %v\n", err)
	}
	if entry.queue != nil {
		entry.queue.Append(completion.Event{
			ToolName:  "spawn_agent",
			Result:    formatNotice(entry, status, detail, partial),
			DoneAt:    time.Now().UTC().Format(time.RFC3339),
			SessionID: entry.ID,
		})
	}
}

// formatNotice 构造完成/失败/中断通知全文（对齐 ADR-040 shell 通知风格）。
func formatNotice(e *Entry, status, detail, partial string) string {
	switch status {
	case StatusCompleted:
		return fmt.Sprintf("（系统通知：子 agent %s（%s，%s）已完成。结果：\n%s）",
			e.Name, e.ID, e.Type, detail)
	case StatusInterrupted:
		p := partial
		if strings.TrimSpace(p) == "" {
			p = "无"
		}
		return fmt.Sprintf("（系统通知：子 agent %s（%s，%s）已中断。中断前结果：\n%s）",
			e.Name, e.ID, e.Type, p)
	default: // failed
		p := partial
		if strings.TrimSpace(p) == "" {
			p = "无"
		}
		return fmt.Sprintf("（系统通知：子 agent %s（%s，%s）执行失败：%s。失败前结果：\n%s）",
			e.Name, e.ID, e.Type, detail, p)
	}
}

// extractAnswer 提取子 agent 的最终答案：conversation 中最后一条无 tool_calls
// 的 assistant 文本（正常完成必有；失败/中断时可能是"已产出"的部分内容）。
func extractAnswer(conv *messages.Conversation) string {
	if conv == nil {
		return ""
	}
	for i := len(conv.Messages) - 1; i >= 0; i-- {
		m := conv.Messages[i]
		if m.Role == messages.RoleAssistant && len(m.ToolCalls) == 0 {
			t := strings.TrimSpace(m.Content)
			if t != "" {
				return t
			}
		}
	}
	return ""
}

// setStatus 更新运行态（锁内写字段）+ 落盘子会话 agentstate（跨进程可恢复）。
func (m *Manager) setStatus(e *Entry, s string) {
	m.mu.Lock()
	e.Status = s
	sess := e.session // Resume 可能并发换会话（锁内读）
	m.mu.Unlock()
	sess.State().SetStatus(s)
	if err := agentstate.SaveFile(sess.StatePath(), sess.State()); err != nil {
		fmt.Fprintf(os.Stderr, "subagent: save state: %v\n", err)
	}
}

// Send 主→子单向消息：仅**直属**运行中的子（消息进子会话 Queue，子下轮采样
// 前注入；子已结束 → 调用方（工具）回填错误引导 resume_agent）。目标必须是
// 当前会话直接 spawn 的子——兄弟/无关 agent 拒绝（2026-08-16 修复，与
// resume_agent 的"仅直属子"对称）。
func (m *Manager) Send(rc *middleware.RuntimeContext, id, message string) error {
	e, ok := m.get(id)
	if !ok {
		return &tools.ToolError{RespondToModel: true, Message: fmt.Sprintf(
			"send_message: 未找到子 agent %s", id)}
	}
	if e.ParentID != rc.SessionID {
		return &tools.ToolError{RespondToModel: true, Message: fmt.Sprintf(
			"send_message: 只能向直属子 agent 发送消息（%s 不是当前会话的子）", id)}
	}
	if m.statusOf(id) != StatusRunning {
		return &tools.ToolError{RespondToModel: true, Message: fmt.Sprintf(
			"send_message: 子 agent %s 未在运行（状态 %s）。如需继续任务请用 resume_agent", id, m.statusOf(id))}
	}
	e.session.Completions().Append(completion.Event{
		ToolName:  "send_message",
		Result:    "（系统通知：主 agent 发来消息：" + message + "）",
		DoneAt:    time.Now().UTC().Format(time.RFC3339),
		SessionID: id,
	})
	return nil
}

// Interrupt 中断运行中的后代（不能中断自己/父/兄弟/无关 agent；对齐 codex
// interrupt_agent + 2026-08-16 归属校验修复）。中断 = cancel 子 turn ctx（Esc
// 同款语义），子收尾通知父（带中断前结果）。
func (m *Manager) Interrupt(rc *middleware.RuntimeContext, id string) error {
	e, ok := m.get(id)
	if !ok {
		return &tools.ToolError{RespondToModel: true, Message: fmt.Sprintf(
			"interrupt_agent: 未找到子 agent %s", id)}
	}
	// 目标必须是当前会话的后代（沿 ParentID 链；自己/祖先/兄弟/无关 agent 均
	// 非后代 → 拒绝；主会话 = 全部子 agent 的祖先）。
	if !m.isDescendant(id, rc.SessionID) {
		return &tools.ToolError{RespondToModel: true, Message: fmt.Sprintf(
			"interrupt_agent: 只能中断当前会话的子 agent（%s 不属于当前会话）", id)}
	}
	st := m.statusOf(id)
	if st != StatusRunning && st != StatusPending {
		return &tools.ToolError{RespondToModel: true, Message: fmt.Sprintf(
			"interrupt_agent: 子 agent %s 已结束（状态 %s）", id, st)}
	}
	m.mu.Lock()
	c := e.cancel
	m.mu.Unlock()
	if c == nil {
		// runChild 尚未赋值 cancel（pending 窗口）：显式报错而非静默跳过。
		return &tools.ToolError{RespondToModel: true, Message: fmt.Sprintf(
			"interrupt_agent: 子 agent %s 正在启动中，请稍后再试", id)}
	}
	c()
	return nil
}

// Resume 磁盘加载已落盘的子会话继续新任务（仅直属子；定案第 6 条）。
// 走 session.ResumeAt（writer 续接原 transcript 段，不新开文件）；message 可选
// （追加为新 user 消息）。完成后结果再次注入父（多轮委托）。
// 进程重启后：内存未命中时从磁盘恢复（List 可见的历史子 agent 必须可 resume，
// 2026-08-16 修复 P1b——读 agentstate.json 验证血缘后 ResumeAt）。
func (m *Manager) Resume(rc *middleware.RuntimeContext, id, message string) error {
	if rc == nil || rc.StatePath == "" {
		return &tools.ToolError{RespondToModel: true, Message: "resume_agent: 当前不在会话中"}
	}
	parentDir := filepath.Dir(rc.StatePath)
	dir := filepath.Join(parentDir, session.DirSubagents, id)

	e, ok := m.get(id)
	if !ok {
		// 磁盘恢复路径（新进程 Manager 无 Entry）。
		st, err := agentstate.LoadFile(filepath.Join(dir, session.FileAgentState))
		if err != nil {
			return &tools.ToolError{RespondToModel: true, Message: fmt.Sprintf(
				"resume_agent: 未找到子 agent %s（可用 list_agents 查看）", id)}
		}
		if st.ParentID != rc.SessionID {
			return &tools.ToolError{RespondToModel: true, Message: fmt.Sprintf(
				"resume_agent: 只能 resume 直属子 agent（%s 不是当前会话的子）", id)}
		}
		sub, err := session.ResumeAt(dir)
		if err != nil {
			return &tools.ToolError{RespondToModel: true, Message: "resume_agent: " + err.Error()}
		}
		e = &Entry{
			ID:       id,
			Name:     st.Name,
			Type:     st.AgentType,
			Status:   StatusPending,
			ParentID: st.ParentID,
			Depth:    st.Depth,
			Dir:      sub.Dir(),
			queue:    rc.Completions,
		}
		return m.resumeEntry(e, rc, message, sub)
	}

	if e.ParentID != rc.SessionID {
		return &tools.ToolError{RespondToModel: true, Message: fmt.Sprintf(
			"resume_agent: 只能 resume 直属子 agent（%s 不是当前会话的子）", id)}
	}
	st := m.statusOf(id)
	if st == StatusRunning || st == StatusPending {
		return &tools.ToolError{RespondToModel: true, Message: fmt.Sprintf(
			"resume_agent: 子 agent %s 正在运行（状态 %s）", id, st)}
	}
	sub, err := session.ResumeAt(e.Dir)
	if err != nil {
		return &tools.ToolError{RespondToModel: true, Message: "resume_agent: " + err.Error()}
	}
	return m.resumeEntry(e, rc, message, sub)
}

// resumeEntry 启动已加载的子会话续跑（内存续跑与磁盘恢复共用）：
// message 追加 → entry 绑定新会话 → pending → goroutine runChild。
func (m *Manager) resumeEntry(e *Entry, rc *middleware.RuntimeContext, message string, sub *session.Session) error {
	if message != "" {
		sub.AddUser(message)
	}
	m.mu.Lock()
	e.session = sub // runChild/finish/Session() 并发读，锁内写
	e.done = make(chan struct{}) // 上一轮 runChild 已 close，重开（defer close 防双 close panic）
	e.cancel = nil
	m.mu.Unlock()
	m.setStatus(e, StatusPending)
	m.mu.Lock()
	m.entries[e.ID] = e
	m.mu.Unlock()
	go m.runChild(e, rc)
	return nil
}

// List 列出当前会话（rc.SessionID）的直属子：运行态（注册表）+ 磁盘历史
// （扫描 <父会话目录>/subagents/ 读 agentstate.json，进程重启后可恢复列表）。
func (m *Manager) List(rc *middleware.RuntimeContext) []EntryView {
	m.mu.Lock()
	var out []EntryView
	for _, e := range m.entries {
		if e.ParentID == rc.SessionID {
			out = append(out, EntryView{ID: e.ID, Name: e.Name, Type: e.Type, Status: e.Status, Depth: e.Depth, ParentID: e.ParentID, Running: true})
		}
	}
	m.mu.Unlock()
	if rc.StatePath != "" {
		out = append(out, m.listFromDisk(filepath.Dir(rc.StatePath), rc.SessionID)...)
	}
	// 确定性排序：id 升序。
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].ID < out[j-1].ID; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// listFromDisk 扫描子会话目录读历史态（去重运行态已覆盖的 id）。
func (m *Manager) listFromDisk(parentDir, parentID string) []EntryView {
	subDir := filepath.Join(parentDir, session.DirSubagents)
	dirs, err := os.ReadDir(subDir)
	if err != nil {
		return nil
	}
	var out []EntryView
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		id := d.Name()
		if _, ok := m.get(id); ok {
			continue // 运行态已覆盖
		}
		st, err := agentstate.LoadFile(filepath.Join(subDir, id, session.FileAgentState))
		if err != nil || st.ParentID != parentID {
			continue
		}
		out = append(out, EntryView{ID: id, Name: st.Name, Type: st.AgentType, Status: st.Status, Depth: st.Depth, ParentID: st.ParentID})
	}
	return out
}

// Subscribe 订阅子 agent 事件流（TUI 查看模式实时滚动；落盘由子 onEvent 固定
// 做，订阅者只负责渲染）。
func (m *Manager) Subscribe(sid string, fn func(events.Event)) {
	m.mu.Lock()
	m.subs[sid] = append(m.subs[sid], fn)
	m.mu.Unlock()
}

// Unsubscribe 退订（fn 用比较删除，同函数指针）。
func (m *Manager) Unsubscribe(sid string, fn func(events.Event)) {
	m.mu.Lock()
	list := m.subs[sid]
	out := list[:0]
	for _, f := range list {
		// reflect.ValueOf(f).Pointer() 比较；普通函数/闭包均可退订。
		if sameFunc(f, fn) {
			continue
		}
		out = append(out, f)
	}
	m.subs[sid] = out
	m.mu.Unlock()
}

// dispatch 把子事件分发给订阅者（锁外调用回调，防重入——对齐 Queue.OnAppend）。
func (m *Manager) dispatch(sid string, ev events.Event) {
	m.mu.Lock()
	list := make([]func(events.Event), len(m.subs[sid]))
	copy(list, m.subs[sid])
	m.mu.Unlock()
	for _, fn := range list {
		fn(ev)
	}
}

// Shutdown 进程退出清理：cancel 全部子 + 等收尾（finish 内 Close 子会话 +
// 通知 Append 父 Queue——父 resume 后补注入）。10s 上限防卡死。
func (m *Manager) Shutdown() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	entries := make([]*Entry, 0, len(m.entries))
	for _, e := range m.entries {
		entries = append(entries, e)
	}
	m.mu.Unlock()
	for _, e := range entries {
		m.mu.Lock()
		c := e.cancel
		m.mu.Unlock()
		if c != nil {
			c()
		}
	}
	for _, e := range entries {
		select {
		case <-e.done:
		case <-time.After(10 * time.Second):
		}
	}
}

// RunningCount 返回仍在运行（done 未关闭）的子 agent 数。
// run 模式回合末等子（2026-08-19，A 方案）：父回合结束后据此决定是否等待。
func (m *Manager) RunningCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, e := range m.entries {
		select {
		case <-e.done:
		default:
			n++
		}
	}
	return n
}

// WaitAll 等待全部运行中的子 agent 完成（有界超时；ctx 取消/超时提前返回）。
// 返回超时/取消时仍处于运行中的子数（0 = 全部完成）。
// 只用于 run 模式回合末等子：TUI 的子跨回合存活由完成注入 + 唤醒承担，
// 不调用本函数（2026-08-19 用户拍板：仅影响 runOnce）。
func (m *Manager) WaitAll(ctx context.Context, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	for {
		if err := ctx.Err(); err != nil {
			return m.RunningCount()
		}
		m.mu.Lock()
		var first <-chan struct{}
		for _, e := range m.entries {
			select {
			case <-e.done:
			default:
				if first == nil {
					first = e.done
				}
			}
		}
		m.mu.Unlock()
		if first == nil {
			return 0
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return m.RunningCount()
		}
		timer := time.NewTimer(remaining)
		select {
		case <-first:
			timer.Stop() // 有子完成 → 重查（其余可能仍运行）
		case <-timer.C:
			return m.RunningCount()
		case <-ctx.Done():
			timer.Stop()
			return m.RunningCount()
		}
	}
}

// CancelRunning 取消全部运行中的子 agent（区别于 Shutdown：不置 closed，
// Manager 仍可 spawn/resume——run 模式回合末等子超时后取消剩余、再跑一轮
// 收尾时模型仍可能创建新子）。
func (m *Manager) CancelRunning() {
	m.mu.Lock()
	entries := make([]*Entry, 0, len(m.entries))
	for _, e := range m.entries {
		entries = append(entries, e)
	}
	m.mu.Unlock()
	for _, e := range entries {
		m.mu.Lock()
		c := e.cancel
		m.mu.Unlock()
		if c != nil {
			c()
		}
	}
}

// toolset 按类型返回工具集（定案第 8 条）：
//   - general-purpose：内置 12 − ask_user + 5 控制工具 + wait_task
//   - explore：只读 5（read_file/list_dir/glob/skill + 只读 shell，2026-08-16）
func (m *Manager) toolset(kind string) []tools.Tool {
	switch kind {
	case KindExplore:
		return []tools.Tool{
			tools.ReadFileTool{}, tools.ListDirTool{}, tools.GlobTool{},
			tools.SkillTool{SkillsDir: m.opts.GlobalSkillsDir},
			// 只读 shell（2026-08-16）：白名单命令强制只读（tools.IsSafeCommand），
			// explore 可跑 git 查询/文件查看等；写操作与 kill_pid 一律拒绝。
			tools.ShellCommandTool{Readonly: true},
		}
	default:
		var out []tools.Tool
		for _, t := range tools.Builtins(m.opts.GlobalSkillsDir) {
			if t.Name() == "ask_user" {
				continue // general-purpose 减 ask_user（用户经主 agent 转达）
			}
			out = append(out, t)
		}
		out = append(out, m.controlTools()...)
		out = append(out, tools.WaitTaskTool{})
		return out
	}
}

// controlTools 返回 5 个控制工具（主装配与 general-purpose 共用）。
func (m *Manager) controlTools() []tools.Tool {
	return []tools.Tool{
		SpawnAgentTool{m},
		SendMessageTool{m},
		InterruptAgentTool{m},
		ResumeAgentTool{m},
		ListAgentsTool{m},
	}
}

// ControlTools 返回主装配用的控制工具（app 拼主装配：
// tools.Builtins + subagent.ControlTools(m)）。
func ControlTools(m *Manager) []tools.Tool { return m.controlTools() }

// get 按 id 查注册表。
func (m *Manager) get(id string) (*Entry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	return e, ok
}

// statusOf 锁内读状态（Interrupt/Send/Resume 并发读安全）。
func (m *Manager) statusOf(id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[id]; ok {
		return e.Status
	}
	return ""
}

// isDescendant 判断 id 是否属于 ancestor 会话的后代（沿 ParentID 链上溯；
// 主会话 = 全部子 agent 的祖先，链最终指向主会话 id 时匹配）。目标/链路
// 不在注册表（磁盘历史/无关会话）→ false。Interrupt 归属校验用
// （2026-08-16 修复：拒绝自己/祖先/兄弟/无关 agent）。
func (m *Manager) isDescendant(id, ancestor string) bool {
	for {
		e, ok := m.get(id)
		if !ok {
			return false
		}
		if e.ParentID == ancestor {
			return true
		}
		id = e.ParentID
	}
}

// Session 锁内读子会话实例（TUI 查看模式复用——运行中/刚收尾的子用同一
// writer，避免 ResumeAt 开第二个 writer 写同一 transcript）。
func (m *Manager) Session(id string) (*session.Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	if !ok {
		return nil, false
	}
	return e.session, true
}

// resolveModel 解析子模型：spawn 覆盖 > 父 rc.Model > provider 默认。
func (m *Manager) resolveModel(rc *middleware.RuntimeContext, override string) string {
	if override != "" {
		return override
	}
	if rc != nil && rc.Model != "" {
		return rc.Model
	}
	if m.opts.Provider != nil {
		return m.opts.Provider.Model
	}
	return ""
}

// resolveCWD 子会话 CWD 继承父的（state.CWD，ADR-028）。
func (m *Manager) resolveCWD(rc *middleware.RuntimeContext) string {
	if rc != nil && rc.State != nil && rc.State.CWD != "" {
		return rc.State.CWD
	}
	return ""
}

// defaultName 默认子 agent 名：<type>-<短id>（可读且可区分）。
func defaultName(kind, id string) string {
	short := id
	if len(short) > 8 {
		short = short[len(short)-8:]
	}
	return kind + "-" + short
}

// normalizeKind 校验/归一化 agent_type（空 = general-purpose）。
func normalizeKind(kind string) string {
	if kind == "" {
		return KindGeneralPurpose
	}
	return kind
}
