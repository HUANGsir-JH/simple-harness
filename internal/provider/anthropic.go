package provider

import (
	"context"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/agent-project/harness/internal/config"
)

// anthropicClient 基于 Anthropic Messages API 实现 Client。
type anthropicClient struct {
	providerBase
	client anthropic.MessageService
}

func newAnthropicClient(res *config.ProviderConfig) *anthropicClient {
	opts := []option.RequestOption{option.WithAPIKey(res.APIKey)}
	// 某些环境（系统代理）会在出站请求注入 `Authorization: Bearer <无效值>`
	// 头，DeepSeek 等兼容端点优先读 Authorization 导致 401。
	// 用 WithAuthToken 显式设置正确的 Bearer 头覆盖它（与 X-Api-Key 双保险）。
	opts = append(opts, option.WithAuthToken(res.APIKey))
	if res.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(res.BaseURL))
	}
	c := anthropic.NewClient(opts...)
	return &anthropicClient{
		providerBase: providerBase{
			baseURL:         res.BaseURL,
			apiKey:          res.APIKey,
			contextWindow:   res.ContextWindow,
			thinkingEnabled: true, // thinking 默认开启（2026-08-10 删 enabled 配置项）
			thinkingEffort:  res.ThinkingEffort,
		},
		client: c.Messages,
	}
}

// defaultMaxTokens 是未配置时的输出 token 上限，取端点允许的最大值。
// anthropic 协议要求 max_tokens 必填（SDK api:"required"），但硬编码小值会截断
// 真实超长任务：DeepSeek 长思考曾把 4096 占满 → thinking 截断（stop_reason=max_tokens）
// 且 text 无输出。取 DeepSeek 有效范围上限（[1, 393216]），使 harness 不再是输出
// 长度的限制因素（用户约束：不设小上限）。
const defaultMaxTokens = 393216

func (a *anthropicClient) Stream(ctx context.Context, req Request) (EventStream, error) {
	// model 是请求参数（ADR-026）：client 只承载连接不持模型，每次采样由
	// sample 经 Request.Model 传入（链路：AgentState → rc → sample）。空则报错
	// 防御（正常路径 agent 默认模型保证非空）。
	if req.Model == "" {
		return nil, fmt.Errorf("provider: request model is empty")
	}
	params := anthropic.MessageNewParams{
		Model:     req.Model,
		MaxTokens: defaultMaxTokens,
		Messages:  toAnthropicMessages(req.Messages),
	}
	// thinking 默认开启（client 侧 true），per-call 可经 rc 覆盖（/thinking、
	// --thinking/--no-thinking 写 AgentState → rc）。
	thinkingEnabled := a.thinkingEnabled
	if req.ThinkingEnabled != nil {
		thinkingEnabled = *req.ThinkingEnabled
	}
	effort := a.thinkingEffort
	if req.ThinkingEffort != "" {
		effort = req.ThinkingEffort
	}
	// thinking 参数：默认不传（DeepSeek 等兼容端点默认开启 thinking，且由端点自行
	// 管理思考长度）。传小的 budget_tokens 反而导致 thinking 截断、text 无输出
	// （实测 budget=1024 时 effort=high 的 thinking 被 max_tokens 截断）。仅需
	// 关闭时显式传 disabled（兼容端点默认开启，不传关不掉）。
	if !thinkingEnabled {
		params.Thinking = anthropic.ThinkingConfigParamUnion{OfDisabled: &anthropic.ThinkingConfigDisabledParam{}}
	} else {
		// 思考深度档位独立传递（output_config.effort），不与 thinking budget 绑定。
		params.OutputConfig = anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffort(effort)}
	}
	if req.Instructions != "" {
		params.System = []anthropic.TextBlockParam{{Text: req.Instructions}}
	}
	if req.MaxOutputTokens > 0 {
		params.MaxTokens = int64(req.MaxOutputTokens)
	}
	if len(req.Tools) > 0 {
		tools, err := toAnthropicTools(req.Tools)
		if err != nil {
			return nil, err
		}
		params.Tools = tools
	}

	stream := a.client.NewStreaming(ctx, params)
	return newAnthropicStream(stream), nil
}
