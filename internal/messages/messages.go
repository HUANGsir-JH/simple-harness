// Package messages defines the unified message model used throughout the
// harness core. Provider adapters convert between this model and their native
// wire formats; session JSONL files serialize this model directly (switching
// backends requires no migration).
package messages

import (
	"encoding/json"
	"fmt"
	"time"
)

// Role is the speaker of a message.
type Role string

const (
	// RoleUser is a user message.
	RoleUser Role = "user"
	// RoleAssistant is a model-generated message. May carry ToolCalls.
	RoleAssistant Role = "assistant"
	// RoleDeveloper is a system/developer instruction (system prompt, AGENTS.md).
	RoleDeveloper Role = "developer"
	// RoleTool is a tool result, associated with a ToolCall via ToolCallID.
	RoleTool Role = "tool"
)

// Message is the unified message type. It is the only message type the core
// layer operates on; provider adapters convert to/from native formats.
type Message struct {
	ID         string     `json:"id,omitempty"`
	Role       Role       `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`   // assistant messages carry these
	ToolCallID string     `json:"tool_call_id,omitempty"` // tool results reference a call
}

// ToolCall is a model-requested function invocation.
type ToolCall struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	Args   json.RawMessage `json:"args"`             // JSON arguments from the model
	Result *ToolResult     `json:"result,omitempty"` // populated after execution
}

// ToolResult is the outcome of executing a ToolCall.
type ToolResult struct {
	Success bool   `json:"success"`
	Content string `json:"content"`
}

// Thread is a message sequence; the storage unit of session JSONL files.
type Thread struct {
	ID        string     `json:"id"`
	CreatedAt string     `json:"created_at"`
	Messages  []*Message `json:"messages"`
}

// NewThread creates a thread with a fresh ID and the current UTC timestamp.
func NewThread() *Thread {
	return &Thread{
		ID:        fmt.Sprintf("thr_%d", time.Now().UnixNano()),
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Messages:  []*Message{},
	}
}

// Add appends a message and returns it.
func (t *Thread) Add(m *Message) *Message {
	t.Messages = append(t.Messages, m)
	return m
}

// NewUserMessage builds a user message with a generated ID.
func NewUserMessage(content string) *Message {
	return &Message{
		ID:      fmt.Sprintf("msg_%d", time.Now().UnixNano()),
		Role:    RoleUser,
		Content: content,
	}
}

// NewAssistantMessage builds an assistant message.
func NewAssistantMessage(content string) *Message {
	return &Message{
		ID:      fmt.Sprintf("msg_%d", time.Now().UnixNano()),
		Role:    RoleAssistant,
		Content: content,
	}
}

// NewToolResultMessage builds a tool result message referencing a call.
func NewToolResultMessage(callID string, success bool, content string) *Message {
	return &Message{
		Role:       RoleTool,
		ToolCallID: callID,
		Content:    content,
		ToolCalls:  nil,
	}
}

// AppendToolResult records the result on the matching ToolCall (by ID).
// Returns false if no matching call exists.
func (m *Message) AppendToolResult(callID string, r *ToolResult) bool {
	for i := range m.ToolCalls {
		if m.ToolCalls[i].ID == callID {
			m.ToolCalls[i].Result = r
			return true
		}
	}
	return false
}
