package impl

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/agent-project/harness/internal/completion"
	"github.com/agent-project/harness/internal/events"
	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
)

// testCompletionRC 构造带完成队列 + AppendUser 的测试 rc（注入中间件最小环境）。
func testCompletionRC(t *testing.T) *middleware.RuntimeContext {
	t.Helper()
	rc := middleware.NewRuntimeContext()
	rc.Messages = messages.NewConversation()
	rc.Completions = completion.New(filepath.Join(t.TempDir(), "completions.json"))
	var appended []string
	rc.AppendUser = func(c string) {
		appended = append(appended, c)
		rc.Messages.Add(messages.NewUserMessage(c)) // 生产 = Session.AddUser 的 conversation.Add
	}
	// 经 rc.attrs 暂存断言数据（同一 call 内单线程）。
	rc.Set("test_appended", &appended)
	return rc
}

// TestBackgroundCompletionInjects 验证 onReasoning before：Drain 后逐条
// AppendUser 注入、in.Messages 同步为注入后的 conversation、pending 清空。
func TestBackgroundCompletionInjects(t *testing.T) {
	rc := testCompletionRC(t)
	rc.Completions.Append(completion.Event{Result: "通知A"})
	rc.Completions.Append(completion.Event{Result: "通知B"})

	m := BackgroundCompletionMiddleware{}
	var nextIn middleware.ReasoningInput
	err := m.OnReasoning(context.Background(), rc, middleware.ReasoningInput{
		Messages: rc.Messages.Messages, // 模拟采样轮开始时的快照
	}, func(ctx context.Context, rc *middleware.RuntimeContext, in middleware.ReasoningInput) error {
		nextIn = in
		return nil
	})
	if err != nil {
		t.Fatalf("OnReasoning: %v", err)
	}
	appended := rc.Get("test_appended").(*[]string)
	if len(*appended) != 2 || (*appended)[0] != "通知A" || (*appended)[1] != "通知B" {
		t.Errorf("AppendUser 应收到两条注入: %v", *appended)
	}
	if rc.Completions.PendingCount() != 0 {
		t.Errorf("Drain 后 pending 应为 0，got %d", rc.Completions.PendingCount())
	}
	// in.Messages 同步：next 看到的输入必须含注入的 user 消息。
	if len(nextIn.Messages) != 2 || nextIn.Messages[0].Content != "通知A" || nextIn.Messages[1].Content != "通知B" {
		t.Errorf("next 收到的 Messages 未同步注入: %+v", nextIn.Messages)
	}
}

// TestBackgroundCompletionEmitsNotice 验证注入后经 rc.Emit 推 EventNotice
// （路径 A 可见性）；Emit nil 时不 panic。
func TestBackgroundCompletionEmitsNotice(t *testing.T) {
	rc := testCompletionRC(t)
	rc.Completions.Append(completion.Event{Result: "通知C"})
	var notices []events.Event
	rc.Emit = func(ev events.Event) { notices = append(notices, ev) }

	m := BackgroundCompletionMiddleware{}
	err := m.OnReasoning(context.Background(), rc, middleware.ReasoningInput{Messages: rc.Messages.Messages},
		func(ctx context.Context, rc *middleware.RuntimeContext, in middleware.ReasoningInput) error {
			return nil
		})
	if err != nil {
		t.Fatalf("OnReasoning: %v", err)
	}
	if len(notices) != 1 || notices[0].Type != events.EventNotice || notices[0].Text != "通知C" {
		t.Errorf("应推 EventNotice{Text=通知C}: %+v", notices)
	}

	// Emit nil：透传不 panic。
	rc2 := testCompletionRC(t)
	rc2.Completions.Append(completion.Event{Result: "D"})
	if err := m.OnReasoning(context.Background(), rc2, middleware.ReasoningInput{Messages: rc2.Messages.Messages},
		func(ctx context.Context, rc *middleware.RuntimeContext, in middleware.ReasoningInput) error {
			return nil
		}); err != nil {
		t.Fatalf("Emit nil 路径: %v", err)
	}
}

// TestBackgroundCompletionPassthrough 验证空队列与 Completions nil 透传
// （不注入、不改写 in.Messages、next 照常调用）。
func TestBackgroundCompletionPassthrough(t *testing.T) {
	m := BackgroundCompletionMiddleware{}
	nextCalled := false
	next := func(ctx context.Context, rc *middleware.RuntimeContext, in middleware.ReasoningInput) error {
		nextCalled = true
		return nil
	}

	// 空队列。
	rc := testCompletionRC(t)
	in := middleware.ReasoningInput{Messages: rc.Messages.Messages}
	if err := m.OnReasoning(context.Background(), rc, in, next); err != nil {
		t.Fatalf("空队列: %v", err)
	}
	if appended := rc.Get("test_appended").(*[]string); len(*appended) != 0 {
		t.Error("空队列不应注入")
	}

	// Completions nil（非会话/测试场景）。
	rcNil := middleware.NewRuntimeContext()
	if err := m.OnReasoning(context.Background(), rcNil, middleware.ReasoningInput{}, next); err != nil {
		t.Fatalf("Completions nil: %v", err)
	}

	// AppendUser nil（Completions 非 nil 但无注入能力）：安全跳过。
	rcNoAppend := middleware.NewRuntimeContext()
	rcNoAppend.Completions = completion.New(filepath.Join(t.TempDir(), "c.json"))
	rcNoAppend.Completions.Append(completion.Event{Result: "E"})
	if err := m.OnReasoning(context.Background(), rcNoAppend, middleware.ReasoningInput{}, next); err != nil {
		t.Fatalf("AppendUser nil: %v", err)
	}
	if rcNoAppend.Completions.PendingCount() != 1 {
		t.Error("AppendUser nil 时不应 Drain（留给下次/唤醒）")
	}
	if !nextCalled {
		t.Error("next 应被调用")
	}
}
