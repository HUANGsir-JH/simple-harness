package provider

import (
	"testing"
)

func TestDefaultEnvKey(t *testing.T) {
	if got := DefaultEnvKey(WireOpenAI); got != "OPENAI_API_KEY" {
		t.Errorf("openai: got %q", got)
	}
	if got := DefaultEnvKey(WireAnthropic); got != "ANTHROPIC_API_KEY" {
		t.Errorf("anthropic: got %q", got)
	}
}

// TestFactoryUnknownProvider 验证未知 wire api 被拒绝。
func TestFactoryUnknownProvider(t *testing.T) {
	if _, err := NewClient(&Resolved{WireAPI: "gemini", Model: "x"}); err == nil {
		t.Fatal("expected error for unknown wire api")
	}
}

// TestFactoryNilResolved 验证 nil 配置报错。
func TestFactoryNilResolved(t *testing.T) {
	if _, err := NewClient(nil); err == nil {
		t.Fatal("expected error for nil resolved")
	}
}

// TestFactoryWireAPIDefault 验证 OpenAI 客户端构建成功（key 来自 Resolved）。
func TestFactoryWireAPIDefault(t *testing.T) {
	c, err := NewClient(&Resolved{WireAPI: WireOpenAI, Model: "gpt-4o", APIKey: "k"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil client")
	}
	if c.(*openAIClient).Model() != "gpt-4o" {
		t.Errorf("model: got %q", c.(*openAIClient).Model())
	}
}
