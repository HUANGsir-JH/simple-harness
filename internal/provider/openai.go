package provider

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/agent-project/harness/internal/messages"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/ssestream"
	"github.com/openai/openai-go/responses"
)

// openAIClient implements Client over the OpenAI Responses API (and
// OpenAI-compatible endpoints). Per ADR-001 there is no per-vendor
// implementation; the base URL override covers compatible backends.
type openAIClient struct {
	providerBase
	client responses.ResponseService
}

func newOpenAIClient(cfg Config, apiKey string) *openAIClient {
	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	c := openai.NewClient(opts...)
	return &openAIClient{
		providerBase: providerBase{model: cfg.Model, baseURL: cfg.BaseURL, apiKey: apiKey},
		client:       c.Responses,
	}
}

func (o *openAIClient) WireAPI() WireAPI { return WireOpenAI }

func (o *openAIClient) Stream(ctx context.Context, req Request) (EventStream, error) {
	params := responses.ResponseNewParams{
		Model:             o.model,
		Instructions:      openai.String(req.Instructions),
		Input:             responses.ResponseNewParamsInputUnion{OfInputItemList: toOpenAIInput(req.Messages)},
		ParallelToolCalls: openai.Bool(true),
	}
	if req.MaxOutputTokens > 0 {
		params.MaxOutputTokens = openai.Int(int64(req.MaxOutputTokens))
	}
	if len(req.Tools) > 0 {
		params.Tools = toOpenAITools(req.Tools)
	}

	stream := o.client.NewStreaming(ctx, params)
	return &openAIStream{stream: stream}, nil
}

// --- conversion: unified messages → Responses input items -------------------

func toOpenAIInput(msgs []*messages.Message) responses.ResponseInputParam {
	var out responses.ResponseInputParam
	for _, m := range msgs {
		switch m.Role {
		case messages.RoleUser:
			out = append(out, responses.ResponseInputItemUnionParam{
				OfMessage: &responses.EasyInputMessageParam{
					Role:    responses.EasyInputMessageRoleUser,
					Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String(m.Content)},
				},
			})
		case messages.RoleDeveloper:
			out = append(out, responses.ResponseInputItemUnionParam{
				OfMessage: &responses.EasyInputMessageParam{
					Role:    responses.EasyInputMessageRoleDeveloper,
					Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String(m.Content)},
				},
			})
		case messages.RoleAssistant:
			out = append(out, toOpenAIAssistantItem(m))
		case messages.RoleTool:
			out = append(out, toOpenAIToolResult(m))
		}
	}
	return out
}

func toOpenAIAssistantItem(m *messages.Message) responses.ResponseInputItemUnionParam {
	// Assistant history messages that made tool calls must be sent as
	// output-message items so their function-call children can be referenced.
	if len(m.ToolCalls) > 0 {
		content := make([]responses.ResponseOutputMessageContentUnionParam, 0, len(m.ToolCalls)+1)
		if m.Content != "" {
			content = append(content, responses.ResponseOutputMessageContentUnionParam{
				OfOutputText: &responses.ResponseOutputTextParam{Text: m.Content},
			})
		}
		for _, tc := range m.ToolCalls {
			content = append(content, responses.ResponseOutputMessageContentUnionParam{
				OfOutputText: &responses.ResponseOutputTextParam{Text: ""}, // placeholder, refined in phase 2
			})
			_ = tc
		}
		return responses.ResponseInputItemUnionParam{
			OfOutputMessage: &responses.ResponseOutputMessageParam{
				ID:      m.ID,
				Content: content,
			},
		}
	}
	return responses.ResponseInputItemUnionParam{
		OfMessage: &responses.EasyInputMessageParam{
			Role:    responses.EasyInputMessageRoleAssistant,
			Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String(m.Content)},
		},
	}
}

func toOpenAIToolResult(m *messages.Message) responses.ResponseInputItemUnionParam {
	return responses.ResponseInputItemUnionParam{
		OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
			CallID: m.ToolCallID,
			Output: m.Content,
		},
	}
}

// toOpenAITools converts unified tool specs to Responses API tool params.
func toOpenAITools(tools []ToolSpec) []responses.ToolUnionParam {
	var out []responses.ToolUnionParam
	for _, t := range tools {
		params := map[string]any{}
		if len(t.Parameters) > 0 {
			_ = json.Unmarshal(t.Parameters, &params)
		}
		out = append(out, responses.ToolUnionParam{
			OfFunction: &responses.FunctionToolParam{
				Name:        t.Name,
				Description: openai.String(t.Description),
				Parameters:  params,
			},
		})
	}
	return out
}

// --- stream adapter ----------------------------------------------------------

type openAIStream struct {
	stream *ssestream.Stream[responses.ResponseStreamEventUnion]
	cur    Event
	err    error
}

func (s *openAIStream) Next() bool {
	for s.stream.Next() {
		ev := s.stream.Current()
		switch ev.Type {
		case "response.output_text.delta":
			s.cur = Event{Type: EventTextDelta, Text: ev.Delta.OfString}
			return true
		case "response.output_item.done":
			if ev.Item.Type == "function_call" {
				s.cur = Event{
					Type: EventToolCall,
					ToolCall: &messages.ToolCall{
						ID:   ev.Item.ID,
						Name: ev.Item.Name,
						Args: json.RawMessage(ev.Item.Arguments),
					},
				}
				return true
			}
			continue
		case "response.completed":
			s.cur = Event{Type: EventDone}
			return true
		case "error":
			s.err = fmt.Errorf("openai stream error: %s", ev.Message)
			return false
		default:
			continue // ignore other event types
		}
	}
	if err := s.stream.Err(); err != nil {
		s.err = err
	}
	return false
}

func (s *openAIStream) Current() Event { return s.cur }
func (s *openAIStream) Err() error     { return s.err }
func (s *openAIStream) Close() error   { return s.stream.Close() }
