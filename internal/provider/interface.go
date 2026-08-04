// Package provider defines the multi-backend LLM client abstraction. Per
// ADR-001 the multi-backend support is a config struct + a single HTTP client
// per wire API (openai Responses / anthropic Messages); there is no separate
// implementation per vendor for compatible endpoints.
package provider

import (
	"context"
	"encoding/json"

	"github.com/agent-project/harness/internal/messages"
)

// WireAPI identifies the request/response wire protocol of a provider.
type WireAPI string

const (
	// WireOpenAI is the OpenAI Responses API (also used by OpenAI-compatible
	// endpoints such as Ollama, LM Studio).
	WireOpenAI WireAPI = "openai"
	// WireAnthropic is the Anthropic Messages API.
	WireAnthropic WireAPI = "anthropic"
)

// Provider describes a configured model backend. Implementations are thin:
// wire API, model, base URL override, context window.
type Provider interface {
	// WireAPI returns the protocol used to talk to this backend.
	WireAPI() WireAPI
	// Model returns the model ID used for sampling.
	Model() string
	// BaseURL overrides the SDK default endpoint; "" means default.
	BaseURL() string
	// ContextWindow returns the model's context window in tokens.
	ContextWindow() int
}

// Config is the user-facing provider configuration (YAML, env-overridable).
// API keys are never stored here; they come from the environment via EnvKey.
type Config struct {
	Provider string `yaml:"provider"` // "openai" | "anthropic"
	Model    string `yaml:"model"`
	BaseURL  string `yaml:"base_url,omitempty"` // optional endpoint override
	EnvKey   string `yaml:"env_key,omitempty"`  // env var holding the API key; inferred per provider if empty
}

// DefaultEnvKey returns the conventional API key env var for a wire API.
func DefaultEnvKey(w WireAPI) string {
	if w == WireAnthropic {
		return "ANTHROPIC_API_KEY"
	}
	return "OPENAI_API_KEY"
}

// ToolSpec is the unified tool schema. Provider adapters convert it to their
// native tool definition format.
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"` // JSON Schema
}

// Request is one sampling request issued by the agent loop.
type Request struct {
	Model           string
	Instructions    string // system/developer prompt (AGENTS.md etc. injected in phase 4)
	Messages        []*messages.Message
	Tools           []ToolSpec // tool definitions (phase 2+)
	MaxOutputTokens int        // 0 = model default
}

// Client is the streaming LLM client exposed to the agent loop.
type Client interface {
	// Stream starts a sampling request and returns an event stream. The
	// caller must Close the stream when done.
	Stream(ctx context.Context, req Request) (EventStream, error)
}

// EventStream yields sampling events. It follows the SDK convention of
// Next/Current/Err/Close; iterate with `for es.Next() { es.Current() }`.
type EventStream interface {
	Next() bool
	Current() Event
	Err() error
	Close() error
}

// EventType discriminates stream events.
type EventType string

const (
	// EventTextDelta is a text increment of the assistant response.
	EventTextDelta EventType = "text_delta"
	// EventToolCall is a completed function call request from the model
	// (phase 2+).
	EventToolCall EventType = "tool_call"
	// EventDone marks the end of this sampling turn.
	EventDone EventType = "done"
	// EventError is a stream-level error (after SDK retries were exhausted).
	EventError EventType = "error"
)

// Event is a single stream event.
type Event struct {
	Type     EventType
	Text     string
	ToolCall *messages.ToolCall
	Error    error
}
