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

// defaultMaxTokens 是未配置时的输出 token 上限，取端点允许的最大值。
// anthropic 协议要求 max_tokens 必填（SDK api:"required"），但硬编码小值会截断
// 真实超长任务：DeepSeek 长思考曾把 4096 占满 → thinking 截断（stop_reason=max_tokens）
// 且 text 无输出。取 DeepSeek 有效范围上限（[1, 393216]），使 harness 不再是输出
// 长度的限制因素（用户约束：不设小上限）。
const defaultMaxTokens = 393216

func (a *anthropicClient) Stream(ctx context.Context, req Request) (EventStream, error) {
	params := anthropic.MessageNewParams{
		Model:     a.model,
		MaxTokens: defaultMaxTokens,
		Messages:  toAnthropicMessages(req.Messages),
	}
	// thinking 参数：默认不传（DeepSeek 等兼容端点默认开启 thinking，且由端点自行
	// 管理思考长度）。传小的 budget_tokens 反而导致 thinking 截断、text 无输出
	// （实测 budget=1024 时 effort=high 的 thinking 被 max_tokens 截断）。仅需
	// 关闭时显式传 disabled（兼容端点默认开启，不传关不掉）。
	if !a.thinkingEnabled {
		params.Thinking = anthropic.ThinkingConfigParamUnion{OfDisabled: &anthropic.ThinkingConfigDisabledParam{}}
	} else {
		// 思考深度档位独立传递（output_config.effort），不与 thinking budget 绑定。
		params.OutputConfig = anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffort(a.thinkingEffort)}
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
			// thinking-only 的 assistant（content 空且无 tool_calls）：剥离 thinking
			// 后（ADR-025）无任何内容，anthropic 不接受空 assistant 消息 → 跳过，
			// 前后 user 消息自然相邻（连续 user 是合法格式，tool_result 即以 user
			// 发送）。模型看到"两个 user 之间无回复"，语义准确且零污染。
			if m.Content == "" && len(m.ToolCalls) == 0 {
				continue
			}
			out = append(out, toAnthropicAssistantMessage(m))
		case messages.RoleTool:
			out = append(out, toAnthropicToolResult(m))
		}
	}
	return out
}

func toAnthropicAssistantMessage(m *messages.Message) anthropic.MessageParam {
	// 注意：m.Thinking（推理文本）不重放——每次采样请求重放历史时都剥离
	// thinking（ADR-025：thinking 仅审计，不进模型上下文、免格式适配）。
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
	// blocks 跟踪所有内容块（按 content block index），content_block_stop 时
	// 按块类型发出完整块事件（text_done / thinking_done / tool_call）。ADR-025。
	// text/thinking 累积 delta 文本；tool_use 累积 input_json_delta 参数分片。
	blocks map[int64]*pendingBlock
}

// pendingBlock 是一个正在流式累积的内容块。
type pendingBlock struct {
	kind    string          // "text" | "thinking" | "tool_use"
	sb      strings.Builder // 文本 delta；tool_use 的 input_json_delta 分片
	initial string          // tool_use：content_block_start 时 input（小参数可能带全）
	call    *messages.ToolCall
}

func newAnthropicStream(stream *ssestream.Stream[anthropic.MessageStreamEventUnion]) *anthropicStream {
	return &anthropicStream{stream: stream, blocks: map[int64]*pendingBlock{}}
}

func (s *anthropicStream) Next() bool {
	for s.stream.Next() {
		ev := s.stream.Current()
		switch ev.Type {
		case "content_block_start":
			cb := ev.ContentBlock
			pb := &pendingBlock{kind: cb.Type}
			if cb.Type == "tool_use" {
				// 工具调用开始；参数可能已带全（content_block_start 时 input 非空）
				// 或经 input_json_delta 流式到达（大参数）。initial 兜底，分片优先。
				pb.call = &messages.ToolCall{ID: cb.ID, Name: cb.Name}
				if cb.Input != nil {
					if data, err := json.Marshal(cb.Input); err == nil {
						pb.initial = string(data)
					}
				}
			}
			s.blocks[ev.Index] = pb
			continue
		case "content_block_delta":
			pb := s.blocks[ev.Index]
			if pb == nil {
				continue
			}
			switch ev.Delta.Type {
			case "text_delta":
				pb.sb.WriteString(ev.Delta.Text)
				s.cur = Event{Type: EventTextDelta, Text: ev.Delta.Text}
				return true
			case "thinking_delta":
				pb.sb.WriteString(ev.Delta.Thinking)
				s.cur = Event{Type: EventThinkingDelta, Text: ev.Delta.Thinking}
				return true
			case "input_json_delta":
				pb.sb.WriteString(ev.Delta.PartialJSON)
			}
			continue
		case "content_block_stop":
			pb := s.blocks[ev.Index]
			if pb == nil {
				continue
			}
			delete(s.blocks, ev.Index)
			switch pb.kind {
			case "tool_use":
				// 参数：优先 input_json_delta 分片累积，其次 start 自带 input，最后空对象。
				args := pb.sb.String()
				if args == "" {
					args = pb.initial
				}
				if args == "" {
					args = "{}"
				}
				pb.call.Args = json.RawMessage(args)
				s.cur = Event{Type: EventToolCall, ToolCall: pb.call}
				return true
			case "thinking":
				s.cur = Event{Type: EventThinkingDone, Text: pb.sb.String()}
				return true
			case "text":
				s.cur = Event{Type: EventTextDone, Text: pb.sb.String()}
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
