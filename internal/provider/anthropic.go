package provider

import (
	"context"
	"encoding/json"

	"github.com/agent-project/harness/internal/messages"
	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
)

// anthropicClient 基于 Anthropic Messages API 实现 Client。
type anthropicClient struct {
	providerBase
	client anthropic.MessageService
}

func newAnthropicClient(res *Resolved) *anthropicClient {
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
			model:           res.Model,
			baseURL:         res.BaseURL,
			apiKey:          res.APIKey,
			contextWindow:   res.ContextWindow,
			thinkingEnabled: res.ThinkingEnabled,
			thinkingEffort:  res.ThinkingEffort,
		},
		client: c.Messages,
	}
}

func (a *anthropicClient) Stream(ctx context.Context, req Request) (EventStream, error) {
	params := anthropic.MessageNewParams{
		Model:     a.model,
		MaxTokens: 4096, // 硬默认值；阶段二配置中完善
		Messages:  toAnthropicMessages(req.Messages),
	}
	// thinking：按 Anthropic Messages API 标准 thinking 参数开启（budget_tokens
	// 取最小合法值，兼容端点可忽略）；档位通过 SDK 的 output_config.effort 传递。
	// 关闭时显式传 thinking disabled —— DeepSeek 等兼容端点默认开启 thinking。
	if a.thinkingEnabled {
		params.Thinking = anthropic.ThinkingConfigParamOfEnabled(1024)
		params.OutputConfig = anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffort(a.thinkingEffort)}
	} else {
		params.Thinking = anthropic.ThinkingConfigParamUnion{OfDisabled: &anthropic.ThinkingConfigDisabledParam{}}
	}
	if req.Instructions != "" {
		params.System = []anthropic.TextBlockParam{{Text: req.Instructions}}
	}
	if req.MaxOutputTokens > 0 {
		params.MaxTokens = int64(req.MaxOutputTokens)
	}
	if len(req.Tools) > 0 {
		params.Tools = toAnthropicTools(req.Tools)
	}

	stream := a.client.NewStreaming(ctx, params)
	return &anthropicStream{stream: stream}, nil
}

// --- 转换：统一消息 → Messages API 参数 ------------------------------

func toAnthropicMessages(msgs []*messages.Message) []anthropic.MessageParam {
	var out []anthropic.MessageParam
	for _, m := range msgs {
		switch m.Role {
		case messages.RoleUser:
			out = append(out, anthropic.MessageParam{
				Role:    anthropic.MessageParamRoleUser,
				Content: []anthropic.ContentBlockParamUnion{{OfText: &anthropic.TextBlockParam{Text: m.Content}}},
			})
		case messages.RoleDeveloper:
			// Messages API 没有 developer 角色；developer 消息会成为 system 块。
			// 历史中不支持多个 system 块，因此将其合并进 user 消息
			//（系统提示本身通过 Stream 中的 Instructions 设置）。
			out = append(out, anthropic.MessageParam{
				Role:    anthropic.MessageParamRoleUser,
				Content: []anthropic.ContentBlockParamUnion{{OfText: &anthropic.TextBlockParam{Text: m.Content}}},
			})
		case messages.RoleAssistant:
			out = append(out, toAnthropicAssistantMessage(m))
		case messages.RoleTool:
			out = append(out, toAnthropicToolResult(m))
		}
	}
	return out
}

func toAnthropicAssistantMessage(m *messages.Message) anthropic.MessageParam {
	blocks := make([]anthropic.ContentBlockParamUnion, 0, len(m.ToolCalls)+1)
	if m.Content != "" {
		blocks = append(blocks, anthropic.ContentBlockParamUnion{OfText: &anthropic.TextBlockParam{Text: m.Content}})
	}
	for _, tc := range m.ToolCalls {
		input := map[string]any{}
		if len(tc.Args) > 0 {
			_ = json.Unmarshal(tc.Args, &input)
		}
		blocks = append(blocks, anthropic.ContentBlockParamUnion{
			OfToolUse: &anthropic.ToolUseBlockParam{
				ID:    tc.ID,
				Name:  tc.Name,
				Input: input,
			},
		})
	}
	return anthropic.MessageParam{
		Role:    anthropic.MessageParamRoleAssistant,
		Content: blocks,
	}
}

func toAnthropicToolResult(m *messages.Message) anthropic.MessageParam {
	content := []anthropic.ToolResultBlockParamContentUnion{{OfText: &anthropic.TextBlockParam{Text: m.Content}}}
	return anthropic.MessageParam{
		Role: anthropic.MessageParamRoleUser,
		Content: []anthropic.ContentBlockParamUnion{
			{OfToolResult: &anthropic.ToolResultBlockParam{
				ToolUseID: m.ToolCallID,
				Content:   content,
			}},
		},
	}
}

func toAnthropicTools(tools []ToolSpec) []anthropic.ToolUnionParam {
	var out []anthropic.ToolUnionParam
	for _, t := range tools {
		inputSchema := map[string]any{"type": "object", "properties": map[string]any{}}
		if len(t.Parameters) > 0 {
			_ = json.Unmarshal(t.Parameters, &inputSchema)
		}
		out = append(out, anthropic.ToolUnionParam{
			OfTool: &anthropic.ToolParam{
				Name:        t.Name,
				Description: anthropic.String(t.Description),
				InputSchema: toAnthropicInputSchema(inputSchema),
			},
		})
	}
	return out
}

// toAnthropicInputSchema 将通用 JSON Schema map 转换为 SDK 的
// ToolInputSchemaParam，未知字段通过 ExtraFields 保留。
func toAnthropicInputSchema(m map[string]any) anthropic.ToolInputSchemaParam {
	s := anthropic.ToolInputSchemaParam{
		Properties: m["properties"],
	}
	if req, ok := m["required"].([]any); ok {
		for _, r := range req {
			if rs, ok := r.(string); ok {
				s.Required = append(s.Required, rs)
			}
		}
	}
	for k, v := range m {
		switch k {
		case "type", "properties", "required":
		default:
			if s.ExtraFields == nil {
				s.ExtraFields = map[string]any{}
			}
			s.ExtraFields[k] = v
		}
	}
	return s
}

// --- 流适配器 -------------------------------------------------------------

type anthropicStream struct {
	stream *ssestream.Stream[anthropic.MessageStreamEventUnion]
	cur    Event
	err    error
}

func (s *anthropicStream) Next() bool {
	for s.stream.Next() {
		ev := s.stream.Current()
		switch ev.Type {
		case "content_block_start":
			cb := ev.ContentBlock
			if cb.Type == "tool_use" {
				args, _ := json.Marshal(cb.Input)
				s.cur = Event{
					Type: EventToolCall,
					ToolCall: &messages.ToolCall{
						ID:   cb.ID,
						Name: cb.Name,
						Args: args,
					},
				}
				return true
			}
			continue
		case "content_block_delta":
			if ev.Delta.Type == "text_delta" {
				s.cur = Event{Type: EventTextDelta, Text: ev.Delta.Text}
				return true
			}
			continue
		case "message_stop":
			s.cur = Event{Type: EventDone}
			return true
		default:
			continue
		}
	}
	if err := s.stream.Err(); err != nil {
		s.err = err
	}
	return false
}

func (s *anthropicStream) Current() Event { return s.cur }
func (s *anthropicStream) Err() error     { return s.err }
func (s *anthropicStream) Close() error   { return s.stream.Close() }
