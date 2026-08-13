package impl

import (
	"context"

	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
)

// UsageMiddleware 是 onReasoning after 中间件：把每轮采样的 token 用量写进
// AgentState（ADR-037 用量展示）。
//
// 数据流：agent.sample 在模型调用完成后把本轮 usage 存 rc.attrs["round_usage"]
// （agent 核心不碰 state，ADR-021），本中间件在采样返回后读取并：
//   - SetUsage：覆盖最近一次调用的完整用量（/usage 命令展示，ADR-037 勘误
//     2026-08-13：覆盖而非累计——cache_read 是"当前历史全量"而非增量，跨轮
//     累加会虚高；每次 API 返回的 usage 就是该次调用的完整账目）
//   - SetLastContextTokens：记录最近一次请求的**完整上下文占用**（单轮
//     input + cache_read + cache_creation + output，opencode tokens.total 口径，
//     ADR-037 勘误）：TUI footer 实时展示 + 压缩触发（ShouldCompact）用。
//     注意不能只记 input_tokens——DeepSeek 等端点的 input_tokens 只统计未命中
//     缓存的新增输入，历史上下文在 cache_read 里；只记 input 会把占用低估
//     十几倍，导致 footer 显示异常（ctx 0k）且压缩永不触发（ADR-037 勘误）。
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
		rc.State.SetUsage(*u)
		// 单轮完整占用（opencode tokens.total 同款）：全部输入（含缓存）+ 输出。
		// cache_read 即"当前历史大小"（缓存前缀=历史全量），总和天然随会话单调增长。
		rc.State.SetLastContextTokens(u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens + u.OutputTokens)
	}
	return err
}
