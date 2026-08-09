package provider

import (
	"testing"

	"github.com/agent-project/harness/internal/config"
)

// TestFactoryNilResolved 验证 nil 配置报错。
func TestFactoryNilResolved(t *testing.T) {
	if _, err := NewClient(nil); err == nil {
		t.Fatal("expected error for nil resolved")
	}
}

// TestFactoryAnthropicClient 验证 anthropic 客户端构建成功（key 来自 ProviderConfig）。
// client 只承载连接不含模型（ADR-026），模型走 Request。
func TestFactoryAnthropicClient(t *testing.T) {
	c, err := NewClient(&config.ProviderConfig{Model: "claude-sonnet-5", APIKey: "k"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil client")
	}
}
