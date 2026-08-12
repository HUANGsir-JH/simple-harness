package provider

import (
	"encoding/json"
	"strings"

	"github.com/agent-project/harness/internal/messages"
	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
)

// --- 流适配器 -------------------------------------------------------------

type anthropicStream struct {
	stream *ssestream.Stream[anthropic.MessageStreamEventUnion]
	cur    Event
	err    error
	// usage 是本次采样的 token 用量（ADR-037 用量展示）：message_start 记录
	// input/cache，message_delta 覆盖累计 output/input，message_stop 时随
	// EventDone 一并发出（input_tokens 即"当前上下文占用"，供 footer 与压缩触发）。
	usage *messages.Usage
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
	// signature 是 thinking 块的数字签名（ADR-025 修订完整回传：content_block_start
	// 捕获，thinking_done 随块发出；重放 thinking 块时作为凭据）。
	signature string
}

func newAnthropicStream(stream *ssestream.Stream[anthropic.MessageStreamEventUnion]) *anthropicStream {
	return &anthropicStream{stream: stream, blocks: map[int64]*pendingBlock{}}
}

func (s *anthropicStream) Next() bool {
	for s.stream.Next() {
		ev := s.stream.Current()
		switch ev.Type {
		case "message_start":
			// 首块 usage：input + cache（output 是初始占位 1，后续 message_delta 覆盖）。
			s.usage = &messages.Usage{
				InputTokens:              ev.Message.Usage.InputTokens,
				CacheReadInputTokens:     ev.Message.Usage.CacheReadInputTokens,
				CacheCreationInputTokens: ev.Message.Usage.CacheCreationInputTokens,
				OutputTokens:             ev.Message.Usage.OutputTokens,
			}
			continue
		case "message_delta":
			// output_tokens 随流累计（覆盖式取最后一条为最终值）；input/cache 由
			// message_start 提供，规范上 message_delta 不带——仅当兼容端点确实在
			// delta 返回非零 input/cache 时才覆盖，避免冲掉 message_start 的正确值。
			if s.usage == nil {
				s.usage = &messages.Usage{}
			}
			s.usage.OutputTokens = ev.Usage.OutputTokens
			if ev.Usage.InputTokens > 0 {
				s.usage.InputTokens = ev.Usage.InputTokens
			}
			if ev.Usage.CacheReadInputTokens > 0 {
				s.usage.CacheReadInputTokens = ev.Usage.CacheReadInputTokens
			}
			if ev.Usage.CacheCreationInputTokens > 0 {
				s.usage.CacheCreationInputTokens = ev.Usage.CacheCreationInputTokens
			}
			continue
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
			if cb.Type == "thinking" {
				// 捕获 thinking 块签名（ADR-025 修订：完整回传）。SDK union
				// ContentBlockStartEventContentBlockUnion 的 Signature 字段即来自
				// ThinkingBlock 变体；DeepSeek 兼容端点恒返回。
				pb.signature = cb.Signature
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
				s.cur = Event{Type: EventThinkingDone, Text: pb.sb.String(), Signature: pb.signature}
				return true
			case "text":
				s.cur = Event{Type: EventTextDone, Text: pb.sb.String()}
				return true
			}
			continue
		case "message_stop":
			// 采样结束：usage 随 EventDone 一并发出（渲染/AgentState 累计用）。
			s.cur = Event{Type: EventDone, Usage: s.usage}
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
