package provider

import (
	"testing"
)

// TestDefaultAPIKeyEnv 验证惯例环境变量名。
func TestDefaultAPIKeyEnv(t *testing.T) {
	if got := DefaultAPIKeyEnv; got != "ANTHROPIC_API_KEY" {
		t.Errorf("DefaultAPIKeyEnv: got %q want ANTHROPIC_API_KEY", got)
	}
}

// TestFactoryNilResolved 验证 nil 配置报错。
func TestFactoryNilResolved(t *testing.T) {
	if _, err := NewClient(nil); err == nil {
		t.Fatal("expected error for nil resolved")
	}
}

// TestFactoryAnthropicClient 验证 anthropic 客户端构建成功（key 来自 Resolved）。
func TestFactoryAnthropicClient(t *testing.T) {
	c, err := NewClient(&Resolved{Model: "claude-sonnet-5", APIKey: "k"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil client")
	}
	if c.(*anthropicClient).Model() != "claude-sonnet-5" {
		t.Errorf("model: got %q", c.(*anthropicClient).Model())
	}
}
