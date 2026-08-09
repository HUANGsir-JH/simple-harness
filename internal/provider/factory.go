package provider

import (
	"fmt"

	"github.com/agent-project/harness/internal/config"
)

// NewClient 从解析后的生效配置构建流式 LLM 客户端。
// 单 wire（anthropic Messages）：直接构造 anthropic 适配器。
// 调用方应先调用 config.Resolve 得到 ProviderConfig，再传入本函数。
//
// 注意：client 只承载**连接**（base_url / api_key / 默认 thinking），**不含模型**。
// 模型是请求参数（ADR-026），每次采样经 Request.Model 传入（来自 AgentState →
// rc → sample）。client 被共享，跨会话/跨模型复用同一连接。
func NewClient(res *config.ProviderConfig) (Client, error) {
	if res == nil {
		return nil, fmt.Errorf("provider: resolved config is nil")
	}
	return newAnthropicClient(res), nil
}

// providerBase 是 anthropic 客户端的基础实现（纯连接，无模型）。
type providerBase struct {
	baseURL         string
	apiKey          string
	contextWindow   int
	thinkingEnabled bool
	thinkingEffort  string
}

func (p *providerBase) BaseURL() string    { return p.baseURL }
func (p *providerBase) ContextWindow() int { return p.contextWindow }
