package provider

import (
	"encoding/json"
	"testing"

	"github.com/agent-project/harness/internal/messages"
)

// TestToAnthropicMessagesStripsThinkingWithoutSignature 验证无签名时 thinking 不
// 重放（ADR-025 修订后仍保留）：assistant 的 Thinking 字段存审计，重放历史采样时
// 剥离——只有签名非空（ThinkingSignature）才重放 thinking 块。此约束由
// "仅读 ThinkingSignature"实现，测试锁定防重构失效。
func TestToAnthropicMessagesStripsThinkingWithoutSignature(t *testing.T) {
	// 带 thinking 的 assistant（最常见：推理 + 回答），无签名 → 只重放 text。
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
	if len(blocks) != 1 || blocks[0].OfText == nil {
		t.Fatalf("assistant 应只有一个 text block，got %+v", blocks)
	}
	if got := blocks[0].OfText.Text; got != "final answer" {
		t.Errorf("text block = %q, want final answer", got)
	}
	if blocks[0].OfThinking != nil {
		t.Error("无签名时 thinking block 不应出现在重放请求")
	}

	// thinking + 工具调用（同轮推理后调用工具），无签名 → 只保留 tool_use。
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
		t.Error("无签名时 thinking block 不应出现在重放请求")
	}
}

// TestToAnthropicMessagesReplaysThinkingWithSignature 验证签名非空时重放 thinking
// 块，且放在首块（thinking 在 text/tool_use 前，anthropic 块顺序要求，ADR-025 修订）。
func TestToAnthropicMessagesReplaysThinkingWithSignature(t *testing.T) {
	args := json.RawMessage(`{"path":"a.go"}`)
	out := toAnthropicMessages([]*messages.Message{{
		ID:                "m1",
		Role:              messages.RoleAssistant,
		Content:           "final answer",
		Thinking:          "secret reasoning",
		ThinkingSignature: "sig_abc",
		ToolCalls:         []messages.ToolCall{{ID: "c1", Name: "read_file", Args: args}},
	}})
	if len(out) != 1 {
		t.Fatalf("消息数=%d, want 1", len(out))
	}
	blocks := out[0].Content
	if len(blocks) != 3 {
		t.Fatalf("应 3 个 block（thinking/text/tool_use），got %d", len(blocks))
	}
	if blocks[0].OfThinking == nil {
		t.Fatalf("首块应为 thinking，got %+v", blocks[0])
	}
	if blocks[0].OfThinking.Signature != "sig_abc" || blocks[0].OfThinking.Thinking != "secret reasoning" {
		t.Errorf("thinking block = %+v", blocks[0].OfThinking)
	}
	if blocks[1].OfText == nil || blocks[1].OfText.Text != "final answer" {
		t.Errorf("第二块应为 text，got %+v", blocks[1])
	}
	if blocks[2].OfToolUse == nil || blocks[2].OfToolUse.ID != "c1" {
		t.Errorf("第三块应为 tool_use，got %+v", blocks[2])
	}
}

// TestToAnthropicMessagesReplaysThinkingOnly 验证 thinking-only 的 assistant 带签名
// 时不再跳过：重放为单一 thinking 块（ADR-025 修订完整回传；无签名的跳过 case
// 见 stream_test 的 TestToAnthropicMessagesSkipsThinkingOnly）。
func TestToAnthropicMessagesReplaysThinkingOnly(t *testing.T) {
	out := toAnthropicMessages([]*messages.Message{
		NewTestUserMsg("hi"),
		{Role: messages.RoleAssistant, Thinking: "deep", ThinkingSignature: "sig_deep", Content: ""},
		NewTestUserMsg("继续"),
	})
	if len(out) != 3 {
		t.Fatalf("带签名的 thinking-only assistant 不应跳过，got %d", len(out))
	}
	blocks := out[1].Content
	if len(blocks) != 1 || blocks[0].OfThinking == nil {
		t.Fatalf("assistant 应为单一 thinking block，got %+v", blocks)
	}
	if blocks[0].OfThinking.Signature != "sig_deep" || blocks[0].OfThinking.Thinking != "deep" {
		t.Errorf("thinking block = %+v", blocks[0].OfThinking)
	}
}
