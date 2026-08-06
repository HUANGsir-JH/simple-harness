package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agent-project/harness/internal/messages"
)

// sseEvent 格式化单个 SSE 事件载荷。
func sseEvent(data string) string {
	return "data: " + data + "\n\n"
}

// anthropicSSE 格式化 Anthropic 风格 SSE 事件：SDK 按 `event:` 字段路由，
// 并从 JSON 的 `type` 字段填充 ev.Type，因此两者都必须存在
// （与真实 API 一致）。
func anthropicSSE(eventType, data string) string {
	return "event: " + eventType + "\ndata: " + data + "\n\n"
}

// TestOpenAIStreamTextDelta 验证 OpenAI 适配器将 output_text.delta 事件
// 转换为统一文本事件，并在 response.completed 时结束。
func TestOpenAIStreamTextDelta(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		var sb strings.Builder
		sb.WriteString(sseEvent(`{"type":"response.created","sequence_number":1}`))
		sb.WriteString(sseEvent(`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"Hel","sequence_number":2}`))
		sb.WriteString(sseEvent(`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"lo","sequence_number":3}`))
		sb.WriteString(sseEvent(`{"type":"response.completed","response":{"id":"resp_1"},"sequence_number":4}`))
		w.Write([]byte(sb.String()))
	}))
	defer srv.Close()

	c := newOpenAIClient(&Resolved{Model: "gpt-4o", BaseURL: srv.URL, APIKey: "test-key"})
	es, err := c.Stream(context.Background(), Request{Messages: []*messages.Message{NewTestUserMsg("hi")}})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer es.Close()

	var got strings.Builder
	done := false
	for es.Next() {
		ev := es.Current()
		switch ev.Type {
		case EventTextDelta:
			got.WriteString(ev.Text)
		case EventDone:
			done = true
		default:
			t.Errorf("unexpected event %q", ev.Type)
		}
	}
	if err := es.Err(); err != nil {
		t.Fatalf("stream err: %v", err)
	}
	if !done {
		t.Error("expected EventDone")
	}
	if got.String() != "Hello" {
		t.Errorf("text: got %q want %q", got.String(), "Hello")
	}
}

// TestOpenAIStreamFunctionCall 验证 OpenAI 适配器将已完成的 function_call
// 项转换为 EventToolCall。
func TestOpenAIStreamFunctionCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		var sb strings.Builder
		sb.WriteString(sseEvent(`{"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","name":"read_file","arguments":"","call_id":"call_1"},"output_index":0,"sequence_number":1}`))
		sb.WriteString(sseEvent(`{"type":"response.output_item.done","item":{"id":"fc_1","type":"function_call","name":"read_file","arguments":"{\"path\":\"a.txt\"}","call_id":"call_1"},"output_index":0,"sequence_number":2}`))
		sb.WriteString(sseEvent(`{"type":"response.completed","response":{"id":"resp_1"},"sequence_number":3}`))
		w.Write([]byte(sb.String()))
	}))
	defer srv.Close()

	c := newOpenAIClient(&Resolved{Model: "gpt-4o", BaseURL: srv.URL, APIKey: "test-key"})
	es, err := c.Stream(context.Background(), Request{Messages: []*messages.Message{NewTestUserMsg("read it")}})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer es.Close()

	var call *messages.ToolCall
	for es.Next() {
		ev := es.Current()
		switch ev.Type {
		case EventToolCall:
			call = ev.ToolCall
		case EventDone:
			// ok
		}
	}
	if call == nil {
		t.Fatal("expected a tool call event")
	}
	if call.Name != "read_file" || call.ID != "fc_1" {
		t.Errorf("call: got %+v", call)
	}
	if string(call.Args) != `{"path":"a.txt"}` {
		t.Errorf("args: got %s", call.Args)
	}
}

// TestOpenAIStreamError 验证流错误通过 Err() 暴露。
func TestOpenAIStreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sseEvent(`{"type":"error","code":"server_error","message":"boom","sequence_number":1}`)))
	}))
	defer srv.Close()

	c := newOpenAIClient(&Resolved{Model: "gpt-4o", BaseURL: srv.URL, APIKey: "test-key"})
	es, err := c.Stream(context.Background(), Request{Messages: []*messages.Message{NewTestUserMsg("hi")}})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer es.Close()
	for es.Next() {
		// consume
	}
	if es.Err() == nil {
		t.Error("expected stream error")
	}
}

// TestAnthropicStreamTextDelta 验证 Anthropic 适配器将 content_block_delta
// 文本事件转换为统一文本事件，并在 message_stop 时结束。
func TestAnthropicStreamTextDelta(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		var sb strings.Builder
		sb.WriteString(anthropicSSE("message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}`))
		sb.WriteString(anthropicSSE("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`))
		sb.WriteString(anthropicSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}`))
		sb.WriteString(anthropicSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`))
		sb.WriteString(anthropicSSE("content_block_stop", `{"type":"content_block_stop","index":0}`))
		sb.WriteString(anthropicSSE("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`))
		sb.WriteString(anthropicSSE("message_stop", `{"type":"message_stop"}`))
		w.Write([]byte(sb.String()))
	}))
	defer srv.Close()

	c := newAnthropicClient(&Resolved{Model: "claude-sonnet-5", BaseURL: srv.URL, APIKey: "test-key"})
	es, err := c.Stream(context.Background(), Request{Messages: []*messages.Message{NewTestUserMsg("hi")}})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer es.Close()

	var got strings.Builder
	done := false
	for es.Next() {
		ev := es.Current()
		switch ev.Type {
		case EventTextDelta:
			got.WriteString(ev.Text)
		case EventDone:
			done = true
		default:
			t.Errorf("unexpected event %q", ev.Type)
		}
	}
	if err := es.Err(); err != nil {
		t.Fatalf("stream err: %v", err)
	}
	if !done {
		t.Error("expected EventDone")
	}
	if got.String() != "Hello" {
		t.Errorf("text: got %q want %q", got.String(), "Hello")
	}
}

// TestAnthropicStreamToolUse 验证 tool_use 块转换。
func TestAnthropicStreamToolUse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		var sb strings.Builder
		sb.WriteString(anthropicSSE("message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}`))
		sb.WriteString(anthropicSSE("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"read_file","input":{}}}`))
		sb.WriteString(anthropicSSE("content_block_stop", `{"type":"content_block_stop","index":0}`))
		sb.WriteString(anthropicSSE("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":5}}`))
		sb.WriteString(anthropicSSE("message_stop", `{"type":"message_stop"}`))
		w.Write([]byte(sb.String()))
	}))
	defer srv.Close()

	c := newAnthropicClient(&Resolved{Model: "claude-sonnet-5", BaseURL: srv.URL, APIKey: "test-key"})
	es, err := c.Stream(context.Background(), Request{Messages: []*messages.Message{NewTestUserMsg("read it")}})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer es.Close()

	var call *messages.ToolCall
	for es.Next() {
		ev := es.Current()
		if ev.Type == EventToolCall {
			call = ev.ToolCall
		}
	}
	if call == nil {
		t.Fatal("expected a tool call event")
	}
	if call.Name != "read_file" || call.ID != "toolu_1" {
		t.Errorf("call: got %+v", call)
	}
	if string(call.Args) != "{}" {
		t.Errorf("args: got %s", call.Args)
	}
}

func NewTestUserMsg(s string) *messages.Message {
	return &messages.Message{Role: messages.RoleUser, Content: s}
}

// TestOpenAIStreamReasoning 验证 thinking 启用时，请求体按 OpenAI Responses
// 标准 reasoning 参数传递档位。
func TestOpenAIStreamReasoning(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.Header().Set("Content-Type", "text/event-stream")
		var sb strings.Builder
		sb.WriteString(sseEvent(`{"type":"response.created","sequence_number":1}`))
		sb.WriteString(sseEvent(`{"type":"response.completed","response":{"id":"resp_1"},"sequence_number":2}`))
		w.Write([]byte(sb.String()))
	}))
	defer srv.Close()

	c := newOpenAIClient(&Resolved{Model: "gpt-4o", BaseURL: srv.URL, APIKey: "test-key", ThinkingEnabled: true, ThinkingEffort: EffortHigh})
	es, err := c.Stream(context.Background(), Request{Messages: []*messages.Message{NewTestUserMsg("hi")}})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer es.Close()
	for es.Next() {
	}
	if es.Err() != nil {
		t.Fatalf("stream err: %v", es.Err())
	}
	reasoning, ok := body["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("missing reasoning param in body: %v", body)
	}
	if reasoning["effort"] != EffortHigh {
		t.Errorf("reasoning.effort: got %v want %q", reasoning["effort"], EffortHigh)
	}
}

// TestOpenAIStreamReasoningDisabled 验证 thinking 关闭时，请求体显式传
// reasoning.effort=none（DeepSeek 等默认开启的后端靠它真正关闭）。
func TestOpenAIStreamReasoningDisabled(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sseEvent(`{"type":"response.completed","response":{"id":"resp_1"},"sequence_number":1}`)))
	}))
	defer srv.Close()

	c := newOpenAIClient(&Resolved{Model: "gpt-4o", BaseURL: srv.URL, APIKey: "test-key", ThinkingEnabled: false})
	es, err := c.Stream(context.Background(), Request{Messages: []*messages.Message{NewTestUserMsg("hi")}})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer es.Close()
	for es.Next() {
	}
	reasoning, ok := body["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("missing reasoning param in body: %v", body)
	}
	if reasoning["effort"] != "none" {
		t.Errorf("reasoning.effort: got %v want none", reasoning["effort"])
	}
}

// TestAnthropicStreamThinking 验证 thinking 启用时，请求体携带 Anthropic
// 标准 thinking 参数与 SDK output_config.effort 档位。
func TestAnthropicStreamThinking(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.Header().Set("Content-Type", "text/event-stream")
		var sb strings.Builder
		sb.WriteString(anthropicSSE("message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}`))
		sb.WriteString(anthropicSSE("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`))
		sb.WriteString(anthropicSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`))
		sb.WriteString(anthropicSSE("content_block_stop", `{"type":"content_block_stop","index":0}`))
		sb.WriteString(anthropicSSE("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`))
		sb.WriteString(anthropicSSE("message_stop", `{"type":"message_stop"}`))
		w.Write([]byte(sb.String()))
	}))
	defer srv.Close()

	c := newAnthropicClient(&Resolved{Model: "claude-sonnet-5", BaseURL: srv.URL, APIKey: "test-key", ThinkingEnabled: true, ThinkingEffort: EffortMax})
	es, err := c.Stream(context.Background(), Request{Messages: []*messages.Message{NewTestUserMsg("hi")}})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer es.Close()
	for es.Next() {
	}
	if es.Err() != nil {
		t.Fatalf("stream err: %v", es.Err())
	}
	thinking, ok := body["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("missing thinking param in body: %v", body)
	}
	if thinking["type"] != "enabled" {
		t.Errorf("thinking.type: got %v want enabled", thinking["type"])
	}
	oc, ok := body["output_config"].(map[string]any)
	if !ok {
		t.Fatalf("missing output_config param in body: %v", body)
	}
	if oc["effort"] != EffortMax {
		t.Errorf("output_config.effort: got %v want %q", oc["effort"], EffortMax)
	}
}

// TestAnthropicStreamThinkingDisabled 验证 thinking 关闭时，请求体显式传
// thinking disabled（DeepSeek 等默认开启的后端靠它真正关闭），且无 output_config。
func TestAnthropicStreamThinkingDisabled(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.Header().Set("Content-Type", "text/event-stream")
		var sb strings.Builder
		sb.WriteString(anthropicSSE("message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}`))
		sb.WriteString(anthropicSSE("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`))
		sb.WriteString(anthropicSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`))
		sb.WriteString(anthropicSSE("content_block_stop", `{"type":"content_block_stop","index":0}`))
		sb.WriteString(anthropicSSE("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`))
		sb.WriteString(anthropicSSE("message_stop", `{"type":"message_stop"}`))
		w.Write([]byte(sb.String()))
	}))
	defer srv.Close()

	c := newAnthropicClient(&Resolved{Model: "claude-sonnet-5", BaseURL: srv.URL, APIKey: "test-key", ThinkingEnabled: false})
	es, err := c.Stream(context.Background(), Request{Messages: []*messages.Message{NewTestUserMsg("hi")}})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer es.Close()
	for es.Next() {
	}
	thinking, ok := body["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("missing thinking param in body: %v", body)
	}
	if thinking["type"] != "disabled" {
		t.Errorf("thinking.type: got %v want disabled", thinking["type"])
	}
	if _, ok := body["output_config"]; ok {
		t.Error("output_config param should be absent when thinking disabled")
	}
}
