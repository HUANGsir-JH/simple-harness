package provider

import (
	"fmt"
)

// NewClient 从解析后的运行时配置构建流式 LLM 客户端。
// 单 wire（anthropic Messages）：直接构造 anthropic 适配器。
// 调用方应先调用 Resolve 得到 Resolved，再传入本函数。
func NewClient(res *Resolved) (Client, error) {
	if res == nil {
		return nil, fmt.Errorf("provider: resolved config is nil")
	}
	return newAnthropicClient(res), nil
}

// providerBase 是 anthropic 客户端的基础实现。
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
