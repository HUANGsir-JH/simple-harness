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
// 优先；真实值缺省时估算兜底。
func TestShouldCompact(t *testing.T) {
	opts := Options{ContextWindow: 1_000_000}
	// 真实 usage 驱动。
	rc := testRC(overThresholdRC(850_000))
	if !ShouldCompact(rc, opts) {
		t.Error("850k >= 85%*1M 应触发")
	}
	rc = testRC(overThresholdRC(849_999))
	if ShouldCompact(rc, opts) {
		t.Error("849999 < 85%*1M 不应触发")
	}
	rc = testRC(overThresholdRC(850_000))
	opts.ContextWindow = 0
	if ShouldCompact(rc, opts) {
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
	if !ShouldCompact(rc, opts) {
		t.Error("估算 3400 tokens 应触发（无真实 usage）")
	}
	// 小消息不触发。
	rc.Messages = &messages.Conversation{Messages: []*messages.Message{{Role: messages.RoleUser, Content: "hi"}}}
	if ShouldCompact(rc, opts) {
		t.Error("小上下文不应触发")
	}
}

// TestBuildSummaryPrompt 验证摘要 prompt：previous summary 更新式 vs 新建。
func TestBuildSummaryPrompt(t *testing.T) {
	// 无 previous → 新建摘要。
	np := BuildSummaryPrompt("")
	if !strings.Contains(np, "Create a new anchored summary") || !strings.Contains(np, "## Objective") || !strings.Contains(np, "## Next Move") {
		t.Errorf("新建 prompt 应含创建指令 + 锚定模板: %s", np)
	}
	// 有 previous → 更新式 + 嵌入 previous。
	up := BuildSummaryPrompt("previous-summary-content")
	if !strings.Contains(up, "Update the anchored summary") {
		t.Errorf("应含更新指令: %s", up)
	}
	if !strings.Contains(up, "<previous-summary>\nprevious-summary-content\n</previous-summary>") {
		t.Errorf("应嵌入 previous summary: %s", up)
	}
	if strings.Contains(up, "Create a new anchored summary") {
		t.Error("有 previous 时不应是新建指令")
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
	conv.State.SetSummary("old")
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
	if last.Role != messages.RoleUser || last.Content != BuildSummaryPrompt("old") {
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

// TestRunnerRunCompacts 验证全链路：超阈值 → 摘要 → 重写 conversation 为单一
// summary user + State.Summary + LastContextTokens 清零 + Segment 落盘 + 置标记。
func TestRunnerRunCompacts(t *testing.T) {
	rc := testRC(overThresholdRC(900_000))
	var segmentSeed []*messages.Message
	segmentCalls := 0
	rc.Segment = func(seed []*messages.Message) error {
		segmentCalls++
		segmentSeed = seed
		return nil
	}
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return summaryStream("压缩后的总结"), nil
	}}
	r := NewRunner(NewSummarizer(fc, Options{ContextWindow: 1_000_000}), Options{ContextWindow: 1_000_000})
	done, err := r.Run(context.Background(), rc, false)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !done {
		t.Fatal("应压缩")
	}
	// conversation = 单一 summary user。
	if len(rc.Messages.Messages) != 1 || rc.Messages.Messages[0].Role != messages.RoleUser ||
		rc.Messages.Messages[0].Content != "压缩后的总结" {
		t.Fatalf("conversation 应为单一 summary user: %+v", rc.Messages.Messages)
	}
	if rc.State.Summary != "压缩后的总结" {
		t.Errorf("State.Summary = %q", rc.State.Summary)
	}
	if rc.State.CurrentContextTokens() != 0 {
		t.Error("压缩后 LastContextTokens 应为 0（防重入）")
	}
	if segmentCalls != 1 || len(segmentSeed) != 1 || segmentSeed[0].Content != "压缩后的总结" {
		t.Errorf("Segment 应被调用 1 次且 seed = [summary]: calls=%d seed=%+v", segmentCalls, segmentSeed)
	}
	if rc.Get(middleware.CompactedKey) != true {
		t.Error("应置 compacted 标记")
	}
}

// TestRunnerRunNotOverThreshold 验证未超阈值：不透传压缩（不重写、不落盘、无标记）。
func TestRunnerRunNotOverThreshold(t *testing.T) {
	rc := testRC(overThresholdRC(100))
	segmentCalls := 0
	rc.Segment = func(seed []*messages.Message) error { segmentCalls++; return nil }
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return summaryStream("不应被调用"), nil
	}}
	r := NewRunner(NewSummarizer(fc, Options{ContextWindow: 1_000_000}), Options{ContextWindow: 1_000_000})
	done, err := r.Run(context.Background(), rc, false)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if done {
		t.Fatal("未超阈值不应压缩")
	}
	if len(rc.Messages.Messages) != 2 {
		t.Fatal("未压缩不应重写 conversation")
	}
	if segmentCalls != 0 {
		t.Error("未压缩不应切段")
	}
	if rc.Get(middleware.CompactedKey) == true {
		t.Error("未压缩不应置标记")
	}
}

// TestRunnerRunForce 验证手动 /compact（force=true）：未超阈值也压缩。
func TestRunnerRunForce(t *testing.T) {
	rc := testRC(overThresholdRC(100))
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return summaryStream("强制总结"), nil
	}}
	r := NewRunner(NewSummarizer(fc, Options{ContextWindow: 1_000_000}), Options{ContextWindow: 1_000_000})
	done, err := r.Run(context.Background(), rc, true)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !done {
		t.Fatal("force 应强制压缩")
	}
	if len(rc.Messages.Messages) != 1 {
		t.Fatalf("conversation 应重写: %d", len(rc.Messages.Messages))
	}
}

// TestRunnerRunEmitStart 验证压缩开始通知（ADR-037 扩展）：真实压缩起点
// （超阈值 / force）经 rc.Emit 发出 EventCompactStart；未超阈值被门控拦截不发。
func TestRunnerRunEmitStart(t *testing.T) {
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return summaryStream("压缩后的总结"), nil
	}}
	r := NewRunner(NewSummarizer(fc, Options{ContextWindow: 1_000_000}), Options{ContextWindow: 1_000_000})

	// 超阈值：Summarize 前发出 1 次 start。
	rc := testRC(overThresholdRC(900_000))
	got := []events.EventType{}
	rc.Emit = func(e events.Event) { got = append(got, e.Type) }
	if _, err := r.Run(context.Background(), rc, false); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 1 || got[0] != events.EventCompactStart {
		t.Errorf("超阈值应 emit 1 次 EventCompactStart: %v", got)
	}

	// 未超阈值：门控拦截，不发 start。
	rc2 := testRC(overThresholdRC(100))
	got2 := []events.EventType{}
	rc2.Emit = func(e events.Event) { got2 = append(got2, e.Type) }
	if _, err := r.Run(context.Background(), rc2, false); err != nil {
		t.Fatalf("Run (under threshold): %v", err)
	}
	if len(got2) != 0 {
		t.Errorf("未超阈值不应 emit start: %v", got2)
	}

	// force=true：未超阈值也压缩并发出 start（手动 /compact 同出口）。
	rc3 := testRC(overThresholdRC(100))
	got3 := []events.EventType{}
	rc3.Emit = func(e events.Event) { got3 = append(got3, e.Type) }
	if _, err := r.Run(context.Background(), rc3, true); err != nil {
		t.Fatalf("Run (force): %v", err)
	}
	if len(got3) != 1 || got3[0] != events.EventCompactStart {
		t.Errorf("force 应 emit 1 次 EventCompactStart: %v", got3)
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
	if _, err := r.Run(context.Background(), rc, false); err == nil {
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
	if _, err := r.Run(ctx, rc, false); err == nil {
		t.Fatal("取消应返回错误")
	}
	if len(rc.Messages.Messages) != before {
		t.Fatal("取消不应重写 conversation")
	}
}
