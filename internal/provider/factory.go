package provider

import (
	"fmt"
	"os"
)

// NewClient builds a streaming LLM client for the given config. The API key
// is read from the environment (Config.EnvKey, defaulted per wire API);
// missing key is an error. BaseURL "" selects the SDK default endpoint.
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

// providerBase is the shared provider implementation for both wire APIs.
type providerBase struct {
	model   string
	baseURL string
	apiKey  string
}

func (p *providerBase) Model() string      { return p.model }
func (p *providerBase) BaseURL() string    { return p.baseURL }
func (p *providerBase) ContextWindow() int { return ContextWindowFor(p.model) }
