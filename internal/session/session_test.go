package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/compact"
	"github.com/agent-project/harness/internal/events"
	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/provider"
)

// TestSessionCreateResume 验证 创建 → 落盘（meta/user/agent 块）→ resume 重建
// conversation + 恢复 state 的全链路。
func TestSessionCreateResume(t *testing.T) {
	root := t.TempDir()
	store := NewAt(root)
	projPath := filepath.Join(root, "proj")
	proj := &Project{Path: projPath, Dir: store.ProjectDir(projPath)}

	sess, err := proj.Create("claude-sonnet-5", projPath, "")
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

	// 写 user + agent 块事件（thinking 带签名，ADR-025 修订完整回传）。
	sess.AddUser("hello")
	sess.OnAgentEvent(events.Event{Type: events.EventTurnStart})
	sess.OnAgentEvent(events.Event{Type: events.EventThinkingDone, MsgID: "m2", Text: "think", Signature: "sig_think"})
	sess.OnAgentEvent(events.Event{Type: events.EventTextDone, MsgID: "m2", Text: "ans"})
	sess.OnAgentEvent(events.Event{Type: events.EventToolCall, MsgID: "m2", ToolCall: &messages.ToolCall{ID: "c1", Name: "read_file", Args: json.RawMessage(`{}`)}})
	sess.OnAgentEvent(events.Event{Type: events.EventToolResult, ToolCall: &messages.ToolCall{ID: "c1"}, ToolResult: &messages.ToolResult{Success: true, Content: "x"}})
	sess.OnAgentEvent(events.Event{Type: events.EventTurnDone})
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

	conv := rs.Conversation()
	if len(conv.Messages) != 3 {
		t.Fatalf("resume conversation 消息数=%d, want 3", len(conv.Messages))
	}
	if conv.Messages[0].Role != messages.RoleUser || conv.Messages[0].Content != "hello" {
		t.Errorf("user 恢复: %+v", conv.Messages[0])
	}
	a := conv.Messages[1]
	if a.Role != messages.RoleAssistant || a.Thinking != "think" || a.Content != "ans" || len(a.ToolCalls) != 1 {
		t.Errorf("assistant 恢复: %+v", a)
	}
	if a.ThinkingSignature != "sig_think" {
		t.Errorf("thinking signature 恢复: got %q want sig_think", a.ThinkingSignature)
	}
	if conv.Messages[2].Role != messages.RoleTool || len(conv.Messages[2].ToolResults) != 1 {
		t.Errorf("tool 恢复: %+v", conv.Messages[2])
	}
	// state 恢复。
	if rs.State().SessionID != sess.ID || rs.State().Model != "claude-sonnet-5" {
		t.Errorf("state 恢复: %+v", rs.State())
	}
	// state.CWD 存会话启动目录（ADR-028），resume 后保留。
	if rs.State().CWD != projPath {
		t.Errorf("state.CWD: got %q want %q", rs.State().CWD, projPath)
	}
}

// TestSessionRuntimeContext 验证 RuntimeContext() 从会话填充 per-call 上下文
// （无状态 agent 对位 ADR-026：agent 不持有会话，一切经 rc 传入）。
func TestSessionRuntimeContext(t *testing.T) {
	root := t.TempDir()
	store := NewAt(root)
	proj := &Project{Path: filepath.Join(root, "proj"), Dir: store.ProjectDir(filepath.Join(root, "proj"))}
	sess, err := proj.Create("claude-sonnet-5", filepath.Join(root, "proj"), "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer sess.Close()

	rc := sess.RuntimeContext()
	if rc.SessionID != sess.ID {
		t.Errorf("SessionID: got %q", rc.SessionID)
	}
	if rc.Messages != sess.conversation {
		t.Error("Messages 应指向会话 conversation")
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
	sess, err := proj.Create("m1", filepath.Join(root, "proj"), "")
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

// TestCreatePermissionMode 验证默认审批模式在创建时固化进 AgentState
// （ADR-029：config 默认播种，之后 /permission 切换改会话 state）；空 mode
// 不固化（Permission nil，审批回退默认）。
func TestCreatePermissionMode(t *testing.T) {
	root := t.TempDir()
	store := NewAt(root)
	proj := &Project{Path: root, Dir: store.ProjectDir(root)}

	sess, err := proj.Create("m", root, "readonly")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer sess.Close()
	if sess.state.PermissionMode() != "readonly" {
		t.Errorf("Create 应固化 mode=readonly: %q", sess.state.PermissionMode())
	}
	// RuntimeContext 反映固化值（ApprovalMiddleware 经 rc.State 读模式）。
	if rc := sess.RuntimeContext(); rc.State.PermissionMode() != "readonly" {
		t.Errorf("RuntimeContext 未反映固化 mode: %q", rc.State.PermissionMode())
	}
	// 重新加载验证已落盘。
	st, err := agentstate.LoadFile(sess.statePath)
	if err != nil || st.PermissionMode() != "readonly" {
		t.Errorf("落盘 state permission: %q err=%v", st.PermissionMode(), err)
	}

	// 空 mode → 不固化。
	sess2, err := proj.Create("m", root, "")
	if err != nil {
		t.Fatalf("Create empty mode: %v", err)
	}
	defer sess2.Close()
	if sess2.state.PermissionMode() != "" {
		t.Errorf("空 mode 不应固化 Permission: %q", sess2.state.PermissionMode())
	}
}

// TestSetPermissionMode 验证 /permission 切换模式并落盘。
func TestSetPermissionMode(t *testing.T) {
	root := t.TempDir()
	store := NewAt(root)
	proj := &Project{Path: root, Dir: store.ProjectDir(root)}
	sess, err := proj.Create("m", root, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer sess.Close()

	if err := sess.SetPermissionMode("bypass"); err != nil {
		t.Fatalf("SetPermissionMode: %v", err)
	}
	if rc := sess.RuntimeContext(); rc.State.PermissionMode() != "bypass" {
		t.Errorf("RuntimeContext: %q", rc.State.PermissionMode())
	}
	st, err := agentstate.LoadFile(sess.statePath)
	if err != nil || st.PermissionMode() != "bypass" {
		t.Errorf("落盘: %q err=%v", st.PermissionMode(), err)
	}
}

// TestSessionSetNamePersist 验证 SetName 落盘 + Sessions() 列表填充 name
// （首消息命名 / /rename 持久化，列表展示用）。
func TestSessionSetNamePersist(t *testing.T) {
	root := t.TempDir()
	store := NewAt(root)
	proj := &Project{Path: root, Dir: store.ProjectDir(root)}
	sess, err := proj.Create("m1", root, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer sess.Close()

	if err := sess.SetName("修复 bug"); err != nil {
		t.Fatalf("SetName: %v", err)
	}
	st, err := agentstate.LoadFile(sess.statePath)
	if err != nil || st.Name != "修复 bug" {
		t.Errorf("落盘 name: %+v err=%v", st, err)
	}

	// Sessions() 列表一次 LoadFile 填充 Name。
	list, err := proj.Sessions()
	if err != nil || len(list) != 1 || list[0].Name != "修复 bug" {
		t.Errorf("Sessions 填充 name: %+v err=%v", list, err)
	}
}

// TestCompactSegmentResume 验证压缩切段 + seed 落盘 → resume 重建 conversation =
// 单一 summary user（ADR-037 纯占位）：transcript 新段以摘要行开头，旧段纯审计。
func TestCompactSegmentResume(t *testing.T) {
	root := t.TempDir()
	store := NewAt(root)
	proj := &Project{Path: filepath.Join(root, "proj"), Dir: store.ProjectDir(filepath.Join(root, "proj"))}
	sess, err := proj.Create("m1", filepath.Join(root, "proj"), "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	sess.AddUser("hello")
	sess.OnAgentEvent(events.Event{Type: events.EventTurnStart})
	sess.OnAgentEvent(events.Event{Type: events.EventTextDone, MsgID: "m1", Text: "ans"})
	sess.OnAgentEvent(events.Event{Type: events.EventTurnDone})

	// 超阈值跑压缩（Runner 经 rc.Segment 注入的真实 writer 切段 + seed 落盘）。
	rc := sess.RuntimeContext()
	rc.State.SetLastContextTokens(900_000)
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return provider.NewFakeStream([]provider.Event{
			{Type: provider.EventTextDone, Text: "压缩摘要"},
			{Type: provider.EventDone},
		}), nil
	}}
	r := compact.NewRunner(compact.NewSummarizer(fc, compact.Options{ContextWindow: 1_000_000}), compact.Options{ContextWindow: 1_000_000})
	if err := r.Run(context.Background(), rc); err != nil {
		t.Fatalf("compact Run: %v", err)
	}
	// AgentState 落盘（真实流程：自动压缩由 SessionMiddleware onAgent after 落盘、
	// 手动 /compact 由 Controller.RunCompact 显式落盘；这里模拟）。
	if err := agentstate.SaveFile(sess.StatePath(), sess.State()); err != nil {
		t.Fatalf("SaveFile: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// resume：conversation = 单一 summary user（摘要即消息，不依赖 state 副本）。
	info, ok := proj.Last()
	if !ok {
		t.Fatal("Last 无会话")
	}
	rs, err := proj.Resume(info)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer rs.Close()
	conv := rs.Conversation()
	if len(conv.Messages) != 1 || conv.Messages[0].Role != messages.RoleUser || conv.Messages[0].Content != "压缩摘要" {
		t.Fatalf("resume conversation 应为单一 summary user: %+v", conv.Messages)
	}
}

// TestCreateInSubagent 验证子 agent 会话创建（ADR-045）：指定目录新建
// （<父会话目录>/subagents/<子id>/）+ 血缘播种 + AddUser 后 Resume 恢复
// （续接原 transcript 段，非新开）。
func TestCreateInSubagent(t *testing.T) {
	root := t.TempDir()
	store := NewAt(root)
	projPath := filepath.Join(root, "proj")
	proj := &Project{Path: projPath, Dir: store.ProjectDir(projPath)}
	parent, err := proj.Create("m1", projPath, "")
	if err != nil {
		t.Fatalf("parent Create: %v", err)
	}
	parent.AddUser("parent user")
	if err := parent.Close(); err != nil {
		t.Fatalf("parent Close: %v", err)
	}

	// 子会话：嵌套目录 + 血缘播种 + Permission 快照继承。
	subID := NewID("sub")
	subDir := filepath.Join(parent.Dir(), DirSubagents, subID)
	st := agentstate.New(subID, "m1", projPath)
	st.SetSubagent(parent.ID, "general-purpose", 1)
	st.SetName("子任务")
	st.Permission = &agentstate.PermissionState{Mode: "readonly", Approved: []string{"git status"}}
	sub, err := CreateIn(subDir, st)
	if err != nil {
		t.Fatalf("CreateIn: %v", err)
	}
	// 目录骨架。
	for _, d := range []string{subDir, filepath.Join(subDir, DirHistorys), filepath.Join(subDir, DirPlans)} {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Errorf("缺目录 %s", d)
		}
	}
	// 血缘播种。
	if sub.State().ParentID != parent.ID || sub.State().AgentType != "general-purpose" || sub.State().Depth != 1 {
		t.Errorf("血缘播种: %+v", sub.State())
	}
	if sub.State().PermissionMode() != "readonly" {
		t.Errorf("权限继承: %+v", sub.State().Permission)
	}
	sub.AddUser("任务：分析目录")
	if err := sub.Close(); err != nil {
		t.Fatalf("sub Close: %v", err)
	}

	// Resume 恢复：续接原段（消息数 = 原 1 条 + 新追加 1 条）。
	rs, err := proj.Resume(SessionInfo{ID: subID, Path: subDir})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer rs.Close()
	conv := rs.Conversation()
	if len(conv.Messages) != 1 || conv.Messages[0].Content != "任务：分析目录" {
		t.Fatalf("resume 恢复失败: %+v", conv.Messages)
	}
	// Resume 后继续写（续接原 jsonl，非新文件）。
	rs.AddUser("补充指令")
	if len(conv.Messages) != 2 || conv.Messages[1].Content != "补充指令" {
		t.Fatalf("resume 续接失败: %+v", conv.Messages)
	}
	if err := rs.Close(); err != nil {
		t.Fatalf("rs Close: %v", err)
	}
	files, err := filepath.Glob(filepath.Join(subDir, DirHistorys, "history-*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("应只有一段 history 文件（续接而非新开）: %v %v", files, err)
	}
}
