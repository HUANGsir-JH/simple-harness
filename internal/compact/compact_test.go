package compact

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/events"
	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/provider"
)

// --- 纯函数 ---------------------------------------------------------------

// TestShouldCompact 验证 85% 阈值判定（ADR-037）：实际 usage（LastContextTokens）
// 优先；真实值缺省时估算兜底。tools 仅兜底分支使用，usage 路径传 nil 不影响。
func TestShouldCompact(t *testing.T) {
	opts := Options{ContextWindow: 1_000_000}
	// 真实 usage 驱动。
	rc := testRC(overThresholdRC(850_000))
	if !ShouldCompact(rc, nil, opts) {
		t.Error("850k >= 85%*1M 应触发")
	}
	rc = testRC(overThresholdRC(849_999))
	if ShouldCompact(rc, nil, opts) {
		t.Error("849999 < 85%*1M 不应触发")
	}
	rc = testRC(overThresholdRC(850_000))
	opts.ContextWindow = 0
	if ShouldCompact(rc, nil, opts) {
		t.Error("无 context_window 不应触发")
	}
}

// TestShouldCompactEstimateFallback 验证无真实 usage（LastContextTokens=0）时
// 用估算兜底（EstimateTokens 镜像实际发送，含 thinking）。
func TestShouldCompactEstimateFallback(t *testing.T) {
	opts := Options{ContextWindow: 4_000} // 85% = 3400
	// 估算兜底：消息字节 ≥ 3400*4 = 13600 → 触发。
	big := strings.Repeat("x", 13_600)
	rc := middleware.NewRuntimeContext()
	rc.Messages = &messages.Conversation{Messages: []*messages.Message{{Role: messages.RoleUser, Content: big}}}
	if !ShouldCompact(rc, nil, opts) {
		t.Error("估算 3400 tokens 应触发（无真实 usage）")
	}
	// 小消息不触发。
	rc.Messages = &messages.Conversation{Messages: []*messages.Message{{Role: messages.RoleUser, Content: "hi"}}}
	if ShouldCompact(rc, nil, opts) {
		t.Error("小上下文不应触发")
	}
}

// TestShouldCompactEstimateIncludesFixedOverhead 验证估算兜底计入另外两个
// 请求通道（ADR-037 修订 2026-08-13）：系统提示（rc.SystemPrompt，组合后回写）
// 与工具 schema（判定时实时传入的 in.Tools）——二者不进 messages，缺失任一项
// 触发点都会系统性偏晚。
func TestShouldCompactEstimateIncludesFixedOverhead(t *testing.T) {
	// 85% * 4000 = 3400。消息 2000 tokens（8000 字节）+ 系统提示 1000（4000 字节）
	// + 工具 schema 1000（4000 字节）= 4000 ≥ 3400 → 触发；缺任一固定开销不触发。
	msg := strings.Repeat("x", 8_000)
	sys := strings.Repeat("y", 4_000)
	tools := []provider.ToolSpec{{Name: "t", Description: strings.Repeat("z", 4_000)}}

	rc := middleware.NewRuntimeContext()
	rc.Messages = &messages.Conversation{Messages: []*messages.Message{{Role: messages.RoleUser, Content: msg}}}
	rc.SystemPrompt = sys

	// 无 tools：2000 + 1000 = 3000 < 3400 不触发。
	if ShouldCompact(rc, nil, Options{ContextWindow: 4_000}) {
		t.Error("缺工具 schema 时 3000 < 3400 不应触发")
	}
	// 无系统提示：2000 + 1000 = 3000 < 3400 不触发。
	rc.SystemPrompt = ""
	if ShouldCompact(rc, tools, Options{ContextWindow: 4_000}) {
		t.Error("缺系统提示时 3000 < 3400 不应触发")
	}
	rc.SystemPrompt = sys
	// 全量：4000 >= 3400 触发。
	if !ShouldCompact(rc, tools, Options{ContextWindow: 4_000}) {
		t.Error("计入 系统提示 + 工具 schema 后 4000 >= 3400 应触发")
	}
}

// TestBuildSummaryPrompt 验证摘要 prompt：新建式指令 + 锚定模板（旧摘要无需
// previous 参数——压缩后 conversation 首条就是旧摘要，LLM 在历史里可见，
// review 07 双份喂送已消除）。
func TestBuildSummaryPrompt(t *testing.T) {
	np := BuildSummaryPrompt()
	if !strings.Contains(np, "Create a new anchored summary") || !strings.Contains(np, "## Objective") || !strings.Contains(np, "## Next Move") {
		t.Errorf("prompt 应含创建指令 + 锚定模板: %s", np)
	}
	if strings.Contains(np, "<previous-summary>") {
		t.Error("不应嵌 previous-summary（旧摘要在 conversation 首条）")
	}
}

// --- Summarizer ------------------------------------------------------------

// testRC 构造带 State 与空 Segment 的测试 rc（会话场景对位）。
func testRC(state *agentstate.AgentState) *middleware.RuntimeContext {
	rc := middleware.NewRuntimeContext()
	rc.State = state
	rc.Messages = &messages.Conversation{Messages: []*messages.Message{
		{Role: messages.RoleUser, Content: "用户问题"},
		{Role: messages.RoleAssistant, Content: "回答", Thinking: "推理", ThinkingSignature: "sig"},
	}}
	return rc
}

func overThresholdRC(lastTokens int64) *agentstate.AgentState {
	st := agentstate.New("s1", "m", ".")
	st.SetLastContextTokens(lastTokens)
	return st
}

// summaryStream 构造"文本块完成 → done"的摘要流（Summarizer 只收 EventTextDone，
// 同 agent.sample 组装；delta 是流式增量不收）。
func summaryStream(text string) provider.EventStream {
	return provider.NewFakeStream([]provider.Event{
		{Type: provider.EventTextDone, Text: text},
		{Type: provider.EventDone},
	})
}

// TestSummarizeRequestShape 验证摘要请求 = 完整 conversation + 摘要 prompt 尾
// user，无工具，max_tokens（codex 方式，ADR-037）。
func TestSummarizeRequestShape(t *testing.T) {
	conv := testRC(overThresholdRC(900_000))
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return summaryStream("总结内容"), nil
	}}
	s := NewSummarizer(fc, Options{ContextWindow: 1_000_000, MaxOutputTokens: 4096})
	got, err := s.Summarize(context.Background(), conv)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if got != "总结内容" {
		t.Errorf("summary = %q", got)
	}
	// 请求形状断言。
	req := fc.LastReq
	if req == nil {
		t.Fatal("无请求")
	}
	if len(req.Messages) != 3 { // conversation 2 条 + prompt 1 条
		t.Fatalf("Messages = %d, want 3", len(req.Messages))
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role != messages.RoleUser || last.Content != BuildSummaryPrompt() {
		t.Errorf("最后一条应为摘要 prompt user: role=%s", last.Role)
	}
	if len(req.Tools) != 0 {
		t.Errorf("摘要请求不应有工具: %v", req.Tools)
	}
	if req.MaxOutputTokens != 4096 {
		t.Errorf("MaxOutputTokens = %d, want 4096", req.MaxOutputTokens)
	}
	// Summarize 本身不重写 conversation（重写发生在 Runner.Run）。
	if len(conv.Messages.Messages) != 2 {
		t.Error("Summarize 不应重写 conversation")
	}
}

// TestSummarizeModelOverride 验证摘要请求模型覆盖与 agent.sample 同规则
// （ADR-026）：rc.Model 优先（/model 运行时切换），未设置时用装配默认——
// 保证摘要与正常采样模型一致。
func TestSummarizeModelOverride(t *testing.T) {
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return summaryStream("总结内容"), nil
	}}
	s := NewSummarizer(fc, Options{Model: "built-model"})
	rc := testRC(overThresholdRC(900_000))
	if _, err := s.Summarize(context.Background(), rc); err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if fc.LastReq.Model != "built-model" {
		t.Errorf("无 rc.Model 时应用装配默认: got %q want built-model", fc.LastReq.Model)
	}
	rc.Model = "session-model"
	if _, err := s.Summarize(context.Background(), rc); err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if fc.LastReq.Model != "session-model" {
		t.Errorf("rc.Model 应优先: got %q want session-model", fc.LastReq.Model)
	}
}

// TestSummarizeStreamError 验证摘要流错误 → 返回错误。
func TestSummarizeStreamError(t *testing.T) {
	rc := testRC(overThresholdRC(900_000))
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return provider.NewFakeStream([]provider.Event{{Type: provider.EventError, Error: errors.New("boom")}}), nil
	}}
	s := NewSummarizer(fc, Options{ContextWindow: 1_000_000})
	if _, err := s.Summarize(context.Background(), rc); err == nil {
		t.Fatal("流错误应返回错误")
	}
	if len(rc.Messages.Messages) != 2 {
		t.Error("失败不应重写 conversation")
	}
}

// --- Runner ----------------------------------------------------------------

// TestRunnerShouldCompact 验证判定委托（ADR-037 修订 2026-08-13）：判定在
// 调用方（CompactMiddleware 持 in.Tools），Runner.ShouldCompact 委托包级判定，
// 兜底估算三项全量：消息 + 系统提示 + 工具 schema，逐项加入跨越阈值。
func TestRunnerShouldCompact(t *testing.T) {
	opts := Options{ContextWindow: 4_000} // 85% = 3400
	r := NewRunner(NewSummarizer(nil, opts), opts)
	// 消息 2000 tokens（8000 字节）< 3400 不触发。
	msg := strings.Repeat("x", 8_000)
	rc := middleware.NewRuntimeContext()
	rc.Messages = &messages.Conversation{Messages: []*messages.Message{{Role: messages.RoleUser, Content: msg}}}
	if r.ShouldCompact(rc, nil) {
		t.Error("2000 < 3400 不应触发")
	}
	// 加工具 schema ~1000 tokens：3000 < 3400 仍不触发。
	tools := []provider.ToolSpec{{Name: "t", Description: strings.Repeat("z", 4_000)}}
	if r.ShouldCompact(rc, tools) {
		t.Error("2000 + 工具 1000 = 3000 < 3400 不应触发")
	}
	// 再加系统提示 1000 tokens：4000 >= 3400 触发。
	rc.SystemPrompt = strings.Repeat("y", 4_000)
	if !r.ShouldCompact(rc, tools) {
		t.Error("三项全量 4000 >= 3400 应触发")
	}
}

// TestRunnerRunCompacts 验证全链路：Run 无条件压缩 → 摘要 → 先 Segment 落盘、
// 后重写 conversation 为单一 summary user（review 03 顺序：落盘失败时内存未动）+
// 用量归零（review 06）+ LastContextTokens 清零 + 置标记。
func TestRunnerRunCompacts(t *testing.T) {
	rc := testRC(overThresholdRC(900_000))
	rc.State.SetUsage(messages.Usage{InputTokens: 900_000})
	var segmentSeed []*messages.Message
	segmentCalls := 0
	rewrittenAtSegment := false
	rc.Segment = func(seed []*messages.Message) error {
		segmentCalls++
		segmentSeed = seed
		// 03 回归锚点：Segment 时 conversation 应仍是压缩前旧历史（先落盘后重写）。
		rewrittenAtSegment = len(rc.Messages.Messages) == 1
		return nil
	}
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return summaryStream("压缩后的总结"), nil
	}}
	r := NewRunner(NewSummarizer(fc, Options{ContextWindow: 1_000_000}), Options{ContextWindow: 1_000_000})
	if err := r.Run(context.Background(), rc); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// conversation = 单一 summary user。
	if len(rc.Messages.Messages) != 1 || rc.Messages.Messages[0].Role != messages.RoleUser ||
		rc.Messages.Messages[0].Content != "压缩后的总结" {
		t.Fatalf("conversation 应为单一 summary user: %+v", rc.Messages.Messages)
	}
	if rc.State.CurrentContextTokens() != 0 {
		t.Error("压缩后 LastContextTokens 应为 0（防重入）")
	}
	if got := rc.State.UsageTotals(); !got.IsZero() {
		t.Errorf("压缩后 Usage 应归零（/usage 与 footer 对称），got %+v", got)
	}
	if segmentCalls != 1 || len(segmentSeed) != 1 || segmentSeed[0].Content != "压缩后的总结" {
		t.Errorf("Segment 应被调用 1 次且 seed = [summary]: calls=%d seed=%+v", segmentCalls, segmentSeed)
	}
	if rewrittenAtSegment {
		t.Error("Segment 时 conversation 不应已被重写（先落盘后重写，review 03）")
	}
	if rc.Get(middleware.CompactedKey) != true {
		t.Error("应置 compacted 标记")
	}
}

// TestRunnerRunSegmentFailureKeepsMemory 验证 03 修复：Segment 落盘失败时内存
// conversation/state 未动（双轨一致，都还是压缩前），下一轮干净重试。
func TestRunnerRunSegmentFailureKeepsMemory(t *testing.T) {
	rc := testRC(overThresholdRC(900_000))
	rc.Segment = func(seed []*messages.Message) error { return errors.New("disk boom") }
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return summaryStream("压缩后的总结"), nil
	}}
	r := NewRunner(NewSummarizer(fc, Options{ContextWindow: 1_000_000}), Options{ContextWindow: 1_000_000})
	if err := r.Run(context.Background(), rc); err == nil {
		t.Fatal("Segment 失败应返回错误")
	}
	if len(rc.Messages.Messages) != 2 {
		t.Fatalf("落盘失败不应重写 conversation: %d 条", len(rc.Messages.Messages))
	}
	if rc.State.CurrentContextTokens() != 900_000 {
		t.Error("落盘失败不应清零 LastContextTokens")
	}
	if rc.Get(middleware.CompactedKey) == true {
		t.Error("落盘失败不应置标记")
	}
}

// TestRunnerRunUnconditional 验证 Run 无条件压缩（ADR-037 修订 2026-08-13）：
// 判定由调用方决定，低于阈值也压缩——手动 /compact 语义。
func TestRunnerRunUnconditional(t *testing.T) {
	rc := testRC(overThresholdRC(100)) // 远低于阈值
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return summaryStream("强制总结"), nil
	}}
	r := NewRunner(NewSummarizer(fc, Options{ContextWindow: 1_000_000}), Options{ContextWindow: 1_000_000})
	if err := r.Run(context.Background(), rc); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rc.Messages.Messages) != 1 {
		t.Fatalf("conversation 应重写: %d", len(rc.Messages.Messages))
	}
}

// TestRunnerRunEmitStart 验证压缩开始通知（ADR-037 扩展）：Run（无条件压缩）
// 在 Summarize 前经 rc.Emit 发出 EventCompactStart。
func TestRunnerRunEmitStart(t *testing.T) {
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return summaryStream("压缩后的总结"), nil
	}}
	r := NewRunner(NewSummarizer(fc, Options{ContextWindow: 1_000_000}), Options{ContextWindow: 1_000_000})
	rc := testRC(overThresholdRC(900_000))
	got := []events.EventType{}
	rc.Emit = func(e events.Event) { got = append(got, e.Type) }
	if err := r.Run(context.Background(), rc); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 1 || got[0] != events.EventCompactStart {
		t.Errorf("应 emit 1 次 EventCompactStart: %v", got)
	}
}

// TestRunnerRunFailureKeepsHistory 验证摘要失败：返回错误、**不重写 conversation**
// （历史保留，可下轮再触发或手动 /compact）。
func TestRunnerRunFailureKeepsHistory(t *testing.T) {
	rc := testRC(overThresholdRC(900_000))
	before := len(rc.Messages.Messages)
	segmentCalls := 0
	rc.Segment = func(seed []*messages.Message) error { segmentCalls++; return nil }
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return nil, errors.New("summarize boom")
	}}
	r := NewRunner(NewSummarizer(fc, Options{ContextWindow: 1_000_000}), Options{ContextWindow: 1_000_000})
	if err := r.Run(context.Background(), rc); err == nil {
		t.Fatal("摘要失败应返回错误")
	}
	if len(rc.Messages.Messages) != before {
		t.Fatal("失败不应重写 conversation")
	}
	if segmentCalls != 0 {
		t.Error("失败不应切段")
	}
	if rc.Get(middleware.CompactedKey) == true {
		t.Error("失败不应置标记")
	}
}

// TestRunnerRunCancelKeepsHistory 验证 Esc 中断压缩（ctx 取消）：与摘要失败同
// 处理——返回 ctx 错误、不重写 conversation（ADR-037）。
func TestRunnerRunCancelKeepsHistory(t *testing.T) {
	rc := testRC(overThresholdRC(900_000))
	before := len(rc.Messages.Messages)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return nil, ctx.Err()
	}}
	r := NewRunner(NewSummarizer(fc, Options{ContextWindow: 1_000_000}), Options{ContextWindow: 1_000_000})
	if err := r.Run(ctx, rc); err == nil {
		t.Fatal("取消应返回错误")
	}
	if len(rc.Messages.Messages) != before {
		t.Fatal("取消不应重写 conversation")
	}
}
