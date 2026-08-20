package agent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/compact"
	"github.com/agent-project/harness/internal/config"
	"github.com/agent-project/harness/internal/events"
	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/middleware/impl"
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

// eventRecorder 记录回合事件序列。runToolBatch 并行工具 goroutine 同时 emit
// events.EventToolResult（ADR-024），on 须加锁（写-写 race）。
type eventRecorder struct {
	mu     sync.Mutex
	events []events.Event
}

func (r *eventRecorder) on(e events.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}
func (r *eventRecorder) types() []events.EventType {
	out := make([]events.EventType, 0, len(r.events))
	for _, e := range r.events {
		out = append(out, e.Type)
	}
	return out
}
func (r *eventRecorder) has(et events.EventType) bool {
	for _, e := range r.events {
		if e.Type == et {
			return true
		}
	}
	return false
}

// indexOf 返回事件类型首次出现的序号（-1 = 未出现）。时序断言用：
// start 事件须先于完成事件。
func (r *eventRecorder) indexOf(et events.EventType) int {
	for i, e := range r.events {
		if e.Type == et {
			return i
		}
	}
	return -1
}

func newConversation() *messages.Conversation {
	conv := messages.NewConversation()
	conv.Add(messages.NewUserMessage("hi"))
	return conv
}

// rcFor 构造带消息序列的 per-call 上下文（无状态 agent Run 测试用，ADR-026）。
func rcFor(conv *messages.Conversation) *middleware.RuntimeContext {
	rc := middleware.NewRuntimeContext()
	rc.Messages = conv
	return rc
}

func noToolsAgent(fc *provider.FakeClient) *Agent {
	a := New(fc, "m")
	a.SetTools(tools.NewRegistry())
	return a
}

// --- 测试 -------------------------------------------------------------------

// TestRunSingleTurn 验证无工具单轮：turn_start → text → turn_done，assistant 入 conversation。
func TestRunSingleTurn(t *testing.T) {
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return textStream("Hel", "lo"), nil
	}}
	a := noToolsAgent(fc)
	conv := newConversation()
	rec := &eventRecorder{}

	if err := a.Run(context.Background(), rcFor(conv), rec.on); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rec.events[0].Type != events.EventTurnStart || rec.events[len(rec.events)-1].Type != events.EventTurnDone {
		t.Errorf("边界事件: got %v", rec.types())
	}
	if len(conv.Messages) != 2 || conv.Messages[1].Role != messages.RoleAssistant {
		t.Fatalf("conversation: got %d messages", len(conv.Messages))
	}
	if conv.Messages[1].Content != "Hello" {
		t.Errorf("assistant content: got %q", conv.Messages[1].Content)
	}
	// 请求携带 conversation 消息。
	if fc.LastReq == nil || len(fc.LastReq.Messages) != 1 {
		t.Fatalf("request messages: %+v", fc.LastReq)
	}
}

// TestBuildBaseInstructionsOverride 验证 BuildOptions.BaseInstructions 覆盖链首
// 基础提示词（评测 config.base_instructions 透传；空 = 默认 persona，
// 2026-08-20）。
func TestBuildBaseInstructionsOverride(t *testing.T) {
	run := func(base string) string {
		var instructions string
		fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
			instructions = req.Instructions
			return textStream("ok"), nil
		}}
		a, err := Build(BuildOptions{
			Provider:         &config.ProviderConfig{Model: "m1", ContextWindow: 200_000},
			Client:           fc,
			BaseInstructions: base,
		})
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		rc := rcFor(newConversation())
		if err := a.Run(context.Background(), rc, func(events.Event) {}); err != nil {
			t.Fatalf("Run: %v", err)
		}
		return instructions
	}

	// 覆盖：链首 persona 换成自定义文本（默认中文 persona 不出现）。
	got := run("You are a helpful software engineer assistant.")
	if !strings.HasPrefix(got, "You are a helpful software engineer assistant.") {
		t.Errorf("覆盖 persona 应在系统提示链首，got prefix: %q", got[:min(len(got), 80)])
	}
	if strings.Contains(got, "运行在用户终端") {
		t.Error("覆盖后不应含默认 persona")
	}

	// 空值：回退默认 persona（含"运行在用户终端"）。
	def := run("")
	if !strings.Contains(def, "运行在用户终端") {
		t.Error("空 BaseInstructions 应回退默认 persona")
	}
}

// suffixMiddleware 是测试用 onSystemPrompt 中间件（尾部追加标记，验证正序流水线）。
type suffixMiddleware struct {
	middleware.Base
	text string
}

func (m suffixMiddleware) OnSystemPrompt(_ context.Context, _ *middleware.RuntimeContext, current string) (string, error) {
	return current + m.text, nil
}

// TestRunSystemPromptCompose 验证系统提示组合（ADR-037 修订 2026-08-13）：
// 起点 = rc.SystemPrompt（调用方 per-call 贡献）经 onSystemPrompt 管道组合
// （基础提示词由链首 BaseInstructionsMiddleware 注入，agent 不携带提示词文本），
// 组合结果回写 rc.SystemPrompt（compact 兜底估算读此值）并经
// provider.Request.Instructions 发送（wire 不变）。
func TestRunSystemPromptCompose(t *testing.T) {
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return textStream("ok"), nil
	}}
	a := noToolsAgent(fc)
	a.SetMiddleware(middleware.NewChain(
		impl.BaseInstructionsMiddleware{Text: "BASE"},
		suffixMiddleware{text: "SUFFIX"},
	))
	rc := rcFor(newConversation())
	rc.SystemPrompt = "override"

	if err := a.Run(context.Background(), rc, func(events.Event) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := "BASE" + "\n\n" + "override" + "SUFFIX"
	if fc.LastReq == nil || fc.LastReq.Instructions != want {
		t.Errorf("请求系统提示: got %q want %q", fc.LastReq.Instructions, want)
	}
	if rc.SystemPrompt != want {
		t.Errorf("回写: got %q want %q", rc.SystemPrompt, want)
	}
	// 空起点：基础提示词由链首中间件注入，组合结果不含空段。
	rc2 := rcFor(newConversation())
	if err := a.Run(context.Background(), rc2, func(events.Event) {}); err != nil {
		t.Fatalf("Run(empty): %v", err)
	}
	if rc2.SystemPrompt != "BASE"+"SUFFIX" {
		t.Errorf("空起点组合: got %q", rc2.SystemPrompt)
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

	conv := newConversation()
	rec := &eventRecorder{}
	if err := a.Run(context.Background(), rcFor(conv), rec.on); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 事件含工具与回合边界。
	if !rec.has(events.EventToolCall) || !rec.has(events.EventToolResult) || !rec.has(events.EventTurnDone) {
		t.Errorf("事件缺失: %v", rec.types())
	}
	// conversation：user, assistant(tool_calls), tool_result, assistant(text)
	if len(conv.Messages) != 4 {
		t.Fatalf("conversation: got %d messages", len(conv.Messages))
	}
	tr := conv.Messages[2]
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
	conv := newConversation()
	rec := &eventRecorder{}
	if err := a.Run(context.Background(), rcFor(conv), rec.on); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if max < 2 {
		t.Errorf("期望至少 2 个工具并发执行，峰值 %d", max)
	}
	// user, assistant, tool_result（合并 2 块）, assistant
	if len(conv.Messages) != 4 {
		t.Errorf("conversation 消息数: %d", len(conv.Messages))
	}
	tr := conv.Messages[2]
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
	conv := newConversation()
	rec := &eventRecorder{}
	if err := a.Run(context.Background(), rcFor(conv), rec.on); err != nil {
		t.Fatalf("Run: %v", err)
	}
	tr := conv.Messages[2]
	if tr.Role != messages.RoleTool || len(tr.ToolResults) != 1 || tr.ToolResults[0].Success {
		t.Errorf("tool result 应标记失败: %+v", tr.ToolResults)
	}
	if !strings.Contains(tr.ToolResults[0].Content, "recoverable") {
		t.Errorf("tool result content: %q", tr.ToolResults[0].Content)
	}
	if !rec.has(events.EventTurnDone) {
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
	conv := newConversation()
	rec := &eventRecorder{}
	err := a.Run(context.Background(), rcFor(conv), rec.on)
	if err == nil || !strings.Contains(err.Error(), "fatal") {
		t.Fatalf("期望 Fatal 错误终止，got %v", err)
	}
	if rec.has(events.EventTurnDone) {
		t.Error("Fatal 后不应有 turn_done")
	}
}

// TestRunToolBatchInterruptedRepairsPairing 验证工具批被中断时补全 tool_result
// 配对（Bug10，2026-08-11）：Esc/Ctrl+C 中断工具执行后，assistant 已带
// tool_calls 落盘而 tool_result 缺失——违反 anthropic 邻接约束（ADR-024），
// 下一轮采样必 400。agent 必须为未执行的调用回填"未执行"块（conversation +
// emit 落盘 transcript），双轨保持一致。
func TestRunToolBatchInterruptedRepairsPairing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		if len(req.Messages) == 1 {
			return provider.NewFakeStream([]provider.Event{
				{Type: provider.EventToolCall, ToolCall: &messages.ToolCall{ID: "c1", Name: "slow1", Args: json.RawMessage(`{}`)}},
				{Type: provider.EventToolCall, ToolCall: &messages.ToolCall{ID: "c2", Name: "slow2", Args: json.RawMessage(`{}`)}},
				{Type: provider.EventDone},
			}), nil
		}
		return textStream("done"), nil
	}}
	reg := tools.NewRegistry()
	mk := func(name string) tools.Tool {
		return &fakeTool{name: name, handle: func(ctx context.Context, args json.RawMessage) (messages.ToolResult, error) {
			<-ctx.Done() // 阻塞直到中断（模拟长任务被 Esc 打断）
			return messages.ToolResult{}, ctx.Err()
		}}
	}
	reg.Register(mk("slow1"))
	reg.Register(mk("slow2"))
	a := New(fc, "m")
	a.SetTools(reg)

	conv := newConversation()
	rec := &eventRecorder{}
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	err := a.Run(ctx, rcFor(conv), rec.on)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("期望 context.Canceled，got %v", err)
	}
	// conversation 末尾：tool 消息覆盖全部 call（含未执行的"未执行"块）。
	last := conv.Messages[len(conv.Messages)-1]
	if last.Role != messages.RoleTool || len(last.ToolResults) != 2 {
		t.Fatalf("期望补全 2 个 tool_result 块，got %+v", last)
	}
	ids := map[string]bool{}
	for _, b := range last.ToolResults {
		ids[b.ToolCallID] = true
		if b.Success {
			t.Errorf("未执行块应标记失败: %+v", b)
		}
		if !strings.Contains(b.Content, "未执行") {
			t.Errorf("块内容应含未执行提示: %q", b.Content)
		}
	}
	if !ids["c1"] || !ids["c2"] {
		t.Errorf("应覆盖全部 call_id: %v", ids)
	}
	// 补全的 tool_result 也 emit（transcript 落盘，resume 后可重建合法会话）。
	n := 0
	for _, e := range rec.events {
		if e.Type == events.EventToolResult {
			n++
		}
	}
	if n != 2 {
		t.Errorf("应 emit 2 个补全 EventToolResult，got %d", n)
	}
}

// TestRepairDanglingToolUse 验证采样前自愈：存量会话里 assistant 带 tool_calls
// 但无紧跟的 tool_result（修复前中断残留，且其后已插入 user），repairDanglingToolUse
// 把缺失块插入到 assistant 之后，恢复 tool_use → tool_result 紧邻（Bug10 兜底）。
func TestRepairDanglingToolUse(t *testing.T) {
	conv := messages.NewConversation()
	conv.Add(messages.NewUserMessage("hi"))
	asst := &messages.Message{ID: "m1", Role: messages.RoleAssistant, ToolCalls: []messages.ToolCall{
		{ID: "c1", Name: "t", Args: json.RawMessage(`{}`)},
		{ID: "c2", Name: "t", Args: json.RawMessage(`{}`)},
	}}
	conv.Add(asst)
	conv.Add(messages.NewUserMessage("next")) // 存量损坏：tool_use 后直接 user

	rec := &eventRecorder{}
	repairDanglingToolUse(conv, rec.on)

	// user, assistant(tool_calls), tool(补全), user
	if len(conv.Messages) != 4 {
		t.Fatalf("期望插入 tool 消息后 4 条，got %d", len(conv.Messages))
	}
	if conv.Messages[1] != asst {
		t.Fatal("assistant 应保持在原位")
	}
	toolMsg := conv.Messages[2]
	if toolMsg.Role != messages.RoleTool || len(toolMsg.ToolResults) != 2 {
		t.Fatalf("assistant 后应紧跟完整 tool 消息: %+v", toolMsg)
	}
	if conv.Messages[3].Role != messages.RoleUser {
		t.Fatal("原 user 消息应保持在 tool 之后")
	}
	ids := map[string]bool{}
	for _, b := range toolMsg.ToolResults {
		ids[b.ToolCallID] = true
	}
	if !ids["c1"] || !ids["c2"] {
		t.Errorf("tool 消息应覆盖全部 call_id: %v", ids)
	}
	// 补全的块也 emit（transcript 落盘）。
	n := 0
	for _, e := range rec.events {
		if e.Type == events.EventToolResult {
			n++
		}
	}
	if n != 2 {
		t.Errorf("应 emit 2 个 EventToolResult，got %d", n)
	}
}

// TestRepairDanglingInterleaved 验证 user 插进 tool_result 中间（旧版 requestInterrupt
// 在工具批中 AddUser 中断提示，Bug10 存量损坏）的修复：resume 重建后 assistant 的
// tool_result 被 user 隔开、散成多条 tool 消息——修复必须把全部 result 合并回紧邻
// 的一条 tool 消息（并提升被 user 隔开的真实 result），否则 anthropic 报"下一条消息
// 不含全部 tool_result"（用户实测 400，报缺 call_01）。
func TestRepairDanglingInterleaved(t *testing.T) {
	conv := messages.NewConversation()
	conv.Add(messages.NewUserMessage("hi"))
	asst := &messages.Message{ID: "m1", Role: messages.RoleAssistant, ToolCalls: []messages.ToolCall{
		{ID: "call_00", Name: "shell", Args: json.RawMessage(`{}`)},
		{ID: "call_01", Name: "list", Args: json.RawMessage(`{}`)},
	}}
	conv.Add(asst)
	// 旧版产物：call_01 的 result 紧邻，user(中断提示) 插入，call_00 的 result 被隔开。
	conv.Add(&messages.Message{Role: messages.RoleTool, ToolResults: []messages.ToolResultBlock{
		{ToolCallID: "call_01", Success: true, Content: "list ok"},
	}})
	conv.Add(messages.NewUserMessage("(System: interrupted)"))
	conv.Add(&messages.Message{Role: messages.RoleTool, ToolResults: []messages.ToolResultBlock{
		{ToolCallID: "call_00", Success: false, Content: "shell exit 1"},
	}})
	conv.Add(messages.NewUserMessage("进入plan"))

	repairDanglingToolUse(conv, nil)

	// user, assistant, tool(全部), user(System), user(进入plan)
	if len(conv.Messages) != 5 {
		t.Fatalf("期望 5 条消息，got %d", len(conv.Messages))
	}
	if conv.Messages[1] != asst {
		t.Fatal("assistant 应保持原位")
	}
	adj := conv.Messages[2]
	if adj.Role != messages.RoleTool || len(adj.ToolResults) != 2 {
		t.Fatalf("assistant 后应紧邻一条含全部 result 的 tool 消息: %+v", adj)
	}
	// 真实 result 应被保留（不丢信息，非全部"未执行"）。
	var saw00, saw01 bool
	for _, b := range adj.ToolResults {
		switch b.ToolCallID {
		case "call_00":
			saw00 = true
			if !strings.Contains(b.Content, "shell exit 1") {
				t.Errorf("call_00 应保留真实结果，got %q", b.Content)
			}
		case "call_01":
			saw01 = true
			if !strings.Contains(b.Content, "list ok") {
				t.Errorf("call_01 应保留真实结果，got %q", b.Content)
			}
		}
	}
	if !saw00 || !saw01 {
		t.Errorf("tool 消息应覆盖全部 call: %v", conv.Messages[2].ToolResults)
	}
	// 散落的第二个 tool 消息已移除（result 合并进紧邻）。
	for _, msg := range conv.Messages[3:] {
		if msg.Role == messages.RoleTool {
			t.Fatalf("assistant 之后不应有散落 tool 消息: %+v", msg)
		}
	}
	// 原 user(中断提示) 保持在 tool 之后、下一个 user 之前。
	if conv.Messages[3].Role != messages.RoleUser || !strings.Contains(conv.Messages[3].Content, "interrupted") {
		t.Fatalf("user(中断提示) 应保持在 tool 之后: %+v", conv.Messages[3])
	}
}

// TestRunRepairsDanglingToolUse 验证 Run 采样前自动自愈存量损坏会话：conversation
// 为 [user, assistant(tool_calls), user]（tool_use 后无 tool_result），Run 首轮
// 采样前补全 tool 消息，此后采样不违反 anthropic 邻接约束。
func TestRunRepairsDanglingToolUse(t *testing.T) {
	conv := messages.NewConversation()
	conv.Add(messages.NewUserMessage("hi"))
	conv.Add(&messages.Message{ID: "m1", Role: messages.RoleAssistant, ToolCalls: []messages.ToolCall{
		{ID: "c1", Name: "t", Args: json.RawMessage(`{}`)},
	}})
	conv.Add(messages.NewUserMessage("继续"))

	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return textStream("ok"), nil
	}}
	a := noToolsAgent(fc)
	rec := &eventRecorder{}
	if err := a.Run(context.Background(), rcFor(conv), rec.on); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// user, assistant(tool_calls), tool(自愈), user, assistant(text)
	if len(conv.Messages) != 5 {
		t.Fatalf("期望 5 条消息，got %d", len(conv.Messages))
	}
	tr := conv.Messages[2]
	if tr.Role != messages.RoleTool || len(tr.ToolResults) != 1 || tr.ToolResults[0].ToolCallID != "c1" {
		t.Fatalf("自愈应插入覆盖 c1 的 tool 消息: %+v", tr)
	}
}

// TestRunThinkingDelta 验证 thinking/text 增量透传事件（渲染），且块完成
// 事件（thinking_done/text_done）驱动 assistant 消息的 Content/Thinking 组装
// （ADR-025），thinking 签名随块捕获存 Message.ThinkingSignature（ADR-025 修订）。
func TestRunThinkingDelta(t *testing.T) {
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return provider.NewFakeStream([]provider.Event{
			{Type: provider.EventThinkingDelta, Text: "Let me think"},
			{Type: provider.EventThinkingDone, Text: "Let me think", Signature: "sig_t"},
			{Type: provider.EventTextDelta, Text: "answer"},
			{Type: provider.EventTextDone, Text: "answer"},
			{Type: provider.EventDone},
		}), nil
	}}
	a := noToolsAgent(fc)
	rec := &eventRecorder{}
	conv := newConversation()
	if err := a.Run(context.Background(), rcFor(conv), rec.on); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 块完成事件透传（持久化订阅用），签名随 thinking_done 透传。
	gotThinkingDone, gotTextDone := false, false
	sigOK := false
	for _, e := range rec.events {
		if e.Type == events.EventThinkingDelta && e.Text == "Let me think" {
			gotThinkingDone = true
		}
		if e.Type == events.EventThinkingDone && e.Text == "Let me think" {
			gotThinkingDone = true
			sigOK = e.Signature == "sig_t"
		}
		if e.Type == events.EventTextDone && e.Text == "answer" {
			gotTextDone = true
		}
	}
	if !gotThinkingDone {
		t.Errorf("thinking_done 事件缺失: %v", rec.types())
	}
	if !sigOK {
		t.Errorf("thinking_done 应透传签名 sig_t: %v", rec.events)
	}
	if !gotTextDone {
		t.Errorf("text_done 事件缺失: %v", rec.types())
	}
	// assistant 消息组装：Content=text、Thinking=thinking（完整回传，存签名）。
	var asst *messages.Message
	for _, m := range conv.Messages {
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
	if asst.ThinkingSignature != "sig_t" {
		t.Errorf("assistant ThinkingSignature = %q, want sig_t", asst.ThinkingSignature)
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

	if err := a.Run(context.Background(), rcFor(newConversation()), nil); err != nil {
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

	if err := a.Run(context.Background(), rcFor(newConversation()), func(e events.Event) { seq = append(seq, string(e.Type)) }); err != nil {
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
	err := a.Run(context.Background(), rcFor(newConversation()), rec.on)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("期望 boom 错误，got %v", err)
	}
	if !rec.has(events.EventError) {
		t.Error("应发出 events.EventError")
	}
}

// TestRunModelFromRC 验证 per-call 覆盖（ADR-026）：rc.Model / rc.ThinkingEffort /
// rc.ThinkingEnabled 写入 provider.Request（未设置时用 agent 默认）。
func TestRunModelFromRC(t *testing.T) {
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return textStream("ok"), nil
	}}
	a := New(fc, "default-model") // agent 默认模型
	rc := rcFor(newConversation())
	rc.Model = "other-model"
	rc.ThinkingEffort = config.EffortMax
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
	if fc.LastReq.ThinkingEffort != config.EffortMax {
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

	rc := rcFor(newConversation())
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

// TestEmitBeforeTruncation 验证双轨审计时序契约（C6）：工具结果 emit 时是
// 全量（transcript 侧记审计完整），conversation 经 ToolOutputMiddleware after
// 截断（模型上下文省 token）。这依赖"emit 早于截断"的顺序——若把 emit 挪到
// 截断之后，transcript 会记录截断内容、审计完整性静默丢失，本测试即红。
func TestEmitBeforeTruncation(t *testing.T) {
	// 超长工具结果（> MaxOutputChars 触发 evict 截断 + 落盘）。
	long := "HEAD-" + strings.Repeat("x", tools.MaxOutputChars*2) + "-TAIL"
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		if len(req.Messages) == 1 { // 首轮：工具调用
			return toolCallStream("big_tool", `{}`), nil
		}
		return textStream("done"), nil // 后续：结束回合
	}}
	a := New(fc, "m")
	reg := tools.NewRegistry()
	_ = reg.Register(&fakeTool{name: "big_tool", handle: func(ctx context.Context, args json.RawMessage) (messages.ToolResult, error) {
		return messages.ToolResult{Success: true, Content: long}, nil
	}})
	a.SetTools(reg)
	a.SetMiddleware(middleware.NewChain(impl.ToolOutputMiddleware{}))

	conv := newConversation()
	rc := rcFor(conv)
	rc.StatePath = filepath.Join(t.TempDir(), "sess", "agentstate.json") // evict 落盘需要
	rec := &eventRecorder{}
	if err := a.Run(context.Background(), rc, rec.on); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// ① emit 的事件是完整结果（transcript 侧全量，未截断）。
	var emitted *messages.ToolResult
	for _, e := range rec.events {
		if e.Type == events.EventToolResult && e.ToolResult != nil {
			emitted = e.ToolResult
			break
		}
	}
	if emitted == nil {
		t.Fatal("无 events.EventToolResult")
	}
	if emitted.Content != long {
		t.Errorf("emit 应为完整结果，got 前 80 字节: %s", head80(emitted.Content))
	}

	// ② conversation 里同一结果被截断（含落盘提示）。
	var convContent string
	for _, m := range conv.Messages {
		for _, r := range m.ToolResults {
			if r.ToolCallID == "c1" {
				convContent = r.Content
			}
		}
	}
	if !strings.Contains(convContent, "完整内容已保存到") {
		t.Errorf("conversation 应被截断（含落盘提示），got 前 80 字节: %s", head80(convContent))
	}
}

// head80 截断长字符串前 80 字节用于错误输出（避免刷屏）。
func head80(s string) string {
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}

// TestRunEmitsEventUsage 验证采样轮结束后透出 EventUsage（ADR-037 用量展示：
// provider EventDone 携带的 usage → 回合级事件）。
func TestRunEmitsEventUsage(t *testing.T) {
	usage := &messages.Usage{InputTokens: 100, OutputTokens: 50, CacheReadInputTokens: 20}
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return provider.NewFakeStream([]provider.Event{
			{Type: provider.EventTextDelta, Text: "hi"},
			{Type: provider.EventTextDone, Text: "hi"},
			{Type: provider.EventDone, Usage: usage},
		}), nil
	}}
	a := noToolsAgent(fc)
	conv := newConversation()
	rec := &eventRecorder{}
	if err := a.Run(context.Background(), rcFor(conv), rec.on); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var got *messages.Usage
	for _, e := range rec.events {
		if e.Type == events.EventUsage {
			got = e.Usage
		}
	}
	if got == nil {
		t.Fatalf("expected EventUsage, got %v", rec.types())
	}
	if got.InputTokens != 100 || got.OutputTokens != 50 || got.CacheReadInputTokens != 20 {
		t.Errorf("usage: got %+v", got)
	}
	// assistant 正常入 conversation。
	if len(conv.Messages) != 2 || conv.Messages[1].Content != "hi" {
		t.Errorf("assistant: got %d messages", len(conv.Messages))
	}
}

// TestRunSkipsZeroUsage 验证无 usage 数据（EventDone.Usage nil/全零）时不发
// EventUsage（避免噪音事件）。
func TestRunSkipsZeroUsage(t *testing.T) {
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return textStream("hi"), nil // EventDone 无 usage
	}}
	a := noToolsAgent(fc)
	rec := &eventRecorder{}
	if err := a.Run(context.Background(), rcFor(newConversation()), rec.on); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rec.has(events.EventUsage) {
		t.Error("无用量数据不应发出 EventUsage")
	}
}

// TestRunAutoCompactSampleFailureStillEmitsCompacted 验证 review 08 修复：压缩
// 成功但随后的采样失败时仍 emit EventCompacted（压缩已生效——conversation 已
// 重写、transcript 已切段；用户只看到错误会误判重压/丢历史，先"已压缩"系统行
// 再报错误，下轮重试即正常）。
func TestRunAutoCompactSampleFailureStillEmitsCompacted(t *testing.T) {
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		if n := len(req.Messages); n > 0 && strings.Contains(req.Messages[n-1].Content, "anchored summary") {
			return textStream("总结内容"), nil
		}
		return nil, errors.New("sample boom")
	}}
	compactor := compact.NewRunner(
		compact.NewSummarizer(fc, compact.Options{ContextWindow: 1_000_000}),
		compact.Options{ContextWindow: 1_000_000},
	)
	a := New(fc, "m")
	a.SetTools(tools.NewRegistry())
	a.SetMiddleware(middleware.NewChain(impl.CompactMiddleware{Runner: compactor}))
	a.SetCompactor(compactor)

	conv := newConversation()
	rc := rcFor(conv)
	rc.State = agentstate.New("s1", "m", ".")
	rc.State.SetLastContextTokens(900_000)
	rec := &eventRecorder{}
	rc.Emit = rec.on
	if err := a.Run(context.Background(), rc, rec.on); err == nil {
		t.Fatal("采样失败应返回错误")
	}
	if !rec.has(events.EventCompacted) {
		t.Errorf("压缩成功后采样失败仍应 emit EventCompacted: %v", rec.types())
	}
	if i, j := rec.indexOf(events.EventCompacted), rec.indexOf(events.EventError); i < 0 || j < 0 || i >= j {
		t.Errorf("EventCompacted 应早于 EventError: %v", rec.types())
	}
	// 压缩已生效：conversation = [summary]（采样失败不追加 assistant）。
	if len(conv.Messages) != 1 || conv.Messages[0].Content != "总结内容" {
		t.Errorf("conversation 应为 [summary]: %+v", conv.Messages)
	}
}

// TestRunAutoCompact 验证 onReasoning 自动压缩（ADR-037）：超 85% 阈值 → 采样
// 前压缩 → conversation 重写为单一摘要占位 + emit EventCompacted；未超阈值 →
// 正常透传不压缩。回归锚点（review 01）：**触发压缩的那一轮采样请求本身**
// 必须是重写后的 [summary]（而非压缩前的旧快照——旧快照会让压缩防爆窗失效
// 且该轮 usage 抬高 LastContextTokens 导致下轮重复压缩）。
func TestRunAutoCompact(t *testing.T) {
	// StreamFn 按请求末条是否摘要 prompt 区分：摘要请求 → 摘要流，采样 → 正常流。
	// sampled 记录每次**采样**请求的消息（摘要请求不记，用于断言压缩后采样内容）。
	var sampled [][]*messages.Message
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		if n := len(req.Messages); n > 0 && strings.Contains(req.Messages[n-1].Content, "anchored summary") {
			return textStream("总结内容"), nil
		}
		sampled = append(sampled, req.Messages)
		return textStream("final answer"), nil
	}}
	compactor := compact.NewRunner(
		compact.NewSummarizer(fc, compact.Options{ContextWindow: 1_000_000}),
		compact.Options{ContextWindow: 1_000_000},
	)
	a := New(fc, "m")
	a.SetTools(tools.NewRegistry())
	a.SetMiddleware(middleware.NewChain(impl.CompactMiddleware{Runner: compactor}))
	a.SetCompactor(compactor)

	// 超阈值：压缩 + emit EventCompactStart（先）→ EventCompacted（后）。
	conv := newConversation()
	rc := rcFor(conv)
	rc.State = agentstate.New("s1", "m", ".")
	rc.State.SetLastContextTokens(900_000)
	rec := &eventRecorder{}
	rc.Emit = rec.on // 压缩开始通知出口（ADR-037 扩展）
	if err := a.Run(context.Background(), rc, rec.on); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !rec.has(events.EventCompacted) {
		t.Errorf("应 emit EventCompacted: %v", rec.types())
	}
	if !rec.has(events.EventCompactStart) {
		t.Errorf("应 emit EventCompactStart: %v", rec.types())
	}
	if i, j := rec.indexOf(events.EventCompactStart), rec.indexOf(events.EventCompacted); i < 0 || j < 0 || i >= j {
		t.Errorf("EventCompactStart 应早于 EventCompacted: %v", rec.types())
	}
	// conversation = [summary user 占位, assistant 采样]（压缩重写后采样追加）。
	if len(conv.Messages) != 2 {
		t.Fatalf("conversation 消息数 = %d, want 2", len(conv.Messages))
	}
	if conv.Messages[0].Role != messages.RoleUser || conv.Messages[0].Content != "总结内容" {
		t.Errorf("conversation[0] 应为摘要占位: %+v", conv.Messages[0])
	}
	// 回归锚点（review 01）：压缩后那一轮**采样请求** = [summary]，而非压缩前旧快照。
	if len(sampled) != 1 {
		t.Fatalf("采样次数 = %d, want 1", len(sampled))
	}
	if len(sampled[0]) != 1 || sampled[0][0].Content != "总结内容" {
		t.Errorf("压缩后采样请求应为 [summary]，got %d 条: %+v", len(sampled[0]), sampled[0])
	}

	// 未超阈值：不压缩、不 emit EventCompacted/EventCompactStart、历史保留。
	conv2 := newConversation()
	rc2 := rcFor(conv2)
	rc2.State = agentstate.New("s2", "m", ".")
	rc2.State.SetLastContextTokens(100)
	rec2 := &eventRecorder{}
	rc2.Emit = rec2.on
	if err := a.Run(context.Background(), rc2, rec2.on); err != nil {
		t.Fatalf("Run (under threshold): %v", err)
	}
	if rec2.has(events.EventCompacted) {
		t.Errorf("未超阈值不应 emit EventCompacted: %v", rec2.types())
	}
	if rec2.has(events.EventCompactStart) {
		t.Errorf("未超阈值不应 emit EventCompactStart: %v", rec2.types())
	}
	if len(conv2.Messages) != 2 || conv2.Messages[0].Content != "hi" {
		t.Errorf("未压缩 conversation 应保留原样 + assistant: %+v", conv2.Messages)
	}
}

// TestRunMaxTurns 验证回合上限（评测 --max-turns，2026-08-19）：模型持续请求
// 工具时，采样轮数 ≤ maxTurns；达上限 emit EventMaxTurns 后结束（不再采样/
// 执行工具），回合仍正常 turn_done、无错误。conversation 与工具执行对数 =
// maxTurns 轮。
func TestRunMaxTurns(t *testing.T) {
	calls := 0
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		calls++
		if calls < 10 {
			return toolCallStream("fake_tool", `{}`), nil
		}
		return textStream("done"), nil
	}}
	reg := tools.NewRegistry()
	if err := reg.Register(&fakeTool{name: "fake_tool", handle: func(ctx context.Context, args json.RawMessage) (messages.ToolResult, error) {
		return messages.ToolResult{Success: true, Content: "ok"}, nil
	}}); err != nil {
		t.Fatal(err)
	}

	a := New(fc, "m")
	a.SetTools(reg)
	a.SetMaxTurns(3)
	conv := newConversation()
	rec := &eventRecorder{}
	if err := a.Run(context.Background(), rcFor(conv), rec.on); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if calls != 3 {
		t.Errorf("采样轮数 = %d, want 3（maxTurns 上限）", calls)
	}
	if !rec.has(events.EventMaxTurns) {
		t.Errorf("应 emit EventMaxTurns: %v", rec.types())
	}
	if !rec.has(events.EventTurnDone) {
		t.Errorf("达上限也应正常 turn_done: %v", rec.types())
	}
	if i, j := rec.indexOf(events.EventMaxTurns), rec.indexOf(events.EventTurnDone); i < 0 || j < 0 || i >= j {
		t.Errorf("EventMaxTurns 应早于 EventTurnDone: %v", rec.types())
	}
	// conversation：user + 3×(assistant + tool 结果消息)。
	if got := len(conv.Messages); got != 7 {
		t.Errorf("conversation 消息数 = %d, want 7（user+3×(assistant+tool)）", got)
	}
}

// TestRunMaxTurnsNotHit 验证上限内自然结束：不 emit EventMaxTurns。
func TestRunMaxTurnsNotHit(t *testing.T) {
	calls := 0
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		calls++
		if calls == 1 {
			return toolCallStream("fake_tool", `{}`), nil
		}
		return textStream("done"), nil
	}}
	reg := tools.NewRegistry()
	if err := reg.Register(&fakeTool{name: "fake_tool", handle: func(ctx context.Context, args json.RawMessage) (messages.ToolResult, error) {
		return messages.ToolResult{Success: true, Content: "ok"}, nil
	}}); err != nil {
		t.Fatal(err)
	}

	a := New(fc, "m")
	a.SetTools(reg)
	a.SetMaxTurns(5)
	conv := newConversation()
	rec := &eventRecorder{}
	if err := a.Run(context.Background(), rcFor(conv), rec.on); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rec.has(events.EventMaxTurns) {
		t.Errorf("上限内完成不应 emit EventMaxTurns: %v", rec.types())
	}
	if calls != 2 {
		t.Errorf("采样轮数 = %d, want 2", calls)
	}
}
