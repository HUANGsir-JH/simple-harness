package provider

import (
	"context"
	"encoding/json"
	"strings"

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
	return newAnthropicStream(stream), nil
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
	// 多块：一批 tool_result 合并进一条 user 消息（anthropic 要求 tool_use 后
	// 的下一条消息含全部对应 tool_result）。
	if len(m.ToolResults) > 0 {
		blocks := make([]anthropic.ContentBlockParamUnion, 0, len(m.ToolResults))
		for _, r := range m.ToolResults {
			content := []anthropic.ToolResultBlockParamContentUnion{{OfText: &anthropic.TextBlockParam{Text: r.Content}}}
			block := anthropic.ToolResultBlockParam{
				ToolUseID: r.ToolCallID,
				Content:   content,
			}
			if !r.Success {
				block.IsError = anthropic.Bool(true)
			}
			blocks = append(blocks, anthropic.ContentBlockParamUnion{OfToolResult: &block})
		}
		return anthropic.MessageParam{
			Role:    anthropic.MessageParamRoleUser,
			Content: blocks,
		}
	}
	// 单块兼容。
	content := []anthropic.ToolResultBlockParamContentUnion{{OfText: &anthropic.TextBlockParam{Text: m.Content}}}
	block := anthropic.ToolResultBlockParam{
		ToolUseID: m.ToolCallID,
		Content:   content,
	}
	if m.IsError {
		block.IsError = anthropic.Bool(true)
	}
	return anthropic.MessageParam{
		Role: anthropic.MessageParamRoleUser,
		Content: []anthropic.ContentBlockParamUnion{
			{OfToolResult: &block},
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
	// pending 跟踪正在流式累积参数的 tool_use 块（按 content block index）。
	// anthropic 工具参数经 input_json_delta 分片累积，content_block_start
	// 时 input 可能为空（大参数流式）或已带全（小参数）。
	pending map[int64]*pendingToolCall
}

// pendingToolCall 是一个正在累积参数的 tool_use 块。
type pendingToolCall struct {
	call    *messages.ToolCall
	initial string          // content_block_start 时的 input（JSON 序列化）
	partial strings.Builder // input_json_delta 累积
}

func newAnthropicStream(stream *ssestream.Stream[anthropic.MessageStreamEventUnion]) *anthropicStream {
	return &anthropicStream{stream: stream, pending: map[int64]*pendingToolCall{}}
}

func (s *anthropicStream) Next() bool {
	for s.stream.Next() {
		ev := s.stream.Current()
		switch ev.Type {
		case "content_block_start":
			cb := ev.ContentBlock
			if cb.Type == "tool_use" {
				// 记录工具调用开始；参数在 input_json_delta 或 start 的 input 中。
				initial := ""
				if cb.Input != nil {
					if data, err := json.Marshal(cb.Input); err == nil {
						initial = string(data)
					}
				}
				s.pending[ev.Index] = &pendingToolCall{
					call: &messages.ToolCall{ID: cb.ID, Name: cb.Name},
					initial: initial,
				}
			}
			continue
		case "content_block_delta":
			switch ev.Delta.Type {
			case "text_delta":
				s.cur = Event{Type: EventTextDelta, Text: ev.Delta.Text}
				return true
			case "thinking_delta":
				s.cur = Event{Type: EventThinkingDelta, Text: ev.Delta.Thinking}
				return true
			case "input_json_delta":
				if p := s.pending[ev.Index]; p != nil {
					p.partial.WriteString(ev.Delta.PartialJSON)
				}
				continue
			}
			continue
		case "content_block_stop":
			if p := s.pending[ev.Index]; p != nil {
				// 完成工具调用：优先用流式累积的参数，其次 start 自带 input，最后空对象。
				args := p.partial.String()
				if args == "" {
					args = p.initial
				}
				if args == "" {
					args = "{}"
				}
				p.call.Args = json.RawMessage(args)
				s.cur = Event{Type: EventToolCall, ToolCall: p.call}
				delete(s.pending, ev.Index)
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
