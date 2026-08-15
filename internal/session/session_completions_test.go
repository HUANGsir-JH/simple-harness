package session

import (
	"path/filepath"
	"testing"

	"github.com/agent-project/harness/internal/completion"
	"github.com/agent-project/harness/internal/messages"
)

func newTestProject(t *testing.T) *Project {
	t.Helper()
	root := t.TempDir()
	store := NewAt(root)
	projPath := filepath.Join(root, "proj")
	return &Project{Path: projPath, Dir: store.ProjectDir(projPath)}
}

// TestRuntimeContextInjectsCompletionHooks 验证 rc 注入 Completions + AppendUser
// 两个钩子（2026-08-13）：AppendUser = AddUser，写 conversation + transcript
// user 行。
func TestRuntimeContextInjectsCompletionHooks(t *testing.T) {
	proj := newTestProject(t)
	sess, err := proj.Create("claude-sonnet-5", proj.Path, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer sess.Close()

	rc := sess.RuntimeContext()
	if rc.Completions == nil {
		t.Fatal("rc.Completions 应注入 session 队列")
	}
	if rc.AppendUser == nil {
		t.Fatal("rc.AppendUser 应注入 AddUser")
	}
	if rc.Completions != sess.Completions() {
		t.Error("rc.Completions 应与 session 队列同源")
	}
	rc.AppendUser("（系统通知：后台进程 42 已退出）")
	conv := sess.Conversation()
	if len(conv.Messages) != 1 || conv.Messages[0].Role != messages.RoleUser ||
		conv.Messages[0].Content != "（系统通知：后台进程 42 已退出）" {
		t.Errorf("AppendUser 应写 conversation user 消息: %+v", conv.Messages)
	}
	// transcript user 行（零改动：通知复用 LineTypeUser）。
	sess.writer.Flush()
	lines, skipped, err := sess.TranscriptLines()
	if err != nil || skipped != 0 {
		t.Fatalf("TranscriptLines: %v skipped=%d", err, skipped)
	}
	found := false
	for _, l := range lines {
		if l.Type == LineTypeUser && l.Content == "（系统通知：后台进程 42 已退出）" {
			found = true
		}
	}
	if !found {
		t.Errorf("transcript 应含通知 user 行: %+v", lines)
	}
}

// TestResumeRestoresPendingCompletions 验证 resume 恢复未注入的完成事件
// （completions.json 加载 → 下次采样前仍可注入）。
func TestResumeRestoresPendingCompletions(t *testing.T) {
	proj := newTestProject(t)
	sess, err := proj.Create("claude-sonnet-5", proj.Path, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// 模拟生产端：进程已完成、事件已落盘但未注入。
	sess.Completions().Append(completion.Event{
		ToolName:  "shell_command",
		Result:    "（系统通知：后台进程 7 已退出（exit 0）。日志：x）",
		SessionID: sess.ID,
	})
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	info, ok := proj.Last()
	if !ok {
		t.Fatal("Last 无会话")
	}
	rs, err := proj.Resume(info)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer rs.Close()

	if rs.Completions().PendingCount() != 1 {
		t.Fatalf("resume 后 pending 应为 1，got %d", rs.Completions().PendingCount())
	}
	// 注入端行为（与 BackgroundCompletionMiddleware 相同语义）。
	drained := rs.Completions().Drain()
	if len(drained) != 1 || drained[0].Result == "" {
		t.Fatalf("drained: %+v", drained)
	}
	rs.AddUser(drained[0].Result)
	if got := rs.Conversation().Messages; len(got) != 1 || got[0].Content != drained[0].Result {
		t.Errorf("注入后 conversation 不符: %+v", got)
	}
}
