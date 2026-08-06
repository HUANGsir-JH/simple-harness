package provider

import (
	"fmt"
)

// NewClient 从解析后的运行时配置构建流式 LLM 客户端。
// 调用方应先调用 Resolve 得到 Resolved，再传入本函数。
func NewClient(res *Resolved) (Client, error) {
	if res == nil {
		return nil, fmt.Errorf("provider: resolved config is nil")
	}
	switch res.WireAPI {
	case WireOpenAI:
		return newOpenAIClient(res), nil
	case WireAnthropic:
		return newAnthropicClient(res), nil
	default:
		return nil, fmt.Errorf("provider: unsupported wire api %q", res.WireAPI)
	}
}

// providerBase 是各 wire API 共享的 provider 基础实现。
type providerBase struct {
	model           string
	baseURL         string
	apiKey          string
	contextWindow   int
	thinkingEnabled bool
	thinkingEffort  string
}

func (p *providerBase) Model() string      { return p.model }
func (p *providerBase) BaseURL() string    { return p.baseURL }
func (p *providerBase) ContextWindow() int { return p.contextWindow }
