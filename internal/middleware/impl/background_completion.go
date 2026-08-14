package impl

import (
	"context"

	"github.com/agent-project/harness/internal/events"
	"github.com/agent-project/harness/internal/middleware"
)

// BackgroundCompletionMiddleware 是后台任务完成通知的注入中间件
// （通用 async 通道，2026-08-13）。挂 onReasoning **before**：每次采样前
// Drain 会话完成事件队列，逐条以 user 消息注入对话（路径 A）。
//
// 信号分流：与 TUI 唤醒器（路径 B）读同一个 Queue——会话在采样则本轮注入；
// 会话空闲则唤醒器拉起新 run，新 run 首采样前本中间件把 pending 注入。
// Drain 清空后 PendingCount()==0 天然防重（唤醒器不注入内容，只启动 run）。
//
// 注入后同步 in.Messages（CompactMiddleware 同款）：agent.Run 采样轮开始时
// 捕获的快照不含新注入的 user 消息，不重读会漏发。同时经 rc.Emit 推
// EventNotice（路径 A 可见性：TUI 时间线是事件驱动的，否则模型会突然回应
// 一条界面上从未出现过的通知；transcript 不落盘该类型——user 行已由
// AppendUser 写入）。
//
// 无状态（ADR-026）：不持任何可变状态，共享 chain 可并发。挂载在
// CompactMiddleware 之后、TodoReminderMiddleware 之前（agent.Build）。
type BackgroundCompletionMiddleware struct {
	middleware.Base
}

func (BackgroundCompletionMiddleware) OnReasoning(ctx context.Context, rc *middleware.RuntimeContext, in middleware.ReasoningInput, next middleware.ReasoningHandler) error {
	if rc != nil && rc.Completions != nil && rc.AppendUser != nil {
		if drained := rc.Completions.Drain(); len(drained) > 0 {
			for _, ev := range drained {
				rc.AppendUser(ev.Result)
				if rc.Emit != nil {
					rc.Emit(events.Event{Type: events.EventNotice, Text: ev.Result})
				}
			}
			// 注入后同步采样输入：本轮采样必须携带刚注入的通知。
			in.Messages = rc.Messages.Messages
		}
	}
	return next(ctx, rc, in)
}
