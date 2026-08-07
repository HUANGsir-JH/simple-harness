package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/provider"
	"github.com/agent-project/harness/internal/tools"
)

// --- 测试替身 ---------------------------------------------------------------

// fakeTool 是 agent 测试用的脚本化工具。
type fakeTool struct {
	name   string
	handle func(ctx context.Context, args json.RawMessage) (messages.ToolResult, error)
}

func (f *fakeTool) Name() string { return f.name }
func (f *fakeTool) Spec() provider.ToolSpec {
	return provider.ToolSpec{Name: f.name, Description: "fake", Parameters: json.RawMessage(`{"type":"object"}`)}
}
func (f *fakeTool) Handle(ctx context.Context, _ string, args json.RawMessage) (messages.ToolResult, error) {
	return f.handle(ctx, args)
}

// textStream 构造一个"文本 → done"的事件流。
func textStream(parts ...string) provider.EventStream {
	var evs []provider.Event
	for _, p := range parts {
		evs = append(evs, provider.Event{Type: provider.EventTextDelta, Text: p})
	}
	evs = append(evs, provider.Event{Type: provider.EventDone})
	return provider.NewFakeStream(evs)
}

// toolCallStream 构造一个"工具调用 → done"的事件流。
func toolCallStream(name, args string) provider.EventStream {
	return provider.NewFakeStream([]provider.Event{
		{Type: provider.EventToolCall, ToolCall: &messages.ToolCall{ID: "c1", Name: name, Args: json.RawMessage(args)}},
		{Type: provider.EventDone},
	})
}

// eventRecorder 记录回合事件序列。
type eventRecorder struct {
	events []Event
}

func (r *eventRecorder) on(e Event) { r.events = append(r.events, e) }
func (r *eventRecorder) types() []EventType {
	out := make([]EventType, 0, len(r.events))
	for _, e := range r.events {
		out = append(out, e.Type)
	}
	return out
}
func (r *eventRecorder) has(et EventType) bool {
	for _, e := range r.events {
		if e.Type == et {
			return true
		}
	}
	return false
}

func newThread() *messages.Thread {
	th := messages.NewThread()
	th.Add(messages.NewUserMessage("hi"))
	return th
}

func noToolsAgent(fc *provider.FakeClient) *Agent {
	a := New(fc, "m")
	a.SetTools(tools.NewRegistry())
	return a
}

// --- 测试 -------------------------------------------------------------------

// TestRunSingleTurn 验证无工具单轮：turn_start → text → turn_done，assistant 入 thread。
func TestRunSingleTurn(t *testing.T) {
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return textStream("Hel", "lo"), nil
	}}
	a := noToolsAgent(fc)
	th := newThread()
	rec := &eventRecorder{}

	if err := a.Run(context.Background(), nil, th, rec.on); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rec.events[0].Type != EventTurnStart || rec.events[len(rec.events)-1].Type != EventTurnDone {
		t.Errorf("边界事件: got %v", rec.types())
	}
	if len(th.Messages) != 2 || th.Messages[1].Role != messages.RoleAssistant {
		t.Fatalf("thread: got %d messages", len(th.Messages))
	}
	if th.Messages[1].Content != "Hello" {
		t.Errorf("assistant content: got %q", th.Messages[1].Content)
	}
	// 请求携带 thread 消息。
	if fc.LastReq == nil || len(fc.LastReq.Messages) != 1 {
		t.Fatalf("request messages: %+v", fc.LastReq)
	}
}

// TestRunToolLoop 验证工具闭环：首轮 tool_call → 执行回填 → 次轮文本 → turn_done。
func TestRunToolLoop(t *testing.T) {
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		if len(req.Messages) == 1 { // 首轮仅 user
			return toolCallStream("read_file", `{"path":"a.txt"}`), nil
		}
		return textStream("done"), nil
	}}
	reg := tools.NewRegistry()
	reg.Register(&fakeTool{name: "read_file", handle: func(ctx context.Context, args json.RawMessage) (messages.ToolResult, error) {
		return messages.ToolResult{Success: true, Content: "file content"}, nil
	}})
	a := New(fc, "m")
	a.SetTools(reg)

	th := newThread()
	rec := &eventRecorder{}
	if err := a.Run(context.Background(), nil, th, rec.on); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 事件含工具与回合边界。
	if !rec.has(EventToolCall) || !rec.has(EventToolResult) || !rec.has(EventTurnDone) {
		t.Errorf("事件缺失: %v", rec.types())
	}
	// thread：user, assistant(tool_calls), tool_result, assistant(text)
	if len(th.Messages) != 4 {
		t.Fatalf("thread: got %d messages", len(th.Messages))
	}
	tr := th.Messages[2]
	if tr.Role != messages.RoleTool || len(tr.ToolResults) != 1 {
		t.Fatalf("tool result 消息: %+v", tr)
	}
	if tr.ToolResults[0].ToolCallID != "c1" || !strings.Contains(tr.ToolResults[0].Content, "file content") {
		t.Errorf("tool result: %+v", tr.ToolResults)
	}
	// 第二轮采样请求带上了 assistant + tool result。
	if len(fc.LastReq.Messages) != 3 {
		t.Errorf("第二轮请求消息数: %d", len(fc.LastReq.Messages))
	}
}

// TestRunParallelToolCalls 验证多个工具调用并发执行且结果都回填。
func TestRunParallelToolCalls(t *testing.T) {
	var cur, max int32
	reg := tools.NewRegistry()
	mk := func(name string) tools.Tool {
		return &fakeTool{name: name, handle: func(ctx context.Context, args json.RawMessage) (messages.ToolResult, error) {
			c := atomic.AddInt32(&cur, 1)
			for {
				m := atomic.LoadInt32(&max)
				if c <= m || atomic.CompareAndSwapInt32(&max, m, c) {
					break
				}
			}
			time.Sleep(30 * time.Millisecond)
			atomic.AddInt32(&cur, -1)
			return messages.ToolResult{Success: true, Content: name + ":" + string(args)}, nil
		}}
	}
	reg.Register(mk("read_file"))
	reg.Register(mk("glob"))

	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		if len(req.Messages) == 1 {
			return provider.NewFakeStream([]provider.Event{
				{Type: provider.EventToolCall, ToolCall: &messages.ToolCall{ID: "c1", Name: "read_file", Args: json.RawMessage(`{}`)}},
				{Type: provider.EventToolCall, ToolCall: &messages.ToolCall{ID: "c2", Name: "glob", Args: json.RawMessage(`{}`)}},
				{Type: provider.EventDone},
			}), nil
		}
		return textStream("ok"), nil
	}}
	a := New(fc, "m")
	a.SetTools(reg)
	th := newThread()
	rec := &eventRecorder{}
	if err := a.Run(context.Background(), nil, th, rec.on); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if max < 2 {
		t.Errorf("期望至少 2 个工具并发执行，峰值 %d", max)
	}
	// user, assistant, tool_result（合并 2 块）, assistant
	if len(th.Messages) != 4 {
		t.Errorf("thread 消息数: %d", len(th.Messages))
	}
	tr := th.Messages[2]
	if tr.Role != messages.RoleTool || len(tr.ToolResults) != 2 {
		t.Errorf("tool result 应合并 2 块: %+v", tr.ToolResults)
	}
}

// TestRunToolRespondToModel 验证工具可恢复错误：结果回填失败、循环继续。
func TestRunToolRespondToModel(t *testing.T) {
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		if len(req.Messages) == 1 {
			return toolCallStream("boom", `{}`), nil
		}
		return textStream("retried"), nil
	}}
	reg := tools.NewRegistry()
	reg.Register(&fakeTool{name: "boom", handle: func(ctx context.Context, args json.RawMessage) (messages.ToolResult, error) {
		return messages.ToolResult{}, &tools.ToolError{RespondToModel: true, Message: "recoverable"}
	}})
	a := New(fc, "m")
	a.SetTools(reg)
	th := newThread()
	rec := &eventRecorder{}
	if err := a.Run(context.Background(), nil, th, rec.on); err != nil {
		t.Fatalf("Run: %v", err)
	}
	tr := th.Messages[2]
	if tr.Role != messages.RoleTool || len(tr.ToolResults) != 1 || tr.ToolResults[0].Success {
		t.Errorf("tool result 应标记失败: %+v", tr.ToolResults)
	}
	if !strings.Contains(tr.ToolResults[0].Content, "recoverable") {
		t.Errorf("tool result content: %q", tr.ToolResults[0].Content)
	}
	if !rec.has(EventTurnDone) {
		t.Error("RespondToModel 错误后回合应继续到 turn_done")
	}
}

// TestRunToolFatal 验证工具 Fatal 错误终止回合。
func TestRunToolFatal(t *testing.T) {
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return toolCallStream("boom", `{}`), nil
	}}
	reg := tools.NewRegistry()
	reg.Register(&fakeTool{name: "boom", handle: func(ctx context.Context, args json.RawMessage) (messages.ToolResult, error) {
		return messages.ToolResult{}, &tools.ToolError{RespondToModel: false, Message: "fatal"}
	}})
	a := New(fc, "m")
	a.SetTools(reg)
	th := newThread()
	rec := &eventRecorder{}
	err := a.Run(context.Background(), nil, th, rec.on)
	if err == nil || !strings.Contains(err.Error(), "fatal") {
		t.Fatalf("期望 Fatal 错误终止，got %v", err)
	}
	if rec.has(EventTurnDone) {
		t.Error("Fatal 后不应有 turn_done")
	}
}

// TestRunThinkingDelta 验证 thinking 增量透传为事件。
func TestRunThinkingDelta(t *testing.T) {
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return provider.NewFakeStream([]provider.Event{
			{Type: provider.EventThinkingDelta, Text: "Let me think"},
			{Type: provider.EventTextDelta, Text: "answer"},
			{Type: provider.EventDone},
		}), nil
	}}
	a := noToolsAgent(fc)
	rec := &eventRecorder{}
	if err := a.Run(context.Background(), nil, newThread(), rec.on); err != nil {
		t.Fatalf("Run: %v", err)
	}
	found := false
	for _, e := range rec.events {
		if e.Type == EventThinkingDelta && e.Text == "Let me think" {
			found = true
		}
	}
	if !found {
		t.Errorf("thinking 事件缺失: %v", rec.types())
	}
}

// middlewareRecorder 验证工具循环中 middleware 链被调用。
type middlewareRecorder struct {
	middleware.Base
	calls *[]string
}

func (m middlewareRecorder) OnToolCall(ctx context.Context, rc *middleware.RuntimeContext, in middleware.ToolCallInput, next func(context.Context, *middleware.RuntimeContext, middleware.ToolCallInput) error) error {
	*m.calls = append(*m.calls, "onToolCall:before")
	err := next(ctx, rc, in)
	*m.calls = append(*m.calls, "onToolCall:after")
	return err
}

func (m middlewareRecorder) OnActing(ctx context.Context, rc *middleware.RuntimeContext, in middleware.ActingInput, next func(context.Context, *middleware.RuntimeContext, middleware.ActingInput) error) error {
	*m.calls = append(*m.calls, "onActing:before:"+in.Call.Name)
	err := next(ctx, rc, in)
	*m.calls = append(*m.calls, "onActing:after:"+in.Call.Name)
	return err
}

// TestRunMiddlewareChain 验证 onToolCall/onActing 在工具循环中按层次调用。
func TestRunMiddlewareChain(t *testing.T) {
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		if len(req.Messages) == 1 {
			return toolCallStream("read_file", `{}`), nil
		}
		return textStream("ok"), nil
	}}
	reg := tools.NewRegistry()
	reg.Register(&fakeTool{name: "read_file", handle: func(ctx context.Context, args json.RawMessage) (messages.ToolResult, error) {
		return messages.ToolResult{Success: true, Content: "x"}, nil
	}})
	var calls []string
	a := New(fc, "m")
	a.SetTools(reg)
	a.SetMiddleware(middleware.NewChain(middlewareRecorder{calls: &calls}))

	if err := a.Run(context.Background(), nil, newThread(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	joined := strings.Join(calls, ",")
	if !strings.Contains(joined, "onToolCall:before") || !strings.Contains(joined, "onToolCall:after") {
		t.Errorf("onToolCall 未按洋葱调用: %v", calls)
	}
	if !strings.Contains(joined, "onActing:before:read_file") || !strings.Contains(joined, "onActing:after:read_file") {
		t.Errorf("onActing 未按洋葱调用: %v", calls)
	}
	// onActing 应嵌套在 onToolCall 内。
	ti, ai := indexOf(calls, "onToolCall:before"), indexOf(calls, "onActing:before:read_file")
	te, ae := indexOf(calls, "onToolCall:after"), indexOf(calls, "onActing:after:read_file")
	if ti < 0 || ai < 0 || te < 0 || ae < 0 || !(ti < ai && ae < te) {
		t.Errorf("层次错误: %v", calls)
	}
}

// TestRunStreamError 验证 provider 流错误传播。
func TestRunStreamError(t *testing.T) {
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return provider.NewFakeStream([]provider.Event{
			{Type: provider.EventTextDelta, Text: "partial"},
			{Type: provider.EventError, Error: errors.New("boom")},
		}), nil
	}}
	a := noToolsAgent(fc)
	rec := &eventRecorder{}
	err := a.Run(context.Background(), nil, newThread(), rec.on)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("期望 boom 错误，got %v", err)
	}
	if !rec.has(EventError) {
		t.Error("应发出 EventError")
	}
}

func indexOf(ss []string, s string) int {
	for i, v := range ss {
		if v == s {
			return i
		}
	}
	return -1
}
