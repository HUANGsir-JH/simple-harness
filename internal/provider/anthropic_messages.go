package provider

import (
	"encoding/json"
	"fmt"

	"github.com/agent-project/harness/internal/messages"
	"github.com/anthropics/anthropic-sdk-go"
)

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
			// thinking-only 的 assistant（content 空且无 tool_calls）：若有可重放的
			// thinking（带签名，ADR-025 修订完整回传）则重放；否则（无 thinking，或
			// thinking 无签名无法重放）为空消息，anthropic 不接受空 assistant 消息 →
			// 跳过，前后 user 消息自然相邻（连续 user 是合法格式，tool_result 即以
			// user 发送）。模型看到"两个 user 之间无回复"，语义准确且零污染。
			if m.Content == "" && len(m.ToolCalls) == 0 && m.ThinkingSignature == "" {
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
	// ADR-025 修订：thinking 完整回传。ThinkingSignature 非空时重放 thinking 块
	// （DeepSeek 流式签名经 signature_delta 捕获；实测回传 200 且计入
	// input_tokens）。签名是标准 anthropic 协议的重放凭据——严格端点校验签名，
	// 无签名回传会 400；故仅签名非空才重放，其余端点 thinking 仍只存不重放
	// （免格式适配）。
	blocks := make([]anthropic.ContentBlockParamUnion, 0, len(m.ToolCalls)+2)
	if m.ThinkingSignature != "" {
		blocks = append(blocks, anthropic.ContentBlockParamUnion{
			OfThinking: &anthropic.ThinkingBlockParam{
				Signature: m.ThinkingSignature,
				Thinking:  m.Thinking,
			},
		})
	}
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

func toAnthropicTools(tools []ToolSpec) ([]anthropic.ToolUnionParam, error) {
	var out []anthropic.ToolUnionParam
	for _, t := range tools {
		inputSchema := map[string]any{"type": "object", "properties": map[string]any{}}
		if len(t.Parameters) > 0 {
			if err := json.Unmarshal(t.Parameters, &inputSchema); err != nil {
				// fail-loud：工具 schema 是编程产物（内置由 jsonschema 生成，必然
				// 有效；此处防御自定义工具手写坏 schema），采样时报错而非静默发空
				// schema 给模型（C4）。
				return nil, fmt.Errorf("tool %q: 参数 schema 非法: %w", t.Name, err)
			}
		}
		out = append(out, anthropic.ToolUnionParam{
			OfTool: &anthropic.ToolParam{
				Name:        t.Name,
				Description: anthropic.String(t.Description),
				InputSchema: toAnthropicInputSchema(inputSchema),
			},
		})
	}
	return out, nil
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
