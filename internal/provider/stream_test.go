package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agent-project/harness/internal/config"
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

	c := newAnthropicClient(&config.ProviderConfig{Model: "claude-sonnet-5", BaseURL: srv.URL, APIKey: "test-key"})
	es, err := c.Stream(context.Background(), Request{Model: "claude-sonnet-5", Messages: []*messages.Message{NewTestUserMsg("hi")}})
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

	c := newAnthropicClient(&config.ProviderConfig{Model: "claude-sonnet-5", BaseURL: srv.URL, APIKey: "test-key"})
	es, err := c.Stream(context.Background(), Request{Model: "claude-sonnet-5", Messages: []*messages.Message{NewTestUserMsg("read it")}})
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

	c := newAnthropicClient(&config.ProviderConfig{Model: "claude-sonnet-5", BaseURL: srv.URL, APIKey: "test-key"})
	es, err := c.Stream(context.Background(), Request{Model: "claude-sonnet-5", Messages: []*messages.Message{NewTestUserMsg("create")}})
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

	c := newAnthropicClient(&config.ProviderConfig{Model: "claude-sonnet-5", BaseURL: srv.URL, APIKey: "test-key", ThinkingEffort: config.EffortMax})
	es, err := c.Stream(context.Background(), Request{Model: "claude-sonnet-5", Messages: []*messages.Message{NewTestUserMsg("hi")}})
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
	if oc["effort"] != config.EffortMax {
		t.Errorf("output_config.effort: got %v want %q", oc["effort"], config.EffortMax)
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

	// client 默认 thinking 开启；per-call 覆盖关闭（Request.ThinkingEnabled，
	// /thinking、--no-thinking 写 AgentState → rc 的路径）。
	disabled := false
	c := newAnthropicClient(&config.ProviderConfig{Model: "claude-sonnet-5", BaseURL: srv.URL, APIKey: "test-key"})
	es, err := c.Stream(context.Background(), Request{Model: "claude-sonnet-5", Messages: []*messages.Message{NewTestUserMsg("hi")}, ThinkingEnabled: &disabled})
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

// TestAnthropicStreamThinkingDelta 验证 thinking_delta 流式文本转换为统一事件，
// 且 thinking 块签名从 content_block_start 捕获随 thinking_done 发出（ADR-025 修订）。
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

	c := newAnthropicClient(&config.ProviderConfig{Model: "claude-sonnet-5", BaseURL: srv.URL, APIKey: "test-key"})
	es, err := c.Stream(context.Background(), Request{Model: "claude-sonnet-5", Messages: []*messages.Message{NewTestUserMsg("hi")}})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer es.Close()

	var thinking strings.Builder
	var thinkingDone string
	var sig string
	for es.Next() {
		switch ev := es.Current(); ev.Type {
		case EventThinkingDelta:
			thinking.WriteString(ev.Text)
		case EventThinkingDone:
			// 块完成事件（ADR-025）：完整 thinking 块文本 + 签名（完整回传凭据）。
			thinkingDone = ev.Text
			sig = ev.Signature
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
	if sig != "sig_1" {
		t.Errorf("thinking_done signature: got %q want sig_1", sig)
	}
}

// TestAnthropicStreamSignatureDelta 验证 DeepSeek 真实流式行为：thinking 块在
// content_block_start 处签名是**空串**，签名经独立的 signature_delta 事件下发
// （content_block_stop 之前）——适配器须把它挂到 pendingBlock 随 thinking_done
// 发出（此前缺失该分支，签名全部丢弃，thinking 完整回传功能失效）。
func TestAnthropicStreamSignatureDelta(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		var sb strings.Builder
		sb.WriteString(anthropicSSE("message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}`))
		// DeepSeek 实测：start 处签名空串。
		sb.WriteString(anthropicSSE("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`))
		sb.WriteString(anthropicSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me think"}}`))
		sb.WriteString(anthropicSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig_deepseek_1"}}`))
		sb.WriteString(anthropicSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":" more"}}`))
		sb.WriteString(anthropicSSE("content_block_stop", `{"type":"content_block_stop","index":0}`))
		sb.WriteString(anthropicSSE("message_stop", `{"type":"message_stop"}`))
		w.Write([]byte(sb.String()))
	}))
	defer srv.Close()

	c := newAnthropicClient(&config.ProviderConfig{Model: "claude-sonnet-5", BaseURL: srv.URL, APIKey: "test-key"})
	es, err := c.Stream(context.Background(), Request{Model: "claude-sonnet-5", Messages: []*messages.Message{NewTestUserMsg("hi")}})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer es.Close()

	var thinkingDone string
	var sig string
	for es.Next() {
		switch ev := es.Current(); ev.Type {
		case EventThinkingDone:
			thinkingDone = ev.Text
			sig = ev.Signature
		}
	}
	if err := es.Err(); err != nil {
		t.Fatalf("stream err: %v", err)
	}
	if thinkingDone != "Let me think more" {
		t.Errorf("thinking_done: got %q", thinkingDone)
	}
	if sig != "sig_deepseek_1" {
		t.Errorf("signature_delta 未挂到 thinking 块：got %q want sig_deepseek_1", sig)
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

	// thinking 默认开启；Request 显式覆盖 enabled/effort/model（per-call 覆盖机制，ADR-026）。
	c := newAnthropicClient(&config.ProviderConfig{Model: "default-model", BaseURL: srv.URL, APIKey: "test-key"})
	enabled := true
	req := Request{
		Messages:        []*messages.Message{NewTestUserMsg("hi")},
		Model:           "override-model",
		ThinkingEnabled: &enabled,
		ThinkingEffort:  config.EffortMax,
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
	if oc["effort"] != config.EffortMax {
		t.Errorf("effort: got %v want %q", oc["effort"], config.EffortMax)
	}
	if _, ok := body["thinking"]; ok {
		t.Error("thinking 参数不应出现（开启时只传 output_config）")
	}
}

// TestToAnthropicMessagesSkipsThinkingOnly 验证 thinking-only 且无签名的 assistant
// 消息在重放时被跳过（thinking 无签名无法重放，内容为空无法表达为合法 anthropic
// 消息），前后 user 相邻。带签名则重放（见 anthropic_messages_test 的
// TestToAnthropicMessagesReplaysThinkingOnly，ADR-025 修订完整回传）。
func TestToAnthropicMessagesSkipsThinkingOnly(t *testing.T) {
	msgs := []*messages.Message{
		NewTestUserMsg("hi"),
		{Role: messages.RoleAssistant, Thinking: "deep", Content: ""}, // thinking-only 无签名
		NewTestUserMsg("继续"),
	}
	out := toAnthropicMessages(msgs)
	if len(out) != 2 {
		t.Fatalf("应跳过无签名的空 assistant，got %d", len(out))
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

// TestAnthropicStreamCapturesUsage 验证 message_start（input/cache）+ 最后
// message_delta（累计 output）合成 usage，随 EventDone 发出（ADR-037 用量展示）。
func TestAnthropicStreamCapturesUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		var sb strings.Builder
		sb.WriteString(anthropicSSE("message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1,"cache_read_input_tokens":3}}}`))
		sb.WriteString(anthropicSSE("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`))
		sb.WriteString(anthropicSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`))
		sb.WriteString(anthropicSSE("content_block_stop", `{"type":"content_block_stop","index":0}`))
		sb.WriteString(anthropicSSE("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":5}}`))
		sb.WriteString(anthropicSSE("message_stop", `{"type":"message_stop"}`))
		w.Write([]byte(sb.String()))
	}))
	defer srv.Close()

	c := newAnthropicClient(&config.ProviderConfig{Model: "claude-sonnet-5", BaseURL: srv.URL, APIKey: "test-key"})
	es, err := c.Stream(context.Background(), Request{Model: "claude-sonnet-5", Messages: []*messages.Message{NewTestUserMsg("hi")}})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer es.Close()

	var usage *messages.Usage
	done := false
	for es.Next() {
		ev := es.Current()
		if ev.Type == EventDone {
			done = true
			usage = ev.Usage
		}
	}
	if err := es.Err(); err != nil {
		t.Fatalf("stream err: %v", err)
	}
	if !done {
		t.Fatal("expected EventDone")
	}
	if usage == nil {
		t.Fatal("expected usage on EventDone")
	}
	if usage.InputTokens != 10 {
		t.Errorf("input_tokens: got %d want 10", usage.InputTokens)
	}
	if usage.OutputTokens != 5 {
		t.Errorf("output_tokens: got %d want 5（最后 message_delta 覆盖）", usage.OutputTokens)
	}
	if usage.CacheReadInputTokens != 3 {
		t.Errorf("cache_read_input_tokens: got %d want 3", usage.CacheReadInputTokens)
	}
}

// TestAnthropicStreamUsageNilWithoutUsage 验证无 usage 字段时 EventDone.Usage 为 nil。
func TestAnthropicStreamUsageNilWithoutUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(anthropicSSE("message_stop", `{"type":"message_stop"}`)))
	}))
	defer srv.Close()

	c := newAnthropicClient(&config.ProviderConfig{Model: "claude-sonnet-5", BaseURL: srv.URL, APIKey: "test-key"})
	es, err := c.Stream(context.Background(), Request{Model: "claude-sonnet-5", Messages: []*messages.Message{NewTestUserMsg("hi")}})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer es.Close()

	for es.Next() {
		if ev := es.Current(); ev.Type == EventDone && ev.Usage != nil {
			t.Error("无 usage 时 EventDone.Usage 应为 nil")
		}
	}

	if err := es.Err(); err != nil {
		t.Fatalf("stream err: %v", err)
	}
}

// TestAnthropicStreamSamplingParams 验证模型级采样参数 top_p/temperature 注入
// 请求体（评测协议对齐 top_p=0.95 / temperature=1.0，2026-08-19）；未配置时
// 请求不携带。
func TestAnthropicStreamSamplingParams(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(anthropicSSE("message_stop", `{"type":"message_stop"}`)))
	}))
	defer srv.Close()

	// 配置了 top_p/temperature：请求体应携带。
	c := newAnthropicClient(&config.ProviderConfig{
		Model: "m", BaseURL: srv.URL, APIKey: "test-key",
		TopP: 0.95, Temperature: 1.0,
	})
	es, err := c.Stream(context.Background(), Request{Model: "m", Messages: []*messages.Message{NewTestUserMsg("hi")}})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	_ = es.Close()
	if gotBody["top_p"] != 0.95 {
		t.Errorf("top_p: got %v want 0.95", gotBody["top_p"])
	}
	if gotBody["temperature"] != 1.0 {
		t.Errorf("temperature: got %v want 1.0", gotBody["temperature"])
	}

	// 未配置：请求体不应携带。
	var gotBody2 map[string]any
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody2)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(anthropicSSE("message_stop", `{"type":"message_stop"}`)))
	}))
	defer srv2.Close()
	c2 := newAnthropicClient(&config.ProviderConfig{Model: "m", BaseURL: srv2.URL, APIKey: "test-key"})
	es2, err := c2.Stream(context.Background(), Request{Model: "m", Messages: []*messages.Message{NewTestUserMsg("hi")}})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	_ = es2.Close()
	if _, ok := gotBody2["top_p"]; ok {
		t.Errorf("未配置 top_p 不应携带: %v", gotBody2["top_p"])
	}
	if _, ok := gotBody2["temperature"]; ok {
		t.Errorf("未配置 temperature 不应携带: %v", gotBody2["temperature"])
	}
}
