package session

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/agent-project/harness/internal/agent"
	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/messages"
)

// FileAgentState 是会话目录下的 state 文件名。
const FileAgentState = "agentstate.json"

// Session 是一个会话：thread + AgentState + 异步 transcript writer。
// 目录布局见 Store 注释（session 目录下 historys/ + agentstate.json + plans/）。
type Session struct {
	ID         string
	dir        string
	historyDir string
	statePath  string
	thread     *messages.Thread
	state      *agentstate.AgentState
	writer     *TranscriptWriter
}

// Create 新建一个会话：建目录、写 meta 首行、初始化 agentstate.json。
func (p *Project) Create(model string) (*Session, error) {
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
	st := agentstate.New(sid, model, p.Path)
	if err := agentstate.SaveFile(statePath, st); err != nil {
		return nil, err
	}
	w, err := NewTranscriptWriter(historyDir)
	if err != nil {
		return nil, err
	}
	s := &Session{
		ID:         sid,
		dir:        dir,
		historyDir: historyDir,
		statePath:  statePath,
		thread:     messages.NewThread(),
		state:      st,
		writer:     w,
	}
	// meta 首行（会话元数据，resume 可读）。
	w.Write(Line{Type: "meta", SessionID: sid, CWD: p.Path, Model: model, CreatedAt: st.CreatedAt})
	return s, nil
}

// Resume 加载已有会话：重建 thread + 恢复 state + 打开最新 transcript 继续追加。
func (p *Project) Resume(info SessionInfo) (*Session, error) {
	historyDir := filepath.Join(info.Path, DirHistorys)
	statePath := filepath.Join(info.Path, FileAgentState)
	th, err := LoadThread(historyDir)
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
		ID:         info.ID,
		dir:        info.Path,
		historyDir: historyDir,
		statePath:  statePath,
		thread:     th,
		state:      st,
		writer:     w,
	}, nil
}

// Dir 返回会话目录。
func (s *Session) Dir() string { return s.dir }

// Thread 返回会话的消息序列（resume 后含历史）。
func (s *Session) Thread() *messages.Thread { return s.thread }

// State 返回 AgentState（经 rc.State 注入 agent；StateMiddleware after 落盘）。
func (s *Session) State() *agentstate.AgentState { return s.state }

// StatePath 返回 agentstate.json 路径（StateMiddleware 用）。
func (s *Session) StatePath() string { return s.statePath }

// Writer 返回 transcript writer（CLI 转发 agent 事件用）。
func (s *Session) Writer() *TranscriptWriter { return s.writer }

// WriteUser 记录一条用户消息（写 user 行）。msg 须先 Add 进 thread。
func (s *Session) WriteUser(msg *messages.Message) {
	s.writer.Write(Line{Type: "user", MsgID: msg.ID, Content: msg.Content})
}

// OnAgentEvent 把 agent 回合级事件转发给 transcript writer（块级实时落盘）。
func (s *Session) OnAgentEvent(ev agent.Event) {
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
