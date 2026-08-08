package middleware

import (
	"context"

	"github.com/agent-project/harness/internal/messages"
)

// TodoReminderThreshold 是提醒触发阈值：距上次"todo 活动"（模型调用
// update_todo，或一次提醒注入）>= TodoReminderThreshold 次 model call 时，
// 在采样请求消息尾部注入提醒段，把模型注意力拉回待办（ADR-027；参照
// Claude Code 的 system-reminder 防偏离机制）。
const TodoReminderThreshold = 10

// TodoReminderMiddleware 是 onReasoning middleware：每轮采样前递增计数，
// 触发时在请求消息尾部注入提醒。
//
// 活动基准语义（todo_last_activity）：update_todo 工具与提醒注入都会把它设为
// 当前轮次 → 计数清零重计。效果：模型更新 todo 后 10 轮内不打扰；模型一直
// 不更新则每 10 轮提醒一次（不刷屏）。
//
// 实现要点：
//   - 计数存 rc.attrs（per-Run，随 rc 新建清零）
//   - 注入的是 **请求临时副本**（copy 新 slice），不改 rc.Messages
//     （conversation）→ 提醒不落 transcript、resume 不重放（ADR-027）；
//     且提醒在请求尾部，不影响历史前缀的缓存命中
type TodoReminderMiddleware struct {
	Base
}

func (m TodoReminderMiddleware) OnReasoning(ctx context.Context, rc *RuntimeContext, in ReasoningInput, next ReasoningHandler) error {
	cnt := 0
	if v, ok := rc.Get("todo_sample_count").(int); ok {
		cnt = v
	}
	cnt++
	rc.Set("todo_sample_count", cnt)

	if rc.State != nil && len(rc.State.Todos) > 0 {
		last := 0
		if v, ok := rc.Get("todo_last_activity").(int); ok {
			last = v
		}
		if cnt-last >= TodoReminderThreshold {
			// 必须 copy 新 slice：in.Messages 与 conversation.Messages 共享底层
			// 数组，直接 append 会写穿到 conversation（落盘污染）。
			msgs := make([]*messages.Message, len(in.Messages), len(in.Messages)+1)
			copy(msgs, in.Messages)
			msgs = append(msgs, &messages.Message{
				Role:    messages.RoleUser,
				Content: "（系统提醒：你有一份待办清单，请对照推进，不要偏离。）\n" + rc.State.RenderTodos(),
			})
			in.Messages = msgs
			// 提醒本身也算一次活动：清零重计，避免每轮重复注入刷屏。
			rc.Set("todo_last_activity", cnt)
		}
	}
	return next(ctx, rc, in)
}
