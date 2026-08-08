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

// anthropicSSE 格式化 Anthropic 风格 SSE 事件：SDK 按 `event:` 字段路由，
// 并从 JSON 的 `type` 字段填充 ev.Type，因此两者都必须存在
// （与真实 API 一致）。
func anthropicSSE(eventType, data string) string {
	return "event: " + eventType + "\ndata: " + data + "\n\n"
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
	var doneText string
	done := false
	for es.Next() {
		ev := es.Current()
		switch ev.Type {
		case EventTextDelta:
			got.WriteString(ev.Text)
		case EventTextDone:
			// 块完成事件（ADR-025）：完整块文本，供持久化订阅。
			doneText = ev.Text
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
	if doneText != "Hello" {
		t.Errorf("text_done: got %q want %q", doneText, "Hello")
	}
}

// TestAnthropicStreamToolUse 验证 tool_use 块转换（start 已带全参数）。
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

// TestAnthropicStreamToolCallStreamingArgs 验证工具参数经 input_json_delta
// 分片累积（真实 API 行为：content_block_start 时 input 为空，参数后续流式）。
func TestAnthropicStreamToolCallStreamingArgs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		var sb strings.Builder
		sb.WriteString(anthropicSSE("message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}`))
		sb.WriteString(anthropicSSE("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"apply_patch","input":{}}}`))
		sb.WriteString(anthropicSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"pat"}}`))
		sb.WriteString(anthropicSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"ch\":\"simple\"}"}}`))
		sb.WriteString(anthropicSSE("content_block_stop", `{"type":"content_block_stop","index":0}`))
		sb.WriteString(anthropicSSE("message_stop", `{"type":"message_stop"}`))
		w.Write([]byte(sb.String()))
	}))
	defer srv.Close()

	c := newAnthropicClient(&Resolved{Model: "claude-sonnet-5", BaseURL: srv.URL, APIKey: "test-key"})
	es, err := c.Stream(context.Background(), Request{Messages: []*messages.Message{NewTestUserMsg("create")}})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer es.Close()

	var call *messages.ToolCall
	for es.Next() {
		if ev := es.Current(); ev.Type == EventToolCall {
			call = ev.ToolCall
		}
	}
	if err := es.Err(); err != nil {
		t.Fatalf("stream err: %v", err)
	}
	if call == nil {
		t.Fatal("expected a tool call")
	}
	if call.Name != "apply_patch" || call.ID != "toolu_1" {
		t.Errorf("call: %+v", call)
	}
	// 两个分片累积成完整参数 JSON。
	if string(call.Args) != `{"patch":"simple"}` {
		t.Errorf("args: got %s", call.Args)
	}
}

// TestAnthropicStreamThinking 验证 thinking 启用时，请求体**不传** thinking
// 参数（DeepSeek 等兼容端点默认开启 thinking，传小 budget 反而截断），只带
// output_config.effort 档位。
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
	// 默认不传 thinking 参数（DeepSeek 等兼容端点默认开启 thinking；传小的
	// budget_tokens 反而截断，见 defaultMaxTokens 注释）。
	if _, ok := body["thinking"]; ok {
		t.Errorf("默认不应传 thinking 参数: %v", body["thinking"])
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

// TestAnthropicStreamThinkingDelta 验证 thinking_delta 流式文本转换为统一事件。
func TestAnthropicStreamThinkingDelta(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		var sb strings.Builder
		sb.WriteString(anthropicSSE("message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}`))
		sb.WriteString(anthropicSSE("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":"sig_1"}}`))
		sb.WriteString(anthropicSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me think"}}`))
		sb.WriteString(anthropicSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":" more"}}`))
		sb.WriteString(anthropicSSE("content_block_stop", `{"type":"content_block_stop","index":0}`))
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

	var thinking strings.Builder
	var thinkingDone string
	for es.Next() {
		switch ev := es.Current(); ev.Type {
		case EventThinkingDelta:
			thinking.WriteString(ev.Text)
		case EventThinkingDone:
			// 块完成事件（ADR-025）：完整 thinking 块文本，供持久化订阅。
			thinkingDone = ev.Text
		}
	}
	if err := es.Err(); err != nil {
		t.Fatalf("stream err: %v", err)
	}
	if thinking.String() != "Let me think more" {
		t.Errorf("thinking: got %q", thinking.String())
	}
	if thinkingDone != "Let me think more" {
		t.Errorf("thinking_done: got %q want %q", thinkingDone, "Let me think more")
	}
}

// TestAnthropicRequestOverrides 验证 per-call 覆盖（ADR-026）：Request.Model /
// ThinkingEnabled / ThinkingEffort 优先于 client 配置默认（运行时模型/档位切换）。
func TestAnthropicRequestOverrides(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.Header().Set("Content-Type", "text/event-stream")
		var sb strings.Builder
		sb.WriteString(anthropicSSE("message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}`))
		sb.WriteString(anthropicSSE("message_stop", `{"type":"message_stop"}`))
		w.Write([]byte(sb.String()))
	}))
	defer srv.Close()

	// client 默认 thinking 关闭；Request 覆盖为开启 + effort max + 换模型。
	c := newAnthropicClient(&Resolved{Model: "default-model", BaseURL: srv.URL, APIKey: "test-key", ThinkingEnabled: false})
	enabled := true
	req := Request{
		Messages:        []*messages.Message{NewTestUserMsg("hi")},
		Model:           "override-model",
		ThinkingEnabled: &enabled,
		ThinkingEffort:  EffortMax,
	}
	es, err := c.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	for es.Next() {
	}
	if es.Err() != nil {
		t.Fatalf("stream err: %v", es.Err())
	}
	if body["model"] != "override-model" {
		t.Errorf("model: got %v", body["model"])
	}
	oc, ok := body["output_config"].(map[string]any)
	if !ok {
		t.Fatalf("missing output_config: %v", body)
	}
	if oc["effort"] != EffortMax {
		t.Errorf("effort: got %v want %q", oc["effort"], EffortMax)
	}
	if _, ok := body["thinking"]; ok {
		t.Error("thinking 参数不应出现（开启时只传 output_config）")
	}
}

// TestToAnthropicMessagesSkipsThinkingOnly 验证 thinking-only 的 assistant 消息
// 在重放时被跳过（内容为空无法表达为合法 anthropic 消息），前后 user 相邻。
func TestToAnthropicMessagesSkipsThinkingOnly(t *testing.T) {
	msgs := []*messages.Message{
		NewTestUserMsg("hi"),
		{Role: messages.RoleAssistant, Thinking: "deep", Content: ""}, // thinking-only，无 text 无 tool_calls
		NewTestUserMsg("继续"),
	}
	out := toAnthropicMessages(msgs)
	if len(out) != 2 {
		t.Fatalf("应跳过空 assistant，got %d", len(out))
	}
	for i, pm := range out {
		if string(pm.Role) != "user" {
			t.Errorf("消息 %d 应为 user（跳过后 user 相邻）: %s", i, pm.Role)
		}
	}
	// 正常 assistant 不受影响。
	msgs[1].Content = "ok"
	out2 := toAnthropicMessages(msgs)
	if len(out2) != 3 {
		t.Fatalf("正常 assistant 不应跳过，got %d", len(out2))
	}
}
