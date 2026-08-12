// Package messages 定义贯穿 harness 核心层的统一消息模型。
// provider 适配层负责将该模型与其原生 wire 格式互转；
// 会话 JSONL 文件直接序列化该模型（切换后端无需迁移）。
package messages

import (
	"encoding/json"
	"fmt"
	"time"
)

// Role 表示一条消息的发言人。
type Role string

const (
	// RoleUser 表示用户消息。
	RoleUser Role = "user"
	// RoleAssistant 表示模型生成的消息，可携带 ToolCalls。
	RoleAssistant Role = "assistant"
	// RoleDeveloper 表示系统/开发者指令（系统提示、AGENTS.md）。
	RoleDeveloper Role = "developer"
	// RoleTool 表示工具执行结果，通过 ToolCallID 关联到对应的 ToolCall。
	RoleTool Role = "tool"
)

// Message 是统一消息类型，也是核心层唯一操作的消息类型；
// provider 适配层负责与各后端原生格式互转。
type Message struct {
	ID       string `json:"id,omitempty"`
	Role     Role   `json:"role"`
	Content  string `json:"content,omitempty"`
	Thinking string `json:"thinking,omitempty"` // assistant 推理文本
	// ThinkingSignature 是 thinking 块的数字签名（ADR-025 修订完整回传）：
	// 非空时 provider 重放 thinking 块（ThinkingBlockParam 首块）；空则只存不重放
	//（严格端点兼容；DeepSeek 兼容端点恒返回）。JSON 序列化进 transcript，resume 恢复。
	ThinkingSignature string            `json:"thinking_signature,omitempty"`
	ToolCalls         []ToolCall        `json:"tool_calls,omitempty"`   // 助手消息携带这些
	ToolCallID        string            `json:"tool_call_id,omitempty"` // tool results reference a call
	ToolResults       []ToolResultBlock `json:"tool_results,omitempty"` // tool result 消息携带（多块合并，满足 anthropic 紧邻要求）
	IsError           bool              `json:"is_error,omitempty"`     // 单块 tool result 标记执行失败
}

// ToolResultBlock 是一次工具执行的单块结果（可多条合并进一条 tool result 消息）。
type ToolResultBlock struct {
	ToolCallID string `json:"tool_call_id"`
	Success    bool   `json:"success"`
	Content    string `json:"content"`
}

// ToolCall 是模型请求的一次函数调用。
type ToolCall struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	Args   json.RawMessage `json:"args"`             // 来自模型的 JSON 参数
	Result *ToolResult     `json:"result,omitempty"` // populated after execution
}

// ToolResult 是一次 ToolCall 的执行结果。
type ToolResult struct {
	Success bool   `json:"success"`
	Content string `json:"content"`
}

// Usage 是单次采样请求的 token 用量（anthropic wire：message_start 的
// input/cache + 最后一个 message_delta 的累计 output_tokens）。放在统一消息
// 模型层，provider（wire 用量）、events（回合级事件）、agentstate（会话累计）
// 三方复用同一类型，避免 provider 类型泄漏到存储层。
type Usage struct {
	InputTokens              int64 `json:"input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens,omitempty"`
	OutputTokens             int64 `json:"output_tokens"`
}

// IsZero 报告用量是否全为零（未捕获到 usage 时判断展示）。
func (u *Usage) IsZero() bool {
	return u == nil || (u.InputTokens == 0 && u.CacheReadInputTokens == 0 &&
		u.CacheCreationInputTokens == 0 && u.OutputTokens == 0)
}

// Conversation 是消息序列，也是会话 JSONL 文件的存储单元。
type Conversation struct {
	ID        string     `json:"id"`
	CreatedAt string     `json:"created_at"`
	Messages  []*Message `json:"messages"`
}

// NewConversation 创建一个带新 ID 与当前 UTC 时间戳的 Conversation。
func NewConversation() *Conversation {
	return &Conversation{
		ID:        fmt.Sprintf("conv_%d", time.Now().UnixNano()),
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Messages:  []*Message{},
	}
}

// Add 追加一条消息并返回它。
func (t *Conversation) Add(m *Message) *Message {
	t.Messages = append(t.Messages, m)
	return m
}

// NewUserMessage 构造一条带生成 ID 的用户消息。
func NewUserMessage(content string) *Message {
	return &Message{
		ID:      fmt.Sprintf("msg_%d", time.Now().UnixNano()),
		Role:    RoleUser,
		Content: content,
	}
}

// NewAssistantMessage 构造一条助手消息。
func NewAssistantMessage(content string) *Message {
	return &Message{
		ID:      fmt.Sprintf("msg_%d", time.Now().UnixNano()),
		Role:    RoleAssistant,
		Content: content,
	}
}

// NewToolResultMessage 构造一条引用单个调用的工具结果消息（兼容单块场景）。
// success=false 时置 IsError，provider 适配层转各后端 is_error 标记。
func NewToolResultMessage(callID string, success bool, content string) *Message {
	return &Message{
		Role:       RoleTool,
		ToolCallID: callID,
		Content:    content,
		IsError:    !success,
	}
}

// NewToolResultsMessage 构造一条携带多块工具结果的消息（合并一批调用结果，
// 满足 anthropic "tool_use 后下一条消息含全部 tool_result" 的要求）。
func NewToolResultsMessage(results []ToolResultBlock) *Message {
	return &Message{Role: RoleTool, ToolResults: results}
}

// AppendToolResult 将结果记录到匹配的 ToolCall（按 ID）上。
// 若无匹配的调用则返回 false。
func (m *Message) AppendToolResult(callID string, r *ToolResult) bool {
	for i := range m.ToolCalls {
		if m.ToolCalls[i].ID == callID {
			m.ToolCalls[i].Result = r
			return true
		}
	}
	return false
}
