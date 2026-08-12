package impl

import (
	"context"

	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
)

// UsageMiddleware 是 onReasoning after 中间件：把每轮采样的 token 用量累计进
// AgentState（ADR-037 用量展示）。
//
// 数据流：agent.sample 在模型调用完成后把本轮 usage 存 rc.attrs["round_usage"]
// （agent 核心不碰 state，ADR-021），本中间件在采样返回后读取并：
//   - AddUsage：累计会话总账（/usage 命令展示，跨轮 + resume 恢复）
//   - SetLastContextTokens：记录最近一次请求的 input_tokens = 当前上下文占用
//     （TUI footer 实时展示 + 压缩触发，阶段 C 用）
//
// 无状态（ADR-026）：不持有会话对象，从 rc.State 读写；共享 chain 可并发。
type UsageMiddleware struct {
	middleware.Base
}

func (m UsageMiddleware) OnReasoning(ctx context.Context, rc *middleware.RuntimeContext, in middleware.ReasoningInput, next middleware.ReasoningHandler) error {
	err := next(ctx, rc, in)
	if err != nil || rc == nil || rc.State == nil {
		return err
	}
	if u, ok := rc.Get("round_usage").(*messages.Usage); ok && u != nil {
		rc.State.AddUsage(*u)
		rc.State.SetLastContextTokens(u.InputTokens)
	}
	return err
}
