package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-project/harness/internal/agent"
	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/messages"
)

// TestSessionCreateResume 验证 创建 → 落盘（meta/user/agent 块）→ resume 重建
// thread + 恢复 state 的全链路。
func TestSessionCreateResume(t *testing.T) {
	root := t.TempDir()
	store := NewAt(root)
	projPath := filepath.Join(root, "proj")
	proj := &Project{Path: projPath, Dir: store.ProjectDir(projPath)}

	sess, err := proj.Create("claude-sonnet-5")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// 目录骨架。
	for _, d := range []string{sess.Dir(), filepath.Join(sess.Dir(), DirHistorys), filepath.Join(sess.Dir(), DirPlans)} {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Errorf("缺目录 %s", d)
		}
	}
	if _, err := os.Stat(sess.StatePath()); err != nil {
		t.Errorf("缺 agentstate.json: %v", err)
	}

	// 写 user + agent 块事件。
	sess.AddUser("hello")
	sess.OnAgentEvent(agent.Event{Type: agent.EventTurnStart})
	sess.OnAgentEvent(agent.Event{Type: agent.EventThinkingDone, MsgID: "m2", Text: "think"})
	sess.OnAgentEvent(agent.Event{Type: agent.EventTextDone, MsgID: "m2", Text: "ans"})
	sess.OnAgentEvent(agent.Event{Type: agent.EventToolCall, MsgID: "m2", ToolCall: &messages.ToolCall{ID: "c1", Name: "read_file", Args: json.RawMessage(`{}`)}})
	sess.OnAgentEvent(agent.Event{Type: agent.EventToolResult, ToolCall: &messages.ToolCall{ID: "c1"}, ToolResult: &messages.ToolResult{Success: true, Content: "x"}})
	sess.OnAgentEvent(agent.Event{Type: agent.EventTurnDone})
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// resume：Last 找到刚创建的会话。
	info, ok := proj.Last()
	if !ok {
		t.Fatal("Last 无会话")
	}
	rs, err := proj.Resume(info)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer rs.Close()

	th := rs.Thread()
	if len(th.Messages) != 3 {
		t.Fatalf("resume thread 消息数=%d, want 3", len(th.Messages))
	}
	if th.Messages[0].Role != messages.RoleUser || th.Messages[0].Content != "hello" {
		t.Errorf("user 恢复: %+v", th.Messages[0])
	}
	a := th.Messages[1]
	if a.Role != messages.RoleAssistant || a.Thinking != "think" || a.Content != "ans" || len(a.ToolCalls) != 1 {
		t.Errorf("assistant 恢复: %+v", a)
	}
	if th.Messages[2].Role != messages.RoleTool || len(th.Messages[2].ToolResults) != 1 {
		t.Errorf("tool 恢复: %+v", th.Messages[2])
	}
	// state 恢复。
	if rs.State().SessionID != sess.ID || rs.State().Model != "claude-sonnet-5" {
		t.Errorf("state 恢复: %+v", rs.State())
	}
}

// TestSessionRuntimeContext 验证 RuntimeContext() 从会话填充 per-call 上下文
// （无状态 agent 对位 ADR-026：agent 不持有会话，一切经 rc 传入）。
func TestSessionRuntimeContext(t *testing.T) {
	root := t.TempDir()
	store := NewAt(root)
	proj := &Project{Path: filepath.Join(root, "proj"), Dir: store.ProjectDir(filepath.Join(root, "proj"))}
	sess, err := proj.Create("claude-sonnet-5")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer sess.Close()

	rc := sess.RuntimeContext()
	if rc.SessionID != sess.ID {
		t.Errorf("SessionID: got %q", rc.SessionID)
	}
	if rc.Messages != sess.thread {
		t.Error("Messages 应指向会话 thread")
	}
	if rc.State != sess.state {
		t.Error("State 应指向会话 state")
	}
	if rc.StatePath != sess.statePath {
		t.Error("StatePath 不匹配")
	}
	if rc.Model != "claude-sonnet-5" {
		t.Errorf("Model: got %q", rc.Model)
	}
}

// TestSessionSetModelEffortPersist 验证 SetModel/SetThinkingEffort/SetThinkingEnabled
// 修改 state 并立即落盘（/model /effort 运行时切换的持久化）。
func TestSessionSetModelEffortPersist(t *testing.T) {
	root := t.TempDir()
	store := NewAt(root)
	proj := &Project{Path: filepath.Join(root, "proj"), Dir: store.ProjectDir(filepath.Join(root, "proj"))}
	sess, err := proj.Create("m1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer sess.Close()

	if err := sess.SetModel("m2"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	if err := sess.SetThinkingEffort("max"); err != nil {
		t.Fatalf("SetThinkingEffort: %v", err)
	}
	if err := sess.SetThinkingEnabled(testBoolPtr(false)); err != nil {
		t.Fatalf("SetThinkingEnabled: %v", err)
	}

	// 重新加载验证已落盘。
	st, err := agentstate.LoadFile(sess.statePath)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if st.Model != "m2" || st.ThinkingEffort != "max" || st.ThinkingEnabled == nil || *st.ThinkingEnabled {
		t.Errorf("落盘 state: %+v", st)
	}
	// RuntimeContext 反映新值。
	rc := sess.RuntimeContext()
	if rc.Model != "m2" || rc.ThinkingEffort != "max" || rc.ThinkingEnabled == nil || *rc.ThinkingEnabled {
		t.Errorf("RuntimeContext 未反映新值: %+v", rc)
	}
}

// testBoolPtr 测试用指针构造。
func testBoolPtr(b bool) *bool { return &b }
