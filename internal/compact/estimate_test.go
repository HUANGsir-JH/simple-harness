package compact

import (
	"encoding/json"
	"testing"

	"github.com/agent-project/harness/internal/messages"
)

// TestEstimateTokens 验证估算**镜像实际发送**：计入 thinking 文本/签名、工具参数、
// 工具结果（ADR-025 修订完整回传后 thinking 随请求重放；ADR-037 估算兜底）。
func TestEstimateTokens(t *testing.T) {
	msgs := []*messages.Message{
		{Role: messages.RoleUser, Content: "hello world"}, // 11 bytes
		// assistant：content 6 + thinking 5 + signature 3 = 14
		{Role: messages.RoleAssistant, Content: "answer", Thinking: "think", ThinkingSignature: "sig"},
		// tool：name 9 + args 4 = 13
		{Role: messages.RoleAssistant, ToolCalls: []messages.ToolCall{{Name: "read_file", Args: json.RawMessage(`{"x":1}`)}}},
		// tool result：content 6
		{Role: messages.RoleTool, ToolResults: []messages.ToolResultBlock{{Content: "result"}}},
	}
	// 11 + 14 + 13 + 6 = 44 bytes → /4 = 11 tokens。
	if got := EstimateTokens(msgs); got != 11 {
		t.Errorf("EstimateTokens = %d, want 11", got)
	}
}

// TestEstimateTokensNoThinking 验证无 thinking 时不受影响（普通会话估算正常）。
func TestEstimateTokensNoThinking(t *testing.T) {
	msgs := []*messages.Message{
		{Role: messages.RoleUser, Content: "hello"}, // 5
		{Role: messages.RoleAssistant, Content: "hi"}, // 2
	}
	if got := EstimateTokens(msgs); got != 1 { // 7/4 = 1
		t.Errorf("EstimateTokens = %d, want 1", got)
	}
}

// TestEstimateTokensNilSafe 验证 nil 消息安全（不应 panic）。
func TestEstimateTokensNilSafe(t *testing.T) {
	msgs := []*messages.Message{nil, {Role: messages.RoleUser, Content: "x"}}
	if got := EstimateTokens(msgs); got != 0 { // 1/4 = 0
		t.Errorf("EstimateTokens = %d, want 0", got)
	}
}
