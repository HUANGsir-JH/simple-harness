// Package events 定义 agent 回合级事件类型（agent 产生、session 落盘、UI
// 渲染共同消费的低层契约）。事件定义下沉至此，避免存储层 session 反向依赖
// 编排层 agent（A2，2026-08-11）。agent 从本包 import 并产生事件；session
// 依类型落盘 transcript；UI 桥接渲染。
package events

import "github.com/agent-project/harness/internal/messages"

// EventType 是 agent 回合级事件类型（渲染器/测试订阅）。
type EventType string

const (
	// EventTurnStart 标记一个回合（一次 agent.Run）的开始。
	EventTurnStart EventType = "turn_start"
	// EventThinkingDelta 是模型推理文本增量（thinking 展示）。
	EventThinkingDelta EventType = "thinking_delta"
	// EventThinkingDone 是一个 thinking 内容块完成（Text = 完整块文本；持久化用）。ADR-025。
	EventThinkingDone EventType = "thinking_done"
	// EventTextDelta 是助手回复文本增量。
	EventTextDelta EventType = "text_delta"
	// EventTextDone 是一个 text 内容块完成（Text = 完整块文本；持久化用）。ADR-025。
	EventTextDone EventType = "text_done"
	// EventToolCall 是模型发起的一个工具调用。
	EventToolCall EventType = "tool_call"
	// EventToolResult 是单个工具的执行结果。
	EventToolResult EventType = "tool_result"
	// EventTurnDone 标记一个回合的结束（★ 测试锚点）。
	EventTurnDone EventType = "turn_done"
	// EventUsage 是一次采样轮的 token 用量（agent 每轮采样后发出；renderer
	// 未知类型默认忽略，transcript 不落盘——用量属 AgentState 非消息）。
	EventUsage EventType = "usage"
	// EventError 是回合级错误。
	EventError EventType = "error"
)

// Event 是单个回合级事件。
type Event struct {
	Type       EventType
	MsgID      string // 事件所属消息 id（assistant 块事件；tool_result 为空）
	Text       string
	// Signature 是 thinking 块的数字签名（仅 EventThinkingDone 携带；provider
	// content_block_start 捕获，transcript thinking 行落盘，ADR-025 修订）。
	Signature  string
	ToolCall   *messages.ToolCall
	ToolResult *messages.ToolResult
	Usage      *messages.Usage // EventUsage 携带（nil 或 IsZero = 未捕获）
	Err        error
}

// OnEvent 是回合级事件回调（nil 允许，渲染器/测试订阅）。
type OnEvent func(Event)
