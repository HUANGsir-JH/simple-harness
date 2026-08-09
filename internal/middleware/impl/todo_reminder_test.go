package impl

import (
	"context"
	"strings"
	"testing"

	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
)

// runReasoning 执行一轮 OnReasoning，返回 next 实际拿到的 Messages（注入后）。
func runReasoning(mw TodoReminderMiddleware, rc *middleware.RuntimeContext, msgs []*messages.Message) []*messages.Message {
	in := middleware.ReasoningInput{Messages: msgs}
	_ = mw.OnReasoning(context.Background(), rc, in, func(_ context.Context, _ *middleware.RuntimeContext, in middleware.ReasoningInput) error {
		msgs = in.Messages
		return nil
	})
	return msgs
}

func reminderRC(t *testing.T) (*middleware.RuntimeContext, []*messages.Message) {
	t.Helper()
	rc := middleware.NewRuntimeContext()
	rc.State = agentstate.New("s1", "m", ".")
	rc.State.ReplaceTodos([]agentstate.TodoItem{
		{Position: 1, Description: "修复登录 bug", Status: agentstate.TodoInProgress},
		{Position: 2, Description: "写测试", Status: agentstate.TodoPending},
	})
	conv := []*messages.Message{{Role: messages.RoleUser, Content: "你好"}}
	return rc, conv
}

// TestReminderEmptyTodosNotTriggered 验证 todo 为空时永不触发（计数仍递增）。
func TestReminderEmptyTodosNotTriggered(t *testing.T) {
	rc := middleware.NewRuntimeContext()
	rc.State = agentstate.New("s1", "m", ".") // Todos 空
	conv := []*messages.Message{{Role: messages.RoleUser, Content: "hi"}}
	mw := TodoReminderMiddleware{}
	for i := 0; i < TodoReminderThreshold+2; i++ {
		if got := runReasoning(mw, rc, conv); len(got) != len(conv) {
			t.Fatalf("第 %d 轮空 todos 不应注入: got %d want %d", i+1, len(got), len(conv))
		}
	}
}

// TestReminderBelowThreshold 验证未到阈值不触发（第 9 轮）。
func TestReminderBelowThreshold(t *testing.T) {
	rc, conv := reminderRC(t)
	mw := TodoReminderMiddleware{}
	for i := 0; i < TodoReminderThreshold-1; i++ {
		got := runReasoning(mw, rc, conv)
		if len(got) != len(conv) {
			t.Fatalf("第 %d 轮不应注入: got %d want %d", i+1, len(got), len(conv))
		}
	}
}

// TestReminderTriggersAtThreshold 验证第 10 轮（cnt-last >= 10）注入。
func TestReminderTriggersAtThreshold(t *testing.T) {
	rc, conv := reminderRC(t)
	mw := TodoReminderMiddleware{}
	for i := 0; i < TodoReminderThreshold; i++ {
		got := runReasoning(mw, rc, conv)
		if i == TodoReminderThreshold-1 {
			if len(got) != len(conv)+1 {
				t.Fatalf("第 %d 轮应注入: got %d want %d", i+1, len(got), len(conv)+1)
			}
		} else if len(got) != len(conv) {
			t.Fatalf("第 %d 轮不应注入: got %d want %d", i+1, len(got), len(conv))
		}
	}
}

// TestReminderInjectsAtTail 验证提醒注入在消息列表尾部（最后一条），内容含
// 渲染后的待办清单。
func TestReminderInjectsAtTail(t *testing.T) {
	rc, conv := reminderRC(t)
	mw := TodoReminderMiddleware{}
	var got []*messages.Message
	for i := 0; i < TodoReminderThreshold; i++ {
		got = runReasoning(mw, rc, conv)
	}
	last := got[len(got)-1]
	if last.Role != messages.RoleUser {
		t.Errorf("提醒应为 user 角色: %s", last.Role)
	}
	if !strings.Contains(last.Content, "系统提醒") {
		t.Errorf("提醒应含引导语: %s", last.Content)
	}
	if !strings.Contains(last.Content, "修复登录 bug") || !strings.Contains(last.Content, "[~]") {
		t.Errorf("提醒应含渲染后的待办: %s", last.Content)
	}
}

// TestReminderDoesNotPolluteConversation 验证注入只影响请求副本，conversation
// 底层数组不被污染（in.Messages 比 conv 多一条，conv 自身不变）。
func TestReminderDoesNotPolluteConversation(t *testing.T) {
	rc, conv := reminderRC(t)
	mw := TodoReminderMiddleware{}
	for i := 0; i < TodoReminderThreshold; i++ {
		_ = runReasoning(mw, rc, conv)
	}
	if len(conv) != 1 {
		t.Errorf("conversation 被污染: got %d want 1", len(conv))
	}
	if conv[0].Content != "你好" {
		t.Errorf("conversation 首条被改: %s", conv[0].Content)
	}
}

// TestReminderNoRepeatAfterReminder 验证提醒注入后计数清零重计：模型一直不更新
// → 每 TodoReminderThreshold 轮提醒一次（不每轮刷屏）。
func TestReminderNoRepeatAfterReminder(t *testing.T) {
	rc, conv := reminderRC(t)
	mw := TodoReminderMiddleware{}
	for i := 0; i < TodoReminderThreshold; i++ {
		runReasoning(mw, rc, conv) // 第 10 轮触发一次
	}
	// 清零后（cnt 11..19）不重复。
	for i := 0; i < TodoReminderThreshold-1; i++ {
		if got := runReasoning(mw, rc, conv); len(got) != len(conv) {
			t.Fatalf("清零后第 %d 轮不应注入: got %d", i+1, len(got))
		}
	}
	// 重新数满 10 轮（cnt=20）再次注入。
	if got := runReasoning(mw, rc, conv); len(got) != len(conv)+1 {
		t.Fatalf("重新数满应注入: got %d", len(got))
	}
}

// TestReminderResetAfterUpdate 验证 update_todo 后（活动基准=当前轮）计数清零
// 重计：再数满阈值才再次触发。
// 偏离计数重新起算：阈值 + 额外几轮后才再次触发。
func TestReminderResetAfterUpdate(t *testing.T) {
	rc, conv := reminderRC(t)
	mw := TodoReminderMiddleware{}
	// 跑满一个周期触发一次。
	for i := 0; i < TodoReminderThreshold; i++ {
		runReasoning(mw, rc, conv)
	}
	// 模拟 update_todo：当前 cnt = 10，设为活动基准。
	rc.Set("todo_last_activity", rc.Get("todo_sample_count"))
	// 再跑 9 轮（cnt 11..19）不应触发。
	for i := 0; i < TodoReminderThreshold-1; i++ {
		if got := runReasoning(mw, rc, conv); len(got) != len(conv) {
			t.Fatalf("重置后第 %d 轮不应注入", i+1)
		}
	}
	// 第 10 轮（cnt=20, 20-10=10）再次触发。
	if got := runReasoning(mw, rc, conv); len(got) != len(conv)+1 {
		t.Fatalf("重置后第 10 轮应注入: got %d", len(got))
	}
}
