package provider

import (
	"fmt"
	"os"
	"slices"
	"sort"
)

// DefaultContextWindow 是模型未配置 context_window 时的默认值。
const DefaultContextWindow = 128000

// DefaultEfforts 是模型未配置 thinking.efforts 时的默认档位集。
var DefaultEfforts = []string{EffortLow, EffortHigh, EffortMax}

// Resolved 是解析后的运行时配置：选定 provider + 模型。
// 由 Resolve 产出，NewClient 直接消费。
type Resolved struct {
	ProviderID      string
	BaseURL         string
	APIKey          string
	Model           string
	ContextWindow   int
	ThinkingEnabled bool
	ThinkingEffort  string
	ThinkingEfforts []string
}

// Resolve 从 Config 与可选的 --model 参数解析出运行时配置。
//
// 选择优先级：
//   - provider：default_provider 指定；未指定取 providers 中排序后的第一个
//   - model：--model 指定（必须在所选 provider 的 models 中，否则报错）；
//     未指定取该 provider 的 models 中排序后的第一个
//
// context_window 取模型定义值；未配置（0）回退 DefaultContextWindow。
// API key 解析：APIKey 字段 → EnvKey 环境变量 → DefaultAPIKeyEnv（ANTHROPIC_API_KEY）。
func Resolve(cfg Config, modelFlag string) (*Resolved, error) {
	providerID, p, err := resolveProvider(cfg)
	if err != nil {
		return nil, err
	}

	model, err := resolveModel(p.Models, modelFlag)
	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", providerID, err)
	}

	cw := DefaultContextWindow
	m := p.Models[model]
	if m.ContextWindow > 0 {
		cw = m.ContextWindow
	}

	thinkingEnabled, thinkingEfforts, thinkingEffort := resolveThinking(m)

	apiKey, err := resolveAPIKey(p)
	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", providerID, err)
	}

	return &Resolved{
		ProviderID:      providerID,
		BaseURL:         p.BaseURL,
		APIKey:          apiKey,
		Model:           model,
		ContextWindow:   cw,
		ThinkingEnabled: thinkingEnabled,
		ThinkingEffort:  thinkingEffort,
		ThinkingEfforts: thinkingEfforts,
	}, nil
}

// resolveThinking 解析模型的 thinking 配置。
//   - enabled：默认启用（Enabled nil → true）
//   - efforts：默认 DefaultEfforts；配置了则用配置值（支持集，供 --effort 校验）
//   - current：当前生效档位，默认 DefaultThinkingEffort（high）；
//     若 high 不在 efforts 中，取 efforts 第一个
func resolveThinking(m Model) (enabled bool, efforts []string, current string) {
	enabled = true
	efforts = DefaultEfforts
	if m.Thinking != nil {
		if m.Thinking.Enabled != nil {
			enabled = *m.Thinking.Enabled
		}
		if len(m.Thinking.Efforts) > 0 {
			efforts = m.Thinking.Efforts
		}
	}
	current = DefaultThinkingEffort
	if !slices.Contains(efforts, current) && len(efforts) > 0 {
		current = efforts[0]
	}
	return
}

// resolveProvider 选择 provider：default_provider 优先，否则取排序后第一个。
func resolveProvider(cfg Config) (string, ProviderConfig, error) {
	if len(cfg.Providers) == 0 {
		return "", ProviderConfig{}, fmt.Errorf("providers: no providers configured")
	}
	if cfg.DefaultProvider != "" {
		p, ok := cfg.Providers[cfg.DefaultProvider]
		if !ok {
			return "", ProviderConfig{}, fmt.Errorf("providers: default_provider %q not found", cfg.DefaultProvider)
		}
		return cfg.DefaultProvider, p, nil
	}
	// map 遍历无序，排序取确定性。
	names := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	first := names[0]
	return first, cfg.Providers[first], nil
}

// resolveModel 选择模型：modelFlag 优先（须存在），否则取排序后第一个。
func resolveModel(models map[string]Model, modelFlag string) (string, error) {
	if len(models) == 0 {
		return "", fmt.Errorf("models: no models configured")
	}
	if modelFlag != "" {
		if _, ok := models[modelFlag]; !ok {
			return "", fmt.Errorf("models: %q not found in this provider", modelFlag)
		}
		return modelFlag, nil
	}
	names := make([]string, 0, len(models))
	for name := range models {
		names = append(names, name)
	}
	sort.Strings(names)
	return names[0], nil
}

// resolveAPIKey 解析 API key：APIKey 字段 → EnvKey 环境变量 → DefaultAPIKeyEnv。
func resolveAPIKey(p ProviderConfig) (string, error) {
	if p.APIKey != "" {
		return p.APIKey, nil
	}
	envKey := p.EnvKey
	if envKey == "" {
		envKey = DefaultAPIKeyEnv
	}
	if k := os.Getenv(envKey); k != "" {
		return k, nil
	}
	return "", fmt.Errorf("no API key (set api_key, env_key %q, or %s)", envKey, DefaultAPIKeyEnv)
}
