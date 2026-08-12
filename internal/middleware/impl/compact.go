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
	if m.Runner != nil && rc != nil {
		if _, err := m.Runner.Run(ctx, rc, false); err != nil {
			return err
		}
	}
	return next(ctx, rc, in)
}
