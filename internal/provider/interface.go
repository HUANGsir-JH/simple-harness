// Package provider 定义 LLM 客户端抽象与 anthropic wire 适配。
// 依据 ADR-022，仅保留 anthropic Messages 一个 wire（2026-08-07 移除 openai wire）；
// 多后端 = 多 anthropic 兼容端点（base_url 覆盖），DeepSeek 等兼容端点只需覆盖。
// 配置模型/加载/解析/校验已拆至 internal/config（2026-08-09）：本包回归纯 wire。
package provider

import (
	"context"
	"encoding/json"

	"github.com/agent-project/harness/internal/messages"
)

// ToolSpec 是统一的工具 schema。provider 适配层将其转换为
// anthropic SDK 的原生工具定义格式。
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"` // JSON Schema
}

// Request 是 agent 循环发起的一次采样请求。
type Request struct {
	Model           string
	Instructions    string // 系统/开发者提示（AGENTS.md 等将在阶段四注入）
	Messages        []*messages.Message
	Tools           []ToolSpec // 工具定义（阶段二起）
	MaxOutputTokens int        // 0 表示模型默认
	// ThinkingEnabled / ThinkingEffort 是 per-call 覆盖（ADR-026 运行时切换）：
	// nil / 空 = 使用 client 配置默认（来自 ProviderConfig）。
	ThinkingEnabled *bool
	ThinkingEffort  string
}

// Client 是暴露给 agent 循环的流式 LLM 客户端。
type Client interface {
	// Stream 发起一次采样请求并返回事件流。调用方用完后必须 Close。
	Stream(ctx context.Context, req Request) (EventStream, error)
}

// EventStream 产出采样事件。遵循 SDK 的
// Next/Current/Err/Close 约定；用 `for es.Next() { es.Current() }` 迭代。
type EventStream interface {
	Next() bool
	Current() Event
	Err() error
	Close() error
}

// EventType 区分不同的流事件类型。
type EventType string

const (
	// EventTextDelta 是助手回复的文本增量。
	EventTextDelta EventType = "text_delta"
	// EventTextDone 是一个 text 内容块完成（Event.Text = 完整块文本；流式
	// delta 之外的块级信号，供持久化等订阅）。ADR-025。
	EventTextDone EventType = "text_done"
	// EventThinkingDelta 是模型的推理文本增量（thinking；阶段二透出供展示）。
	EventThinkingDelta EventType = "thinking_delta"
	// EventThinkingDone 是一个 thinking 内容块完成（Event.Text = 完整块文本）。ADR-025。
	EventThinkingDone EventType = "thinking_done"
	// EventToolCall 是模型发起的、已完成的函数调用请求（阶段二起）。
	EventToolCall EventType = "tool_call"
	// EventDone 标记本次采样回合的结束。
	EventDone EventType = "done"
	// EventError 是流级错误（SDK 重试耗尽后）。
	EventError EventType = "error"
)

// Event 是单个流事件。
type Event struct {
	Type     EventType
	Text     string
	ToolCall *messages.ToolCall
	Error    error
}
