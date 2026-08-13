package impl

import (
	"context"

	"github.com/agent-project/harness/internal/compact"
	"github.com/agent-project/harness/internal/middleware"
)

// CompactMiddleware 是 onReasoning **before** 中间件：每轮采样前检查上下文是否
// 超阈值（context_window 85%，ADR-037），超则触发压缩。
//
// 错误语义：摘要失败/取消（Esc）返回错误 → agent.Run 终止（TUI 显示"上下文
// 压缩失败，请重试或 /compact"，下轮再触发）；**失败绝不重写 conversation**。
// 压缩成功：重写 rc.Messages = [summary user] + rc.Segment 切新 transcript 段
// + 置 compacted 标记（agent 读后 emit events.EventCompacted，TUI 系统行）。
//
// 无状态（ADR-026）：Runner 不可变，只从 rc 读写；共享 chain 可并发。
// 挂载点 = onReasoning（与 TodoReminder 叠加；注册在 UsageMiddleware 之前使
// 压缩的 before 先于采样、用量的 after 后于采样，见 agent.Build）。
type CompactMiddleware struct {
	middleware.Base
	Runner *compact.Runner
}

func (m CompactMiddleware) OnReasoning(ctx context.Context, rc *middleware.RuntimeContext, in middleware.ReasoningInput, next middleware.ReasoningHandler) error {
	// 判定在 middleware（ADR-037 修订 2026-08-13）：兜底估算需要工具 schema
	//（in.Tools，本轮采样将发送）+ 组合后的系统提示（rc.SystemPrompt，agent.Run
	// 已回写）——二者只在此处同时可得，Runner 因此是纯执行器（Run 无条件压缩，
	// 手动 /compact 同语义）。
	if m.Runner != nil && rc != nil && m.Runner.ShouldCompact(rc, in.Tools) {
		if err := m.Runner.Run(ctx, rc); err != nil {
			return err
		}
		// 压缩成功：采样必须用重写后的 conversation。in.Messages 是 agent.Run
		// 采样轮开始时捕获的压缩前快照，直接透传会以完整旧上下文采样——
		// 触发压缩的那轮仍可能爆窗，且该轮 usage 会把 LastContextTokens
		// 重新抬高，导致下轮重复触发压缩。
		in.Messages = rc.Messages.Messages
	}
	return next(ctx, rc, in)
}
