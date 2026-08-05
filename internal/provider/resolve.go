package provider

import (
	"fmt"
	"os"
	"sort"
)

// DefaultContextWindow 是模型未配置 context_window 时的默认值。
const DefaultContextWindow = 128000

// Resolved 是解析后的运行时配置：选定 provider + 模型。
// 由 Resolve 产出，NewClient 直接消费。
type Resolved struct {
	ProviderID    string
	WireAPI       WireAPI
	BaseURL       string
	APIKey        string
	Model         string
	ContextWindow int
}

// Resolve 从 Config 与可选的 --model 参数解析出运行时配置。
//
// 选择优先级：
//   - provider：default_provider 指定；未指定取 providers 中排序后的第一个
//   - model：--model 指定（必须在所选 provider 的 models 中，否则报错）；
//     未指定取该 provider 的 models 中排序后的第一个
//
// context_window 取模型定义值；未配置（0）回退 DefaultContextWindow。
// API key 解析：APIKey 字段 → EnvKey 环境变量 → 该 wire API 的惯例环境变量名。
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
	if m := p.Models[model]; m.ContextWindow > 0 {
		cw = m.ContextWindow
	}

	apiKey, err := resolveAPIKey(p, p.WireAPI)
	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", providerID, err)
	}

	return &Resolved{
		ProviderID:    providerID,
		WireAPI:       p.WireAPI,
		BaseURL:       p.BaseURL,
		APIKey:        apiKey,
		Model:         model,
		ContextWindow: cw,
	}, nil
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

// resolveAPIKey 解析 API key：APIKey 字段 → EnvKey 环境变量 → 惯例变量名。
func resolveAPIKey(p ProviderConfig, w WireAPI) (string, error) {
	if p.APIKey != "" {
		return p.APIKey, nil
	}
	envKey := p.EnvKey
	if envKey == "" {
		envKey = DefaultEnvKey(w)
	}
	if k := os.Getenv(envKey); k != "" {
		return k, nil
	}
	return "", fmt.Errorf("no API key (set api_key, env_key %q, or %s)", envKey, DefaultEnvKey(w))
}
