package provider

import (
	"encoding/json"
	"testing"

	"github.com/agent-project/harness/internal/messages"
)

// TestToAnthropicMessagesStripsThinking 验证 thinking 不重放（C5，ADR-025）：
// assistant 消息的 Thinking 字段存审计，重放历史采样时剥离——转换输出里
// 不该出现 thinking 内容（thinking-only 跳过的 case 见 stream_test 的
// TestToAnthropicMessagesSkipsThinkingOnly）。此约束由"不读取字段"实现，
// 靠约定维持，测试锁定防重构失效。
func TestToAnthropicMessagesStripsThinking(t *testing.T) {
	// 带 thinking 的 assistant（最常见：推理 + 回答）。
	out := toAnthropicMessages([]*messages.Message{{
		ID:       "m1",
		Role:     messages.RoleAssistant,
		Content:  "final answer",
		Thinking: "secret internal reasoning",
	}})
	if len(out) != 1 {
		t.Fatalf("消息数=%d, want 1", len(out))
	}
	blocks := out[0].Content
	// 只有一个 text block，无 thinking 泄漏。
	if len(blocks) != 1 || blocks[0].OfText == nil {
		t.Fatalf("assistant 应只有一个 text block，got %+v", blocks)
	}
	if got := blocks[0].OfText.Text; got != "final answer" {
		t.Errorf("text block = %q, want final answer", got)
	}
	if blocks[0].OfThinking != nil {
		t.Error("thinking block 不应出现在重放请求")
	}

	// thinking + 工具调用（同轮推理后调用工具）：只保留 tool_use。
	args := json.RawMessage(`{"path":"a.go"}`)
	out = toAnthropicMessages([]*messages.Message{{
		ID:        "m2",
		Role:      messages.RoleAssistant,
		Content:   "",
		Thinking:  "need to read the file",
		ToolCalls: []messages.ToolCall{{ID: "c1", Name: "read_file", Args: args}},
	}})
	if len(out) != 1 {
		t.Fatalf("thinking+tool 消息数=%d, want 1", len(out))
	}
	blocks = out[0].Content
	if len(blocks) != 1 || blocks[0].OfToolUse == nil {
		t.Fatalf("应只有一个 tool_use block，got %+v", blocks)
	}
	if blocks[0].OfThinking != nil {
		t.Error("thinking block 不应出现在重放请求")
	}
}
