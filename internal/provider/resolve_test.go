package provider

import (
	"slices"
	"testing"

	"gopkg.in/yaml.v3"
)

// yamlUnmarshalStrict 用 yaml.v3 解析配置（测试辅助）。
func yamlUnmarshalStrict(data []byte, v any) error {
	return yaml.Unmarshal(data, v)
}

// testConfig 构造一个两 provider 的测试配置。
func testConfig() Config {
	return Config{
		DefaultProvider: "deepseek",
		Providers: map[string]ProviderConfig{
			"deepseek": {
				WireAPI: WireOpenAI,
				BaseURL: "https://api.deepseek.com/",
				APIKey:  "sk-deepseek",
				Models: map[string]Model{
					"deepseek-v4-flash": {ContextWindow: 128000},
					"deepseek-v4":       {ContextWindow: 256000},
				},
			},
			"claude": {
				WireAPI: WireAnthropic,
				BaseURL: "https://api.anthropic.com/",
				EnvKey:  "ANTHROPIC_API_KEY",
				Models: map[string]Model{
					"claude-sonnet-5": {ContextWindow: 1000000},
				},
			},
		},
	}
}

// TestResolveDefaultProvider 验证 default_provider 生效。
func TestResolveDefaultProvider(t *testing.T) {
	r, err := Resolve(testConfig(), "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.ProviderID != "deepseek" {
		t.Errorf("provider: got %q", r.ProviderID)
	}
	if r.WireAPI != WireOpenAI || r.BaseURL != "https://api.deepseek.com/" || r.APIKey != "sk-deepseek" {
		t.Errorf("provider config mismatch: %+v", r)
	}
	// 未指定 --model → 取 models 排序第一个（deepseek-v4 < deepseek-v4-flash）
	if r.Model != "deepseek-v4" {
		t.Errorf("model: got %q want deepseek-v4", r.Model)
	}
	if r.ContextWindow != 256000 {
		t.Errorf("context window: got %d want 256000", r.ContextWindow)
	}
}

// TestResolveDefaultProviderFallback 验证未指定 default_provider 时取排序第一个。
func TestResolveDefaultProviderFallback(t *testing.T) {
	cfg := testConfig()
	cfg.DefaultProvider = ""
	// claude 用 EnvKey 读环境变量，需要先设置。
	t.Setenv("ANTHROPIC_API_KEY", "sk-claude")
	r, err := Resolve(cfg, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// providers 排序：claude < deepseek → claude
	if r.ProviderID != "claude" {
		t.Errorf("provider: got %q want claude", r.ProviderID)
	}
}

// TestResolveModelFlag 验证 --model 指定模型。
func TestResolveModelFlag(t *testing.T) {
	r, err := Resolve(testConfig(), "deepseek-v4-flash")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Model != "deepseek-v4-flash" {
		t.Errorf("model: got %q", r.Model)
	}
	if r.ContextWindow != 128000 {
		t.Errorf("context window: got %d want 128000", r.ContextWindow)
	}
}

// TestResolveModelNotFound 验证 --model 指定不存在的模型报错。
func TestResolveModelNotFound(t *testing.T) {
	if _, err := Resolve(testConfig(), "nope"); err == nil {
		t.Fatal("expected error for unknown model")
	}
}

// TestResolveContextWindowDefault 验证未配置 context_window 回退默认。
func TestResolveContextWindowDefault(t *testing.T) {
	cfg := Config{
		Providers: map[string]ProviderConfig{
			"p": {
				APIKey: "k",
				Models: map[string]Model{
					"m": {}, // 未配置 context_window
				},
			},
		},
	}
	r, err := Resolve(cfg, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.ContextWindow != DefaultContextWindow {
		t.Errorf("context window: got %d want %d", r.ContextWindow, DefaultContextWindow)
	}
}

// TestResolveAPIKeyEnvFallback 验证 APIKey 为空时从环境变量读取。
func TestResolveAPIKeyEnvFallback(t *testing.T) {
	t.Setenv("MY_TEST_KEY", "env-key")
	cfg := Config{
		Providers: map[string]ProviderConfig{
			"p": {
				EnvKey: "MY_TEST_KEY",
				Models: map[string]Model{"m": {}},
			},
		},
	}
	r, err := Resolve(cfg, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.APIKey != "env-key" {
		t.Errorf("api key: got %q want env-key", r.APIKey)
	}
}

// TestResolveMissingKey 验证无任何 key 来源时报错。
func TestResolveMissingKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	cfg := Config{
		Providers: map[string]ProviderConfig{
			"p": {Models: map[string]Model{"m": {}}},
		},
	}
	if _, err := Resolve(cfg, ""); err == nil {
		t.Fatal("expected error when no API key")
	}
}

// TestResolveNoProviders 验证无 provider 时报错。
func TestResolveNoProviders(t *testing.T) {
	if _, err := Resolve(Config{}, ""); err == nil {
		t.Fatal("expected error when no providers")
	}
}

// TestResolveDefaultProviderNotFound 验证 default_provider 不存在时报错。
func TestResolveDefaultProviderNotFound(t *testing.T) {
	cfg := testConfig()
	cfg.DefaultProvider = "nope"
	if _, err := Resolve(cfg, ""); err == nil {
		t.Fatal("expected error when default_provider not found")
	}
}

// TestConfigYAML 验证多 provider 多模型 YAML 结构解析。
func TestConfigYAML(t *testing.T) {
	yamlText := `
default_provider: deepseek
providers:
  deepseek:
    wire_api: openai
    base_url: https://api.deepseek.com/
    api_key: sk-1
    models:
      deepseek-v4-flash:
        context_window: 128000
        thinking:
          enabled: false
          efforts: [low, high, max]
      deepseek-v4:
        context_window: 256000
  claude:
    wire_api: anthropic
    models:
      claude-sonnet-5: {}
`
	var cfg Config
	if err := yamlUnmarshalStrict([]byte(yamlText), &cfg); err != nil {
		t.Fatalf("yaml parse: %v", err)
	}
	if cfg.DefaultProvider != "deepseek" {
		t.Errorf("default provider: got %q", cfg.DefaultProvider)
	}
	if len(cfg.Providers) != 2 {
		t.Fatalf("providers: got %d want 2", len(cfg.Providers))
	}
	ds := cfg.Providers["deepseek"]
	if ds.WireAPI != WireOpenAI || ds.BaseURL != "https://api.deepseek.com/" || ds.APIKey != "sk-1" {
		t.Errorf("deepseek: %+v", ds)
	}
	flash := ds.Models["deepseek-v4-flash"]
	if flash.ContextWindow != 128000 {
		t.Errorf("deepseek-v4-flash: %+v", flash)
	}
	if flash.Thinking == nil {
		t.Fatal("deepseek-v4-flash: thinking not parsed")
	}
	if flash.Thinking.Enabled == nil || *flash.Thinking.Enabled {
		t.Errorf("deepseek-v4-flash thinking.enabled: %+v", flash.Thinking.Enabled)
	}
	if !slices.Equal(flash.Thinking.Efforts, []string{EffortLow, EffortHigh, EffortMax}) {
		t.Errorf("deepseek-v4-flash thinking.efforts: got %v", flash.Thinking.Efforts)
	}
	if cfg.Providers["claude"].WireAPI != WireAnthropic {
		t.Errorf("claude wire api: %q", cfg.Providers["claude"].WireAPI)
	}
}

// TestResolveThinkingDefault 验证未配置 thinking 时默认启用、默认支持集、档位 high。
func TestResolveThinkingDefault(t *testing.T) {
	r, err := Resolve(testConfig(), "") // 模型均未配置 thinking
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !r.ThinkingEnabled {
		t.Error("thinking: expected enabled by default")
	}
	if r.ThinkingEffort != DefaultThinkingEffort {
		t.Errorf("thinking effort: got %q want %q", r.ThinkingEffort, DefaultThinkingEffort)
	}
	if !slices.Equal(r.ThinkingEfforts, DefaultEfforts) {
		t.Errorf("thinking efforts: got %v want default %v", r.ThinkingEfforts, DefaultEfforts)
	}
}

// TestResolveThinkingDisabled 验证 enabled: false 生效。
func TestResolveThinkingDisabled(t *testing.T) {
	f := false
	cfg := Config{
		Providers: map[string]ProviderConfig{
			"p": {APIKey: "k", Models: map[string]Model{
				"m": {Thinking: &Thinking{Enabled: &f}},
			}},
		},
	}
	r, err := Resolve(cfg, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.ThinkingEnabled {
		t.Error("thinking: expected disabled")
	}
}

// TestResolveThinkingEfforts 验证 efforts 支持集解析；high 不在集内时
// 当前档位回退到第一个。
func TestResolveThinkingEfforts(t *testing.T) {
	cfg := Config{
		Providers: map[string]ProviderConfig{
			"p": {APIKey: "k", Models: map[string]Model{
				"m": {Thinking: &Thinking{Efforts: []string{EffortLow, EffortMax}}},
			}},
		},
	}
	r, err := Resolve(cfg, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !slices.Contains(r.ThinkingEfforts, EffortMax) {
		t.Errorf("thinking efforts: got %v want contains %q", r.ThinkingEfforts, EffortMax)
	}
	// high 不在集内 → 当前档位取第一个（low）
	if r.ThinkingEffort != EffortLow {
		t.Errorf("thinking effort: got %q want %q", r.ThinkingEffort, EffortLow)
	}
}

// TestResolveThinkingEffortsContainHigh 验证 high 在集内时当前档位保持 high。
func TestResolveThinkingEffortsContainHigh(t *testing.T) {
	cfg := Config{
		Providers: map[string]ProviderConfig{
			"p": {APIKey: "k", Models: map[string]Model{
				"m": {Thinking: &Thinking{Efforts: []string{EffortLow, EffortHigh, EffortMax}}},
			}},
		},
	}
	r, err := Resolve(cfg, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.ThinkingEffort != EffortHigh {
		t.Errorf("thinking effort: got %q want %q", r.ThinkingEffort, EffortHigh)
	}
}
