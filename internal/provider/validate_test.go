package provider

import (
	"strings"
	"testing"
)

// TestValidateBadThinkingEffort 验证 thinking.efforts 中非法档位报错。
func TestValidateBadThinkingEffort(t *testing.T) {
	cfg := Config{
		Providers: map[string]ProviderConfig{
			"p": {APIKey: "k", Models: map[string]Model{
				"m": {Thinking: &Thinking{Efforts: []string{"turbo"}}},
			}},
		},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "thinking.efforts") {
		t.Fatalf("expected thinking.efforts error, got %v", err)
	}
}

// TestValidateOK 验证合法配置通过。
func TestValidateOK(t *testing.T) {
	cfg := testConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// TestValidateNoProviders 验证空 providers 报错。
func TestValidateNoProviders(t *testing.T) {
	if err := (Config{}).Validate(); err == nil {
		t.Fatal("expected error when no providers")
	}
}

// TestValidateDefaultNotFound 验证 default_provider 不存在报错。
func TestValidateDefaultNotFound(t *testing.T) {
	cfg := testConfig()
	cfg.DefaultProvider = "nope"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error when default_provider not found")
	}
}

// TestValidateNoModels 验证 provider 无 models 报错。
func TestValidateNoModels(t *testing.T) {
	cfg := Config{
		Providers: map[string]ProviderConfig{
			"p": {APIKey: "k"}, // 无 models
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error when provider has no models")
	}
}

// TestValidateBadWireAPI 验证非法 wire_api 报错。
func TestValidateBadWireAPI(t *testing.T) {
	cfg := Config{
		Providers: map[string]ProviderConfig{
			"p": {
				WireAPI: "gemini",
				APIKey:  "k",
				Models:  map[string]Model{"m": {}},
			},
		},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "wire_api") {
		t.Fatalf("expected wire_api error, got %v", err)
	}
}

// TestValidateNegativeContextWindow 验证负 context_window 报错。
func TestValidateNegativeContextWindow(t *testing.T) {
	cfg := Config{
		Providers: map[string]ProviderConfig{
			"p": {
				APIKey: "k",
				Models: map[string]Model{"m": {ContextWindow: -1}},
			},
		},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "context_window") {
		t.Fatalf("expected context_window error, got %v", err)
	}
}

// TestValidateMissingKey 验证无 key 来源报错。
func TestValidateMissingKey(t *testing.T) {
	cfg := Config{
		Providers: map[string]ProviderConfig{
			"p": {Models: map[string]Model{"m": {}}},
		},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("expected API key error, got %v", err)
	}
}

// TestValidateMultipleErrors 验证一次返回全部错误（多行）。
func TestValidateMultipleErrors(t *testing.T) {
	cfg := Config{
		Providers: map[string]ProviderConfig{
			"p": {
				WireAPI: "gemini",
				Models:  map[string]Model{"m": {ContextWindow: -5}},
			},
			// 无 key、无 models
			"q": {},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected errors")
	}
	// 应有 4 条错误：p 的 wire_api、p 的 context_window、p 缺 key、q 缺 models + 缺 key（2条）
	if n := strings.Count(err.Error(), "\n  - "); n != 5 {
		t.Errorf("expected 5 errors, got %d:\n%s", n, err)
	}
}
