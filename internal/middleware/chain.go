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

// --- hook 签名类型别名 -----------------------------------------------------
//
// 5 个 onion hook 的签名长得完全一样：context.Context + *RuntimeContext +
// 各自的 Input，返回 error。直接写出来会让 Middleware 接口和 Wrap* 里
// 出现十几行长签名，阅读时眼神全糊在类型上。这里用别名收窄：
//
//	type AgentHandler = func(context.Context, *RuntimeContext, AgentInput) error
//
// 注意用的是 =（alias，别名）而不是普通类型定义（type X func(...)）：
// alias 与原类型**完全等价、可互换**，因此——
//   - 已有实现（Base 的方法、测试里 recordingMiddleware 覆写的 OnAgent 等）
//     仍用原签名书写，无需任何改动即可编译；
//   - 函数字面量可直接赋值/传入别名类型参数。

// AgentHandler 是 onAgent hook 的签名（包裹一整个 agent.Run）。
type AgentHandler = func(context.Context, *RuntimeContext, AgentInput) error

// ReasoningHandler 是 onReasoning hook 的签名（包裹一次采样轮）。
type ReasoningHandler = func(context.Context, *RuntimeContext, ReasoningInput) error

// ToolCallHandler 是 onToolCall hook 的签名（包裹一批工具调用）。
type ToolCallHandler = func(context.Context, *RuntimeContext, ToolCallInput) error

// ActingHandler 是 onActing hook 的签名（包裹单个工具执行）。
type ActingHandler = func(context.Context, *RuntimeContext, ActingInput) error

// ModelCallHandler 是 onModelCall hook 的签名（包裹一次模型 API 调用）。
type ModelCallHandler = func(context.Context, *RuntimeContext, ModelCallInput) error

// Middleware 是 agent 的扩展点。嵌入 Base 后只需覆写需要的 hook。
type Middleware interface {
	OnAgent(ctx context.Context, rc *RuntimeContext, in AgentInput, next AgentHandler) error
	OnReasoning(ctx context.Context, rc *RuntimeContext, in ReasoningInput, next ReasoningHandler) error
	OnToolCall(ctx context.Context, rc *RuntimeContext, in ToolCallInput, next ToolCallHandler) error
	OnActing(ctx context.Context, rc *RuntimeContext, in ActingInput, next ActingHandler) error
	OnModelCall(ctx context.Context, rc *RuntimeContext, in ModelCallInput, next ModelCallHandler) error
	OnSystemPrompt(ctx context.Context, rc *RuntimeContext, current string) (string, error)
}

// Base 提供全部 hook 的空实现（透传 next / 原样返回提示词）。
// 自定义 middleware 嵌入 Base 覆写需要的 hook 即可。
type Base struct{}

func (Base) OnAgent(ctx context.Context, rc *RuntimeContext, in AgentInput, next AgentHandler) error {
	return next(ctx, rc, in)
}
func (Base) OnReasoning(ctx context.Context, rc *RuntimeContext, in ReasoningInput, next ReasoningHandler) error {
	return next(ctx, rc, in)
}
func (Base) OnToolCall(ctx context.Context, rc *RuntimeContext, in ToolCallInput, next ToolCallHandler) error {
	return next(ctx, rc, in)
}
func (Base) OnActing(ctx context.Context, rc *RuntimeContext, in ActingInput, next ActingHandler) error {
	return next(ctx, rc, in)
}
func (Base) OnModelCall(ctx context.Context, rc *RuntimeContext, in ModelCallInput, next ModelCallHandler) error {
	return next(ctx, rc, in)
}
func (Base) OnSystemPrompt(_ context.Context, _ *RuntimeContext, current string) (string, error) {
	return current, nil
}

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
//
// ## 组装（调用 WrapAgent 时）与执行（调用返回的函数时）是两回事
//
// 本函数**不做任何执行**，它做的是"套娃组装"：把 core 一层层包起来，
// 返回给调用方一个"最外层入口"。真正的执行发生在调用方
// `wrapped(ctx, rc, in)` 那一刻——此时才一层层往里剥：
//
//	外层.before → 次层.before → ... → core → ... → 次层.after → 外层.after
//
// 假设注册 [m1, m2]（m1 先 Add），展开等价于：
//
//	return func(ctx, rc, in) error { return m1.OnAgent(ctx, rc, in, func(ctx, rc, in) error {
//	    return m2.OnAgent(ctx, rc, in, core)  // core 就是最里层
//	}) }
//
// ## 为什么倒序循环（len-1 → 0）
//
// 注册顺序是"外层在前"：m1 应该包住 m2。循环从数组尾部开始，把靠后的
// 中间件先包进最内层，最后轮到数组头的 m1 包在最外层。这样先 Add 的
// 中间件 before 最先执行、after 最后执行。
//
// ## 为什么必须 inner := next 快照
//
// Go 闭包捕获的是**变量**而不是"变量当时的取值"。如果不快照，三个闭包
// 都引用同一个 next 变量，看到的会是循环结束后的最终值，链路全乱。
// 每轮 `inner := next` 给这一轮拍一张快照，让闭包记住"我这一轮的下一层"。
// 同理 `m := c.middlewares[i]` 也要先取出来。
//
// ## 空链是零开销透传
//
// 没注册任何中间件时，循环体不执行，直接 return core。所以调用方可以
// 无条件 Wrap*，有没有中间件都安全。
func (c *Chain) WrapAgent(core AgentHandler) AgentHandler {
	next := core
	for i := len(c.middlewares) - 1; i >= 0; i-- {
		m := c.middlewares[i]
		inner := next
		next = func(ctx context.Context, rc *RuntimeContext, in AgentInput) error {
			return m.OnAgent(ctx, rc, in, inner)
		}
	}
	return next
}

// WrapReasoning 包裹一次采样轮。组装逻辑与 WrapAgent 完全相同，只换了
// hook（OnReasoning）和 Input 粒度（一次采样轮）。
func (c *Chain) WrapReasoning(core ReasoningHandler) ReasoningHandler {
	next := core
	for i := len(c.middlewares) - 1; i >= 0; i-- {
		m := c.middlewares[i]
		inner := next
		next = func(ctx context.Context, rc *RuntimeContext, in ReasoningInput) error {
			return m.OnReasoning(ctx, rc, in, inner)
		}
	}
	return next
}

// WrapToolCall 包裹一批工具调用。组装逻辑与 WrapAgent 相同，粒度是一批
// 工具调用（发起 → 逐个执行 → 结果回填）。
func (c *Chain) WrapToolCall(core ToolCallHandler) ToolCallHandler {
	next := core
	for i := len(c.middlewares) - 1; i >= 0; i-- {
		m := c.middlewares[i]
		inner := next
		next = func(ctx context.Context, rc *RuntimeContext, in ToolCallInput) error {
			return m.OnToolCall(ctx, rc, in, inner)
		}
	}
	return next
}

// WrapActing 包裹单个工具执行。组装逻辑与 WrapAgent 相同，粒度是一个
// 工具调用（阶段三权限审批挂载点：before = 审批）。
func (c *Chain) WrapActing(core ActingHandler) ActingHandler {
	next := core
	for i := len(c.middlewares) - 1; i >= 0; i-- {
		m := c.middlewares[i]
		inner := next
		next = func(ctx context.Context, rc *RuntimeContext, in ActingInput) error {
			return m.OnActing(ctx, rc, in, inner)
		}
	}
	return next
}

// WrapModelCall 包裹模型 API 调用。组装逻辑与 WrapAgent 相同，是最内层。
func (c *Chain) WrapModelCall(core ModelCallHandler) ModelCallHandler {
	next := core
	for i := len(c.middlewares) - 1; i >= 0; i-- {
		m := c.middlewares[i]
		inner := next
		next = func(ctx context.Context, rc *RuntimeContext, in ModelCallInput) error {
			return m.OnModelCall(ctx, rc, in, inner)
		}
	}
	return next
}

// ComposeSystemPrompt 按注册顺序从左到右执行 onSystemPrompt（transformer pipeline）。
// 与洋葱相反：这里**不套娃**，前一个的输出直接作为后一个的输入，正序流水线：
//
//	cur = m1(cur); cur = m2(cur); ...
//
// 起点 = rc.SystemPrompt（调用方 per-call 贡献，可为空；基础提示词由链首
// BaseInstructionsMiddleware 注入，agent 不携带任何提示词文本）；注册在前的
// 中间件先生效。
func (c *Chain) ComposeSystemPrompt(ctx context.Context, rc *RuntimeContext) (string, error) {
	cur := ""
	if rc != nil {
		cur = rc.SystemPrompt
	}
	for _, m := range c.middlewares {
		var err error
		cur, err = m.OnSystemPrompt(ctx, rc, cur)
		if err != nil {
			return "", err
		}
	}
	return cur, nil
}
