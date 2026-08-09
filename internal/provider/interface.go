// Package provider 定义 LLM 客户端抽象与 anthropic wire 适配。
// 依据 ADR-022，仅保留 anthropic Messages 一个 wire（2026-08-07 移除 openai wire）；
// 多后端 = 多 anthropic 兼容端点（配置结构体 + 单一 HTTP 客户端），DeepSeek 等
// 兼容端点只需 base_url 覆盖。
package provider

import (
	"context"
	"encoding/json"

	"github.com/agent-project/harness/internal/messages"
)

// DefaultAPIKeyEnv 是 API key 的惯例环境变量名（未配置 env_key 时的回退）。
const DefaultAPIKeyEnv = "ANTHROPIC_API_KEY"

// Config 是面向用户的完整配置（YAML），支持多 provider（多 anthropic 兼容端点）。
// 结构：default_provider 指定默认供应商，providers 按名称分组定义。
type Config struct {
	// DefaultProvider 是默认使用的 provider 名；未指定时取 providers 中
	// 排序后的第一个。
	DefaultProvider string `yaml:"default_provider,omitempty"`
	// Providers 是全部自定义供应商，key 为供应商名。
	Providers map[string]ProviderConfig `yaml:"providers"`
	// Approval 是工具审批配置（阶段三权限，ADR-029）；nil = 默认模式。
	Approval *ApprovalConfig `yaml:"approval,omitempty"`
}

// ApprovalConfig 是工具审批配置。
type ApprovalConfig struct {
	// Mode 是默认审批模式：readonly | acceptedit | bypass（未配置回退
	// acceptedit）。会话级可经 AgentState.Permission.Mode 覆盖（resume 恢复）。
	Mode string `yaml:"mode,omitempty"`
}

// ProviderConfig 描述一个自定义供应商（连接 + 鉴权 + 其下模型列表）。
type ProviderConfig struct {
	// BaseURL 覆盖 SDK 默认端点；为空时使用官方默认。
	BaseURL string `yaml:"base_url,omitempty"`
	// EnvKey 是存放 API key 的环境变量名；与 APIKey 二选一。
	EnvKey string `yaml:"env_key,omitempty"`
	// APIKey 是直接写在配置中的 API key（例如由 .env 转换而来）；
	// 优先级高于 EnvKey。为空时从 EnvKey 环境变量读取。
	APIKey string `yaml:"api_key,omitempty"`
	// Models 是该供应商下的模型定义，key 为模型 ID。
	Models map[string]Model `yaml:"models"`
}

// Model 是单个模型定义。
type Model struct {
	// ContextWindow 是该模型的上下文窗口（token 数）；
	// 0 表示使用 DefaultContextWindow。
	ContextWindow int `yaml:"context_window,omitempty"`
	// Thinking 是该模型的 thinking（推理模式）配置；
	// 未配置时默认启用 thinking、档位 high（见 DefaultThinkingEffort）。
	Thinking *Thinking `yaml:"thinking,omitempty"`
}

// Thinking 是模型级 thinking（推理模式）配置。传递按 anthropic Messages
// SDK 标准参数（thinking + output_config.effort），不对具体后端特化。
type Thinking struct {
	// Enabled 是否启用 thinking；nil（未配置）表示启用。
	Enabled *bool `yaml:"enabled,omitempty"`
	// Efforts 是模型支持的推理档位集（EffortLow / EffortHigh / EffortMax），
	// 覆盖默认档位集 DefaultEfforts；未配置回退默认。
	// 运行时 --effort 只能在 Efforts 内选择。
	Efforts []string `yaml:"efforts,omitempty"`
}

// thinking 推理档位（通用语义，非某后端特化）。
const (
	EffortLow  = "low"
	EffortHigh = "high"
	EffortMax  = "max"
)

// DefaultThinkingEffort 是未配置 thinking.effort 时的默认档位。
const DefaultThinkingEffort = EffortHigh

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
	// nil / 空 = 使用 client 配置默认（来自 Resolved）。
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
