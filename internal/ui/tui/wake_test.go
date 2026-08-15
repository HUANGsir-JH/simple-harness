package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agent-project/harness/internal/agent"
	"github.com/agent-project/harness/internal/completion"
	"github.com/agent-project/harness/internal/config"
	"github.com/agent-project/harness/internal/events"
	"github.com/agent-project/harness/internal/provider"
	"github.com/agent-project/harness/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

// --- MaybeWake 三分支 -------------------------------------------------------

// TestMaybeWakeActiveNil 验证懒加载未触发（active nil）→ 不唤醒。
func TestMaybeWakeActiveNil(t *testing.T) {
	c := NewController(nil, nil, config.Config{}, nil, nil, context.Background())
	if cmd := c.MaybeWake(); cmd != nil {
		t.Fatal("active nil 不应唤醒")
	}
}

// TestMaybeWakeNoPending 验证无 pending 完成事件 → 不唤醒（防空跑）。
func TestMaybeWakeNoPending(t *testing.T) {
	c := newTestController(t, nil)
	if cmd := c.MaybeWake(); cmd != nil {
		t.Fatal("pending 空不应唤醒")
	}
}

// TestMaybeWakeWhileRunning 验证在途 run（cancel 非 nil）→ 不唤醒
// （信号被丢弃，路径 A 注入）。
func TestMaybeWakeWhileRunning(t *testing.T) {
	c := newTestController(t, nil)
	_, cancel := context.WithCancel(c.ctx)
	c.setCancel(cancel)
	defer c.clearCancel()
	c.active.Completions().Append(completion.Event{Result: "x"})
	if cmd := c.MaybeWake(); cmd != nil {
		t.Fatal("在途 run 不应唤醒")
	}
	if c.active.Completions().PendingCount() != 1 {
		t.Error("在途丢弃信号时 pending 应保留（由路径 A 注入）")
	}
}

// TestMaybeWakePreemptsSynchronously 回归锚点（2026-08-13 竞态修复）：MaybeWake
// 必须在返回 cmd **之前**同步抢占 cancel——tea.Cmd 异步执行，若在 cmd 内才
// setCancel，连续两条 wake 消息会双双通过 isRunning → 并发两个 run。
func TestMaybeWakePreemptsSynchronously(t *testing.T) {
	var calls atomic.Int32
	c := newTestController(t, &calls)
	c.active.Completions().Append(completion.Event{Result: "x"})

	cmd1 := c.MaybeWake()
	if cmd1 == nil {
		t.Fatal("pending 非空应唤醒")
	}
	if !c.isRunning() {
		t.Fatal("MaybeWake 应同步抢占 cancel（cmd 尚未执行）")
	}
	if cmd2 := c.MaybeWake(); cmd2 != nil {
		t.Fatal("抢占后第二条 wake 决策应被 isRunning 丢弃")
	}
	_ = cmd1() // 执行唤醒 run
	if calls.Load() != 1 {
		t.Fatalf("应只执行 1 次 agent.Run，got %d", calls.Load())
	}
	if c.isRunning() {
		t.Error("run 完成后 cancel 应清除")
	}
}

// --- completionWakeMsg → Model -------------------------------------------------

// TestCompletionWakeMsgTriggersRun 验证 wake 消息 → MaybeWake 拉起 run +
// 系统行 + running 同步置位。
func TestCompletionWakeMsgTriggersRun(t *testing.T) {
	var calls atomic.Int32
	c := newTestController(t, &calls)
	m := New(c)
	_ = collectSend(c) // setSend：生成 wakeSignal + 登记 open 会话
	c.active.Completions().Append(completion.Event{Result: "后台进程 42 已退出"})

	nm, cmd := m.Update(completionWakeMsg{})
	if cmd == nil {
		t.Fatal("pending 非空应返回唤醒 cmd")
	}
	m = nm.(Model)
	if !m.running {
		t.Error("唤醒后 running 应为 true（同步置位，防并发）")
	}
	if !c.isRunning() {
		t.Error("唤醒后 cancel 应已同步抢占")
	}
	if len(m.msgs) == 0 || m.msgs[len(m.msgs)-1].Content != "后台进程完成，继续执行…" {
		t.Errorf("应追加唤醒系统行: %+v", m.msgs)
	}
	_ = cmd()
	if calls.Load() != 1 {
		t.Fatalf("应执行 1 次 agent.Run，got %d", calls.Load())
	}
}

// TestDoubleWakeNoConcurrentRuns 回归锚点（2026-08-13 竞态修复）：cmd1 未执行
// 时第二条 wake 消息到达——m.running 第二道闸 + MaybeWake 同步抢占双保险，
// 绝不并发启动两个 run。
func TestDoubleWakeNoConcurrentRuns(t *testing.T) {
	var calls atomic.Int32
	c := newTestController(t, &calls)
	m := New(c)
	_ = collectSend(c)
	c.active.Completions().Append(completion.Event{Result: "a"})

	nm, cmd1 := m.Update(completionWakeMsg{})
	if cmd1 == nil {
		t.Fatal("第一次应唤醒")
	}
	m = nm.(Model)
	nm2, cmd2 := m.Update(completionWakeMsg{})
	m = nm2.(Model)
	if cmd2 != nil {
		t.Fatal("第二条 wake 消息不应再启动 run")
	}
	_ = cmd1()
	if calls.Load() != 1 {
		t.Fatalf("只应执行 1 次 agent.Run，got %d", calls.Load())
	}
}

// TestWakeWhileRunningDropped 验证 run 期间 wake 消息被 m.running 闸丢弃
// （路径 A 会在采样前注入）。
func TestWakeWhileRunningDropped(t *testing.T) {
	c := newTestController(t, nil)
	m := New(c)
	m.running = true
	c.active.Completions().Append(completion.Event{Result: "x"})
	nm, cmd := m.Update(completionWakeMsg{})
	if cmd != nil {
		t.Fatal("run 期间 wake 消息应丢弃")
	}
	m = nm.(Model)
	if c.active.Completions().PendingCount() != 1 {
		t.Error("pending 应保留（路径 A 注入）")
	}
}

// --- handleRunDone 补唤醒 --------------------------------------------------------

// TestRunDoneWakesOnPending 竞态窗口（2026-08-13）：在途 run 最后一次采样
// 已过后台完成（pending 残留）→ runDoneMsg（err == nil）补 MaybeWake。
func TestRunDoneWakesOnPending(t *testing.T) {
	var calls atomic.Int32
	c := newTestController(t, &calls)
	m := New(c)
	_ = collectSend(c)
	c.active.Completions().Append(completion.Event{Result: "x"})

	nm, cmd := m.Update(runDoneMsg{})
	m = nm.(Model)
	if cmd == nil {
		t.Fatal("pending 残留且 err == nil 时应补唤醒")
	}
	_ = cmd()
	if calls.Load() != 1 {
		t.Fatalf("应补跑 1 次，got %d", calls.Load())
	}
}

// TestRunDoneFailedNoRetry 回归锚点（2026-08-13 热循环修复）：唤醒 run 失败
// （err != nil）时 pending 未清——runDone 不得补唤醒，否则"唤醒失败 → 再唤醒"
// 无限热循环打 API。
func TestRunDoneFailedNoRetry(t *testing.T) {
	var calls atomic.Int32
	client := &provider.FakeClient{
		StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
			calls.Add(1)
			return nil, fmt.Errorf("provider down")
		},
	}
	sess, proj := newTestSession(t)
	c := NewController(agent.New(client, "test-model"), proj, config.Config{}, sess, nil, context.Background())
	m := New(c)
	_ = collectSend(c)
	c.active.Completions().Append(completion.Event{Result: "x"})

	// 第一次唤醒：启动失败 run。
	nm, cmd := m.Update(completionWakeMsg{})
	m = nm.(Model)
	if cmd == nil {
		t.Fatal("第一次应唤醒")
	}
	done := cmd().(runDoneMsg)
	if done.err == nil {
		t.Fatal("FakeClient 应返回错误")
	}
	// runDoneMsg(err != nil)：不补唤醒。
	nm2, cmd2 := m.Update(done)
	m = nm2.(Model)
	if cmd2 != nil {
		t.Fatal("唤醒失败后 runDone 不得再补唤醒（防热循环）")
	}
	if calls.Load() != 1 {
		t.Fatalf("应只执行 1 次 agent.Run，got %d", calls.Load())
	}
	if c.active.Completions().PendingCount() != 1 {
		t.Error("失败时 pending 应保留（等下一次完成信号/用户消息注入）")
	}
}

// --- 唤起回调登记 -----------------------------------------------------------------

// TestSetSendRegistersWakeOnOpenSessions 验证 setSend 遍历 open 表登记
// （resume 初始 sess 早于 wakeSignal 生成）：Append → completionWakeMsg。
func TestSetSendRegistersWakeOnOpenSessions(t *testing.T) {
	c := newTestController(t, nil)
	collected := collectSend(c) // setSend 生成 wakeSignal + 登记 open
	c.active.Completions().Append(completion.Event{Result: "x"})
	found := false
	for _, msg := range *collected {
		if _, ok := msg.(completionWakeMsg); ok {
			found = true
		}
	}
	if !found {
		t.Fatal("Append 应触发 completionWakeMsg 唤起信号")
	}
}

// TestSwitchToRegistersWake 验证 /switch 打开的会话也挂唤起回调。
func TestSwitchToRegistersWake(t *testing.T) {
	c := newTestController(t, nil)
	sess2, err := c.proj.Create("test-model", c.proj.Path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer sess2.Close()
	collected := collectSend(c)
	if err := c.SwitchTo(sess2.ID); err != nil {
		t.Fatalf("SwitchTo: %v", err)
	}
	c.active.Completions().Append(completion.Event{Result: "x"})
	found := false
	for _, msg := range *collected {
		if _, ok := msg.(completionWakeMsg); ok {
			found = true
		}
	}
	if !found {
		t.Fatal("SwitchTo 打开的会话应挂唤起回调")
	}
}

// TestEnsureActiveRegistersWake 验证懒加载创建（ensureActive）的会话挂唤起回调。
func TestEnsureActiveRegistersWake(t *testing.T) {
	c := NewController(nil, nil, config.Config{}, nil, func() (*session.Session, error) {
		s, _ := newTestSession(t)
		return s, nil
	}, context.Background())
	collected := collectSend(c)
	if err := c.ensureActive(); err != nil {
		t.Fatalf("ensureActive: %v", err)
	}
	c.active.Completions().Append(completion.Event{Result: "x"})
	found := false
	for _, msg := range *collected {
		if _, ok := msg.(completionWakeMsg); ok {
			found = true
		}
	}
	if !found {
		t.Fatal("懒加载创建的会话应挂唤起回调")
	}
}

// TestEventNoticeRendersSystemLine 验证 EventNotice（路径 A 注入可见性）渲染
// 为系统行。
func TestEventNoticeRendersSystemLine(t *testing.T) {
	m := New(nil)
	nm, cmd := m.Update(agentEventMsg{ev: events.Event{Type: events.EventNotice, Text: "（系统通知：后台进程 42 已退出（exit 0）。日志：x）"}})
	if cmd != nil {
		t.Fatal("EventNotice 不应产生 cmd")
	}
	m = nm.(Model)
	if len(m.msgs) == 0 || m.msgs[len(m.msgs)-1].Content == "" {
		t.Fatal("应追加系统行")
	}
}

// TestCompactDispatchBlocksWake 回归锚点（审查 01，2026-08-14）：/compact 分派
// 后、RunCompact cmd 执行前的间隙，completionWakeMsg 不得穿过 m.running 闸
// 拉起唤醒 run——否则与压缩并发读写同一 conversation（data race）。修复：
// /compact 分派同步置 running（RunCompact 的 setCancel 在异步 cmd 内，闸
// 必须更早落下），handleCompactDone 复位。
func TestCompactDispatchBlocksWake(t *testing.T) {
	var calls atomic.Int32
	c := newTestController(t, &calls)
	m := New(c)
	_ = collectSend(c)
	c.active.Completions().Append(completion.Event{Result: "x"})

	nm, cmd := m.handleInput("/compact")
	if cmd == nil {
		t.Fatal("/compact 应返回压缩 cmd")
	}
	m = nm.(Model)
	if !m.running {
		t.Fatal("/compact 分派应同步置 running（防 compact×wake 并发）")
	}
	// compact cmd 尚未执行（cancel 未抢占）：wake 消息被 m.running 闸丢弃。
	nm2, cmd2 := m.Update(completionWakeMsg{})
	m = nm2.(Model)
	if cmd2 != nil {
		t.Fatal("compact 期间 wake 消息不得拉起唤醒 run")
	}
	if c.active.Completions().PendingCount() != 1 {
		t.Error("被丢弃的 pending 应保留（留待下一次信号/用户消息注入）")
	}
	// compact 完成（测试装配无 compactor → err）：running 复位、无额外 run。
	done := cmd().(compactDoneMsg)
	nm3, cmd3 := m.Update(done)
	m = nm3.(Model)
	if m.running {
		t.Error("compact 完成后 running 应复位")
	}
	if cmd3 != nil {
		t.Errorf("compact 完成不应产生额外 cmd: %v", cmd3)
	}
	if calls.Load() != 0 {
		t.Errorf("不应有 agent.Run: %d", calls.Load())
	}
}

// TestWakeRunPreStartCancelNoInterruptNote 回归锚点（审查 05，2026-08-14）：
// MaybeWake 已同步抢占 cancel、wake cmd 尚未开跑时 Esc 打断——handleRunDone
// 不得写伪中断提示污染 conversation（run 未真正启动，无事发生）；pending
// 保留待下一次信号注入。
func TestWakeRunPreStartCancelNoInterruptNote(t *testing.T) {
	var calls atomic.Int32
	c := newTestController(t, &calls)
	_ = collectSend(c)
	c.active.Completions().Append(completion.Event{Result: "x"})
	m := New(c)
	m.width = 80

	// wake 消息 → MaybeWake 同步抢占 cancel + 返回 wake cmd（running 置位）。
	nm, cmd := m.Update(completionWakeMsg{})
	m = nm.(Model)
	if cmd == nil {
		t.Fatal("wake 应返回启动 cmd")
	}
	// cmd 尚未执行：Esc → requestInterrupt → cancelRun（cancel 已被抢占）。
	nm2, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = nm2.(Model)
	if !m.running {
		t.Fatal("Esc 前 running 应仍为 true")
	}

	// 执行被抢占的 wake cmd → runDoneMsg{wakeNotStarted}。
	raw := cmd()
	done, ok := raw.(runDoneMsg)
	if !ok {
		t.Fatalf("wake cmd 应返回 runDoneMsg，got %T", raw)
	}
	if !done.wakeNotStarted {
		t.Fatal("未开跑即中断的唤醒 run 应标记 wakeNotStarted")
	}

	before := len(c.active.Conversation().Messages)
	nm3, cmd3 := m.Update(done)
	m = nm3.(Model)
	if after := len(c.active.Conversation().Messages); after != before {
		t.Errorf("未开跑唤醒 run 不得写中断提示进 conversation（before=%d after=%d）", before, after)
	}
	if cmd3 != nil {
		t.Errorf("不应补唤醒（防热循环）: %v", cmd3)
	}
	if m.running || m.interrupted {
		t.Error("handleRunDone 后 running/interrupted 应复位")
	}
	if c.active.Completions().PendingCount() != 1 {
		t.Error("pending 应保留（留待下一次完成信号/用户消息注入）")
	}
	if calls.Load() != 0 {
		t.Errorf("agent.Run 不应被调用: %d", calls.Load())
	}
}

// --- 审查 06 测试补齐（2026-08-14）------------------------------------------------

// TestWakeRunStartedInterruptWritesNote 是审查 05 的对照锚点：唤醒 run 已真正
// 启动（FakeClient 阻塞至 ctx 取消）后 Esc 打断——handleRunDone 走正常中断
// 语义：conversation 写入中断提示（run 已启动，提示是真实的）。
func TestWakeRunStartedInterruptWritesNote(t *testing.T) {
	sess, proj := newTestSession(t)
	started := make(chan struct{})
	client := &provider.FakeClient{
		StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	c := NewController(agent.New(client, "test-model"), proj, config.Config{}, sess, nil, context.Background())
	m := New(c)
	_ = collectSend(c)
	c.active.Completions().Append(completion.Event{Result: "x"})

	nm, cmd := m.Update(completionWakeMsg{})
	m = nm.(Model)
	if cmd == nil {
		t.Fatal("wake 应返回启动 cmd")
	}
	// 执行 wake cmd（bubbletea 异步执行语义）：阻塞在 StreamFn。
	doneCh := make(chan tea.Msg, 1)
	go func() { doneCh <- cmd() }()
	<-started // run 已真正进入采样

	// Esc 打断在途唤醒 run。
	nm2, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = nm2.(Model)

	done := (<-doneCh).(runDoneMsg)
	if done.wakeNotStarted {
		t.Fatal("已启动的唤醒 run 不得标记 wakeNotStarted")
	}
	if !errors.Is(done.err, context.Canceled) {
		t.Fatalf("应返回 Canceled，got %v", done.err)
	}
	nm3, _ := m.Update(done)
	m = nm3.(Model)
	msgs := c.active.Conversation().Messages
	if len(msgs) == 0 || !strings.Contains(msgs[len(msgs)-1].Content, "interrupted") {
		t.Fatalf("已启动唤醒 run 的中断提示应写入 conversation: %+v", msgs)
	}
	if m.running {
		t.Error("handleRunDone 后 running 应复位")
	}
}

// TestWakeNonActiveSessionEventIgnored 验证审查 06：非 active 会话的完成事件
// 会发出唤起信号（completionWakeMsg），但 MaybeWake 查 active 的 pending →
// 空 → 丢弃（不打扰当前会话）；pending 保留在非 active 会话队列。
func TestWakeNonActiveSessionEventIgnored(t *testing.T) {
	c := newTestController(t, nil) // active = sess1
	collected := collectSend(c)
	sess2, err := c.proj.Create("test-model", c.proj.Path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer sess2.Close()
	c.open[sess2.ID] = sess2
	c.registerWake(sess2) // 非 active 会话同样挂唤起回调

	sess2.Completions().Append(completion.Event{Result: "x"})

	// 唤起信号确实发出。
	found := false
	for _, msg := range *collected {
		if _, ok := msg.(completionWakeMsg); ok {
			found = true
		}
	}
	if !found {
		t.Fatal("非 active 会话 Append 应触发 completionWakeMsg")
	}
	// MaybeWake 丢弃：active（sess1）无 pending。
	if cmd := c.MaybeWake(); cmd != nil {
		t.Fatal("非 active 会话事件不得拉起唤醒 run")
	}
	if sess2.Completions().PendingCount() != 1 {
		t.Error("pending 应保留在非 active 会话队列（切换过去后仍会注入）")
	}
}

// TestWakeSendAfterProgramExitSafe 验证审查 06：program 退出后完成事件 Append
// → wakeSignal → Send 为 no-op 不 panic（bubbletea v1.3.10 Send 的 ctx.Done
// 守卫 + "已终止 no-op" 语义，ADR-040 边界记录）。
func TestWakeSendAfterProgramExitSafe(t *testing.T) {
	sess, proj := newTestSession(t)
	c := NewController(nil, proj, config.Config{}, sess, nil, context.Background())
	m := New(c)
	p := tea.NewProgram(m, tea.WithContext(context.Background()))
	c.setSend(p.Send)

	go func() { _, _ = p.Run() }()
	p.Send(tea.Quit()) // 等 program 启动后退出
	p.Wait()           // 事件循环完全退出

	// 退出后：完成事件 Append → wake → Send（no-op），不 panic。
	c.active.Completions().Append(completion.Event{Result: "x"})
	c.active.Completions().Append(completion.Event{Result: "y"})
}

// TestCompactDoneWakesPending 验证架构整理另议项（2026-08-14）：compact 期间
// 被 m.running 闸丢弃的完成事件 pending，在 handleCompactDone 成功路径
// （compacted=true）补唤醒——与 handleRunDone 的 err==nil 补唤醒对称，延迟
// 不丢；err 路径不补（防热循环，同 handleRunDone 语义）。
func TestCompactDoneWakesPending(t *testing.T) {
	var calls atomic.Int32
	c := newTestController(t, &calls)
	m := New(c)
	_ = collectSend(c)
	c.active.Completions().Append(completion.Event{Result: "x"})

	// compact 分派同步置 running；期间 wake 消息被闸丢弃（同
	// TestCompactDispatchBlocksWake 前半）。
	nm, cmd := m.handleInput("/compact")
	if cmd == nil {
		t.Fatal("/compact 应返回压缩 cmd")
	}
	m = nm.(Model)
	nm2, cmd2 := m.Update(completionWakeMsg{})
	m = nm2.(Model)
	if cmd2 != nil {
		t.Fatal("compact 期间 wake 消息不得拉起唤醒 run")
	}
	if c.active.Completions().PendingCount() != 1 {
		t.Fatal("pending 应保留")
	}
	// compact 成功完成（直接喂完成消息，不执行真实压缩）。
	nm3, cmd3 := m.Update(compactDoneMsg{compacted: true})
	m = nm3.(Model)
	if cmd3 == nil {
		t.Fatal("compact 完成后应补唤醒 pending")
	}
	if !m.running {
		t.Fatal("补唤醒后 running 应置位")
	}
	// 执行唤醒 run：启动 1 次 agent.Run。
	_ = cmd3()
	if calls.Load() != 1 {
		t.Fatalf("应启动 1 次唤醒 run，got %d", calls.Load())
	}
}
