package provider

import (
	"fmt"
	"os"
)

// NewClient 为给定配置构建流式 LLM 客户端。API key 从环境变量读取
// （Config.EnvKey，未指定时按 wire API 推断）；缺少 key 时报错。
// BaseURL 为空时使用 SDK 默认端点。
func NewClient(cfg Config) (Client, error) {
	wire, err := parseWireAPI(cfg.Provider)
	if err != nil {
		return nil, err
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("provider: model is required")
	}

	envKey := cfg.EnvKey
	if envKey == "" {
		envKey = DefaultEnvKey(wire)
	}
	apiKey := cfg.APIKey
	if apiKey == "" {
		apiKey = os.Getenv(envKey)
	}
	if apiKey == "" {
		return nil, fmt.Errorf("provider: no API key (set %s, configure env_key, or put api_key in the config file)", envKey)
	}

	switch wire {
	case WireOpenAI:
		return newOpenAIClient(cfg, apiKey), nil
	case WireAnthropic:
		return newAnthropicClient(cfg, apiKey), nil
	default:
		return nil, fmt.Errorf("provider: unsupported wire api %q", wire)
	}
}

func parseWireAPI(name string) (WireAPI, error) {
	switch name {
	case "openai", "responses", "":
		return WireOpenAI, nil
	case "anthropic", "claude", "messages":
		return WireAnthropic, nil
	default:
		return "", fmt.Errorf("provider: unknown provider %q (want openai or anthropic)", name)
	}
}

// providerBase 是两个 wire API 共享的 provider 基础实现。
type providerBase struct {
	model   string
	baseURL string
	apiKey  string
}

func (p *providerBase) Model() string      { return p.model }
func (p *providerBase) BaseURL() string    { return p.baseURL }
func (p *providerBase) ContextWindow() int { return ContextWindowFor(p.model) }
