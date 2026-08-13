package impl

import (
	"context"

	"github.com/agent-project/harness/internal/middleware"
)

// DefaultBaseInstructions 是框架默认基础提示词（Build 装配标准链时注入）。
const DefaultBaseInstructions = "You are a helpful coding agent."

// BaseInstructionsMiddleware 是标准链的第一个 onSystemPrompt 中间件：
// 在调用方 per-call 贡献（rc.SystemPrompt）之前注入基础提示词。
// subagent 装配可换不同 Text（不同提示词 = 不同装配，build.go 既定方向）。
// 仅挂 onSystemPrompt，不参与洋葱 hook（顺序无副作用）。
type BaseInstructionsMiddleware struct {
	middleware.Base
	Text string
}

// OnSystemPrompt 前置基础提示词：当前内容为空则原样注入，非空则拼接在
// 调用方贡献之前（基础提示词恒在最前）。
func (m BaseInstructionsMiddleware) OnSystemPrompt(_ context.Context, _ *middleware.RuntimeContext, current string) (string, error) {
	if m.Text == "" {
		return current, nil
	}
	if current == "" {
		return m.Text, nil
	}
	return m.Text + "\n\n" + current, nil
}
