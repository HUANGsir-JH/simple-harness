// Package middleware 定义 agent 的进程内扩展机制（ADR-021）。
// 6 个 hook：onAgent / onReasoning / onToolCall / onActing / onModelCall（onion，
// next 前 = before、返回后 = after）+ onSystemPrompt（transformer pipeline）。
// capabilities（压缩/权限/记忆/AGENTS.md 注入）作为 middleware 挂载，
// 核心 ReAct loop 保持纯净。
//
// 每个 hook 贯穿 context.Context（取消/超时）与 *RuntimeContext（per-call
// 元数据：SessionID + 自由 attrs，参照 AgentScope）。
package middleware

import (
	"context"

	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/provider"
)

// --- 各 hook 的输入类型 ---------------------------------------------------

// AgentInput 包裹一整个回复流程（一次 agent.Run）。
type AgentInput struct {
	Messages []*messages.Message
}

// ReasoningInput 包裹一次采样轮（输入组装 → 模型调用 → 流式解码）。
type ReasoningInput struct {
	Messages []*messages.Message
	Tools    []provider.ToolSpec
}

// ToolCallInput 包裹一批工具调用的完整处理（发起 → 逐个执行 → 结果回填）。
type ToolCallInput struct {
	Calls []*messages.ToolCall
}

// ActingInput 包裹单个工具的执行。
type ActingInput struct {
	Call *messages.ToolCall
}

// ModelCallInput 包裹一次模型 API 调用（最内层）。
type ModelCallInput struct {
	Messages []*messages.Message
	Tools    []provider.ToolSpec
}

// Middleware 是 agent 的扩展点。嵌入 Base 后只需覆写需要的 hook。
type Middleware interface {
	OnAgent(ctx context.Context, rc *RuntimeContext, in AgentInput, next func(context.Context, *RuntimeContext, AgentInput) error) error
	OnReasoning(ctx context.Context, rc *RuntimeContext, in ReasoningInput, next func(context.Context, *RuntimeContext, ReasoningInput) error) error
	OnToolCall(ctx context.Context, rc *RuntimeContext, in ToolCallInput, next func(context.Context, *RuntimeContext, ToolCallInput) error) error
	OnActing(ctx context.Context, rc *RuntimeContext, in ActingInput, next func(context.Context, *RuntimeContext, ActingInput) error) error
	OnModelCall(ctx context.Context, rc *RuntimeContext, in ModelCallInput, next func(context.Context, *RuntimeContext, ModelCallInput) error) error
	OnSystemPrompt(ctx context.Context, rc *RuntimeContext, current string) (string, error)
}

// Base 提供全部 hook 的空实现（透传 next / 原样返回提示词）。
// 自定义 middleware 嵌入 Base 覆写需要的 hook 即可。
type Base struct{}

func (Base) OnAgent(ctx context.Context, rc *RuntimeContext, in AgentInput, next func(context.Context, *RuntimeContext, AgentInput) error) error {
	return next(ctx, rc, in)
}
func (Base) OnReasoning(ctx context.Context, rc *RuntimeContext, in ReasoningInput, next func(context.Context, *RuntimeContext, ReasoningInput) error) error {
	return next(ctx, rc, in)
}
func (Base) OnToolCall(ctx context.Context, rc *RuntimeContext, in ToolCallInput, next func(context.Context, *RuntimeContext, ToolCallInput) error) error {
	return next(ctx, rc, in)
}
func (Base) OnActing(ctx context.Context, rc *RuntimeContext, in ActingInput, next func(context.Context, *RuntimeContext, ActingInput) error) error {
	return next(ctx, rc, in)
}
func (Base) OnModelCall(ctx context.Context, rc *RuntimeContext, in ModelCallInput, next func(context.Context, *RuntimeContext, ModelCallInput) error) error {
	return next(ctx, rc, in)
}
func (Base) OnSystemPrompt(_ context.Context, _ *RuntimeContext, current string) (string, error) { return current, nil }

// Chain 是 middleware 的有序集合，负责洋葱包裹与 transformer pipeline。
// 注册顺序：第一个为最外层（before 最先、after 最后）。
type Chain struct {
	middlewares []Middleware
}

// NewChain 创建链（可按外层→内层传入）。
func NewChain(mws ...Middleware) *Chain {
	return &Chain{middlewares: append([]Middleware{}, mws...)}
}

// Add 追加一个 middleware 到最内层（紧邻核心）。
func (c *Chain) Add(m Middleware) {
	c.middlewares = append(c.middlewares, m)
}

// Middlewares 返回当前注册列表（外部只读）。
func (c *Chain) Middlewares() []Middleware { return c.middlewares }

// WrapAgent 把核心函数包裹进 onAgent 洋葱链。
func (c *Chain) WrapAgent(core func(context.Context, *RuntimeContext, AgentInput) error) func(context.Context, *RuntimeContext, AgentInput) error {
	next := core
	for i := len(c.middlewares) - 1; i >= 0; i-- {
		m := c.middlewares[i]
		inner := next
		next = func(ctx context.Context, rc *RuntimeContext, in AgentInput) error { return m.OnAgent(ctx, rc, in, inner) }
	}
	return next
}

// WrapReasoning 包裹一次采样轮。
func (c *Chain) WrapReasoning(core func(context.Context, *RuntimeContext, ReasoningInput) error) func(context.Context, *RuntimeContext, ReasoningInput) error {
	next := core
	for i := len(c.middlewares) - 1; i >= 0; i-- {
		m := c.middlewares[i]
		inner := next
		next = func(ctx context.Context, rc *RuntimeContext, in ReasoningInput) error { return m.OnReasoning(ctx, rc, in, inner) }
	}
	return next
}

// WrapToolCall 包裹一批工具调用。
func (c *Chain) WrapToolCall(core func(context.Context, *RuntimeContext, ToolCallInput) error) func(context.Context, *RuntimeContext, ToolCallInput) error {
	next := core
	for i := len(c.middlewares) - 1; i >= 0; i-- {
		m := c.middlewares[i]
		inner := next
		next = func(ctx context.Context, rc *RuntimeContext, in ToolCallInput) error { return m.OnToolCall(ctx, rc, in, inner) }
	}
	return next
}

// WrapActing 包裹单个工具执行。
func (c *Chain) WrapActing(core func(context.Context, *RuntimeContext, ActingInput) error) func(context.Context, *RuntimeContext, ActingInput) error {
	next := core
	for i := len(c.middlewares) - 1; i >= 0; i-- {
		m := c.middlewares[i]
		inner := next
		next = func(ctx context.Context, rc *RuntimeContext, in ActingInput) error { return m.OnActing(ctx, rc, in, inner) }
	}
	return next
}

// WrapModelCall 包裹模型 API 调用。
func (c *Chain) WrapModelCall(core func(context.Context, *RuntimeContext, ModelCallInput) error) func(context.Context, *RuntimeContext, ModelCallInput) error {
	next := core
	for i := len(c.middlewares) - 1; i >= 0; i-- {
		m := c.middlewares[i]
		inner := next
		next = func(ctx context.Context, rc *RuntimeContext, in ModelCallInput) error { return m.OnModelCall(ctx, rc, in, inner) }
	}
	return next
}

// ComposeSystemPrompt 按注册顺序从左到右执行 onSystemPrompt（transformer pipeline）。
func (c *Chain) ComposeSystemPrompt(ctx context.Context, rc *RuntimeContext, base string) (string, error) {
	cur := base
	for _, m := range c.middlewares {
		var err error
		cur, err = m.OnSystemPrompt(ctx, rc, cur)
		if err != nil {
			return "", err
		}
	}
	return cur, nil
}
