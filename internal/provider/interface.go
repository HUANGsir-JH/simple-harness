// Package provider 定义多后端 LLM 客户端抽象。
// 依据 ADR-001，多后端支持是一个配置结构体 + 每个 wire API 一个 HTTP 客户端
// （openai Responses / anthropic Messages）；兼容端点无需为每家厂商单独实现。
package provider

import (
	"context"
	"encoding/json"

	"github.com/agent-project/harness/internal/messages"
)

// WireAPI 标识 provider 使用的请求/响应 wire 协议。
type WireAPI string

const (
	// WireOpenAI 是 OpenAI Responses API（同样适用于 Ollama、LM Studio 等
	// OpenAI 兼容端点）。
	WireOpenAI WireAPI = "openai"
	// WireAnthropic 是 Anthropic Messages API。
	WireAnthropic WireAPI = "anthropic"
)

// Provider 描述一个已配置的模型后端。实现很薄：
// wire API、模型、base URL 覆盖、上下文窗口。
type Provider interface {
	// WireAPI 返回与该后端通信所用的协议。
	WireAPI() WireAPI
	// Model 返回采样所用的模型 ID。
	Model() string
	// BaseURL 覆盖 SDK 默认端点；空字符串表示默认。
	BaseURL() string
	// ContextWindow 返回模型的上下文窗口（token 数）。
	ContextWindow() int
}

// Config 是面向用户的 provider 配置（YAML，可被环境变量覆盖）。
// API key 可来自配置文件（api_key），也可来自环境变量
// （EnvKey / 该 wire API 的惯例环境变量名）。
type Config struct {
	Provider string `yaml:"provider"` // "openai" | "anthropic"
	Model    string `yaml:"model"`
	BaseURL  string `yaml:"base_url,omitempty"` // 可选端点覆盖
	EnvKey   string `yaml:"env_key,omitempty"`  // 存放 API key 的环境变量名；为空时按 provider 推断
	APIKey   string `yaml:"api_key,omitempty"`  // 直接写在配置中的 API key（例如由 .env 转换而来）
}

// DefaultEnvKey 返回某 wire API 的惯例 API key 环境变量名。
func DefaultEnvKey(w WireAPI) string {
	if w == WireAnthropic {
		return "ANTHROPIC_API_KEY"
	}
	return "OPENAI_API_KEY"
}

// ToolSpec 是统一的工具 schema。provider 适配层将其转换为
// 各后端的原生工具定义格式。
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
