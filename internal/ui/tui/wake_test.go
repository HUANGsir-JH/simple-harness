package tui

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/agent-project/harness/internal/agent"
	"github.com/agent-project/harness/internal/completion"
	"github.com/agent-project/harness/internal/config"
	"github.com/agent-project/harness/internal/events"
	"github.com/agent-project/harness/internal/provider"
	"github.com/agent-project/harness/internal/session"
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
