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
	ID         string     `json:"id,omitempty"`
	Role       Role       `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`   // 助手消息携带这些
	ToolCallID string     `json:"tool_call_id,omitempty"` // tool results reference a call
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

// Thread 是消息序列，也是会话 JSONL 文件的存储单元。
type Thread struct {
	ID        string     `json:"id"`
	CreatedAt string     `json:"created_at"`
	Messages  []*Message `json:"messages"`
}

// NewThread 创建一个带新 ID 与当前 UTC 时间戳的 Thread。
func NewThread() *Thread {
	return &Thread{
		ID:        fmt.Sprintf("thr_%d", time.Now().UnixNano()),
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Messages:  []*Message{},
	}
}

// Add 追加一条消息并返回它。
func (t *Thread) Add(m *Message) *Message {
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

// NewToolResultMessage 构造一条引用指定调用的工具结果消息。
func NewToolResultMessage(callID string, success bool, content string) *Message {
	return &Message{
		Role:       RoleTool,
		ToolCallID: callID,
		Content:    content,
		ToolCalls:  nil,
	}
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
