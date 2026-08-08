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
func (f *fakeTool) Handle(ctx context.Context, _ *middleware.RuntimeContext, _ string, args json.RawMessage) (messages.ToolResult, error) {
	return f.handle(ctx, args)
}

// textStream 构造一个"文本 → done"的事件流。每个文本段先发流式 delta
// （渲染），再发块完成 done（agent 用其组装 assistant Content，ADR-025）。
func textStream(parts ...string) provider.EventStream {
	var evs []provider.Event
	for _, p := range parts {
		evs = append(evs, provider.Event{Type: provider.EventTextDelta, Text: p})
		evs = append(evs, provider.Event{Type: provider.EventTextDone, Text: p})
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

// rcFor 构造带消息序列的 per-call 上下文（无状态 agent Run 测试用，ADR-026）。
func rcFor(th *messages.Thread) *middleware.RuntimeContext {
	rc := middleware.NewRuntimeContext()
	rc.Messages = th
	return rc
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

	if err := a.Run(context.Background(), rcFor(th), rec.on); err != nil {
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
	if err := a.Run(context.Background(), rcFor(th), rec.on); err != nil {
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
	if err := a.Run(context.Background(), rcFor(th), rec.on); err != nil {
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
	if err := a.Run(context.Background(), rcFor(th), rec.on); err != nil {
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
	err := a.Run(context.Background(), rcFor(th), rec.on)
	if err == nil || !strings.Contains(err.Error(), "fatal") {
		t.Fatalf("期望 Fatal 错误终止，got %v", err)
	}
	if rec.has(EventTurnDone) {
		t.Error("Fatal 后不应有 turn_done")
	}
}

// TestRunThinkingDelta 验证 thinking/text 增量透传事件（渲染），且块完成
// 事件（thinking_done/text_done）驱动 assistant 消息的 Content/Thinking 组装（ADR-025）。
func TestRunThinkingDelta(t *testing.T) {
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return provider.NewFakeStream([]provider.Event{
			{Type: provider.EventThinkingDelta, Text: "Let me think"},
			{Type: provider.EventThinkingDone, Text: "Let me think"},
			{Type: provider.EventTextDelta, Text: "answer"},
			{Type: provider.EventTextDone, Text: "answer"},
			{Type: provider.EventDone},
		}), nil
	}}
	a := noToolsAgent(fc)
	rec := &eventRecorder{}
	th := newThread()
	if err := a.Run(context.Background(), rcFor(th), rec.on); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 块完成事件透传（持久化订阅用）。
	gotThinkingDone, gotTextDone := false, false
	for _, e := range rec.events {
		if e.Type == EventThinkingDelta && e.Text == "Let me think" {
			gotThinkingDone = true
		}
		if e.Type == EventThinkingDone && e.Text == "Let me think" {
			gotThinkingDone = true
		}
		if e.Type == EventTextDone && e.Text == "answer" {
			gotTextDone = true
		}
	}
	if !gotThinkingDone {
		t.Errorf("thinking_done 事件缺失: %v", rec.types())
	}
	if !gotTextDone {
		t.Errorf("text_done 事件缺失: %v", rec.types())
	}
	// assistant 消息组装：Content=text、Thinking=thinking（存审计，重放剥离）。
	var asst *messages.Message
	for _, m := range th.Messages {
		if m.Role == messages.RoleAssistant {
			asst = m
		}
	}
	if asst == nil {
		t.Fatal("无 assistant 消息")
	}
	if asst.Content != "answer" || asst.Thinking != "Let me think" {
		t.Errorf("assistant 组装: content=%q thinking=%q", asst.Content, asst.Thinking)
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

	if err := a.Run(context.Background(), rcFor(newThread()), nil); err != nil {
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

// agentSpy 覆写 OnAgent，记录洋葱调用（验证 onAgent 包裹整个回合）。
type agentSpy struct {
	middleware.Base
	seq *[]string
}

func (s *agentSpy) OnAgent(ctx context.Context, rc *middleware.RuntimeContext, in middleware.AgentInput, next middleware.AgentHandler) error {
	*s.seq = append(*s.seq, "onAgent:before")
	err := next(ctx, rc, in)
	*s.seq = append(*s.seq, "onAgent:after")
	return err
}

// TestRunOnAgentWrapsTurn 验证 onAgent 是回合最外层：
// before 先于 turn_start、after 后于 turn_done（与事件打进同一序列对比）。
func TestRunOnAgentWrapsTurn(t *testing.T) {
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return textStream("ok"), nil
	}}
	a := New(fc, "m")
	a.SetTools(tools.NewRegistry())
	var seq []string
	a.SetMiddleware(middleware.NewChain(&agentSpy{seq: &seq}))

	if err := a.Run(context.Background(), rcFor(newThread()), func(e Event) { seq = append(seq, string(e.Type)) }); err != nil {
		t.Fatalf("Run: %v", err)
	}
	bi, ti := indexOf(seq, "onAgent:before"), indexOf(seq, "turn_start")
	di, ai := indexOf(seq, "turn_done"), indexOf(seq, "onAgent:after")
	if !(bi >= 0 && ti >= 0 && di >= 0 && ai >= 0 && bi < ti && di < ai) {
		t.Fatalf("onAgent 未包裹回合（期望 before<turn_start 且 turn_done<after）: %v", seq)
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
	err := a.Run(context.Background(), rcFor(newThread()), rec.on)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("期望 boom 错误，got %v", err)
	}
	if !rec.has(EventError) {
		t.Error("应发出 EventError")
	}
}

// TestRunModelFromRC 验证 per-call 覆盖（ADR-026）：rc.Model / rc.ThinkingEffort /
// rc.ThinkingEnabled 写入 provider.Request（未设置时用 agent 默认）。
func TestRunModelFromRC(t *testing.T) {
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return textStream("ok"), nil
	}}
	a := New(fc, "default-model") // agent 默认模型
	rc := rcFor(newThread())
	rc.Model = "other-model"
	rc.ThinkingEffort = provider.EffortMax
	enabled := false
	rc.ThinkingEnabled = &enabled

	if err := a.Run(context.Background(), rc, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fc.LastReq == nil {
		t.Fatal("no request")
	}
	if fc.LastReq.Model != "other-model" {
		t.Errorf("Request.Model: got %q want other-model", fc.LastReq.Model)
	}
	if fc.LastReq.ThinkingEffort != provider.EffortMax {
		t.Errorf("Request.ThinkingEffort: got %q", fc.LastReq.ThinkingEffort)
	}
	if fc.LastReq.ThinkingEnabled == nil || *fc.LastReq.ThinkingEnabled {
		t.Errorf("Request.ThinkingEnabled: got %v want false", fc.LastReq.ThinkingEnabled)
	}
}

// TestRunOnModelCallReceivesRC 验证 onModelCall 中间件收到真实 rc（rc-drop 修复）：
// 此前 sample 内 wrapped(ctx, nil, ...) 导致 model 层拿 nil rc。
func TestRunOnModelCallReceivesRC(t *testing.T) {
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return textStream("ok"), nil
	}}
	got := make(chan *middleware.RuntimeContext, 1)
	mw := &modelCallSpy{got: got}
	a := New(fc, "m")
	a.SetTools(tools.NewRegistry())
	a.SetMiddleware(middleware.NewChain(mw))

	rc := rcFor(newThread())
	if err := a.Run(context.Background(), rc, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	seen := <-got
	if seen == nil {
		t.Fatal("onModelCall 收到 nil rc")
	}
	if seen != rc {
		t.Error("onModelCall 收到的 rc 与 Run 传入不一致")
	}
}

// modelCallSpy 记录 OnModelCall 收到的 rc。
type modelCallSpy struct {
	middleware.Base
	got chan *middleware.RuntimeContext
}

func (m *modelCallSpy) OnModelCall(ctx context.Context, rc *middleware.RuntimeContext, in middleware.ModelCallInput, next middleware.ModelCallHandler) error {
	m.got <- rc
	return next(ctx, rc, in)
}

func indexOf(ss []string, s string) int {
	for i, v := range ss {
		if v == s {
			return i
		}
	}
	return -1
}
