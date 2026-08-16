package subagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/completion"
	"github.com/agent-project/harness/internal/config"
	"github.com/agent-project/harness/internal/events"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/provider"
	"github.com/agent-project/harness/internal/session"
	"github.com/agent-project/harness/internal/tools"
)

// testHarness 构造测试 Manager + 父会话 rc（HARNESS_HOME 隔离用临时目录）。
func testHarness(t *testing.T, streamFn func(ctx context.Context, req provider.Request) (provider.EventStream, error)) (*Manager, *middleware.RuntimeContext, *completion.Queue) {
	t.Helper()
	root := t.TempDir()
	store := session.NewAt(root)
	proj := &session.Project{Path: root, Dir: store.ProjectDir(root)}
	parent, err := proj.Create("m1", root, "acceptedit")
	if err != nil {
		t.Fatalf("parent Create: %v", err)
	}
	t.Cleanup(func() { parent.Close() })

	fc := &provider.FakeClient{StreamFn: streamFn}
	m := NewManager(Options{
		Provider: &config.ProviderConfig{Model: "m1", ContextWindow: 200_000},
		Client:   fc,
	})
	// 等子 goroutine 全部收尾（Close writer）再清理临时目录。
	t.Cleanup(m.Shutdown)
	rc := parent.RuntimeContext()
	q := completion.New(filepath.Join(parent.Dir(), "completions.json"))
	rc.Completions = q
	return m, rc, q
}

// immediateStream 返回"一次采样即完成"的流（子 agent 无工具调用直接结束）。
func immediateStream(answer string) func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
	return func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return provider.NewFakeStream([]provider.Event{
			{Type: provider.EventTextDone, Text: answer},
			{Type: provider.EventDone},
		}), nil
	}
}

// hangingStream 返回真正挂起的流：阻塞直到 ctx 取消（中断/Shutdown 测试用）。
func hangingStream() func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
	return func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
}

// waitEvent 轮询父 Queue 直至收到 want 条完成事件（子 agent 完成通知）。
func waitEvent(t *testing.T, q *completion.Queue, timeout time.Duration) []completion.Event {
	t.Helper()
	var events []completion.Event
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		events = append(events, q.Drain()...)
		if len(events) > 0 {
			return events
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待子 agent 完成事件超时")
	return nil
}

// TestSpawnCompletesAndNotifies 验证完整链路：spawn → 子 agent（mock 立即完成）
// → Status=completed + 血缘落盘 + 通知（含 name/结果）进父 Queue。
func TestSpawnCompletesAndNotifies(t *testing.T) {
	m, rc, q := testHarness(t, immediateStream("分析完毕：目录含 3 个文件"))

	id, err := m.Spawn(rc, SpawnRequest{Name: "探查", Message: "分析这个目录", Type: KindGeneralPurpose})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if !strings.HasPrefix(id, "sub-") {
		t.Errorf("子 id 应以 sub- 开头: %q", id)
	}

	ev := waitEvent(t, q, 5*time.Second)
	if ev[0].SessionID != id {
		t.Errorf("通知 SessionID 应为子 id: %q", ev[0].SessionID)
	}
	if !strings.Contains(ev[0].Result, "探查") || !strings.Contains(ev[0].Result, "已完成") ||
		!strings.Contains(ev[0].Result, "分析完毕：目录含 3 个文件") {
		t.Errorf("完成通知: %q", ev[0].Result)
	}

	// 状态 + 血缘落盘（子会话 agentstate.json 跨进程可读）。
	if st := m.statusOf(id); st != StatusCompleted {
		t.Fatalf("运行态状态: %s", st)
	}
	subDir := filepath.Join(filepath.Dir(rc.StatePath), session.DirSubagents, id)
	st, err := agentstate.LoadFile(filepath.Join(subDir, session.FileAgentState))
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != StatusCompleted || st.ParentID != rc.SessionID || st.AgentType != KindGeneralPurpose || st.Depth != 1 {
		t.Errorf("血缘落盘: %+v", st)
	}
	if st.Name != "探查" {
		t.Errorf("name: %q", st.Name)
	}
	// 子会话 transcript 存在（首条 user = spawn message）。
	conv, err := session.LoadConversation(filepath.Join(subDir, session.DirHistorys))
	if err != nil || len(conv.Messages) == 0 || conv.Messages[0].Content != "分析这个目录" {
		t.Errorf("子会话起点 = spawn message: %v %+v", err, conv.Messages)
	}
}

// TestSpawnDepthLimit 验证嵌套深度上限（硬编码 2）：深度 2 的子 spawn → 拒绝。
func TestSpawnDepthLimit(t *testing.T) {
	m, rc, _ := testHarness(t, immediateStream("ok"))
	// 模拟深度 2 的当前 agent（主=0 → 1 → 2，第 3 层拒绝）。
	m.mu.Lock()
	m.entries["sub-depth2"] = &Entry{ID: "sub-depth2", Depth: 2}
	m.mu.Unlock()
	rc.SessionID = "sub-depth2"

	_, err := m.Spawn(rc, SpawnRequest{Message: "x"})
	var te *tools.ToolError
	if !errors.As(err, &te) || !te.RespondToModel {
		t.Fatalf("深度超限应回填错误: %v", err)
	}
	if !strings.Contains(te.Message, "上限") {
		t.Errorf("错误文案: %q", te.Message)
	}
}

// TestSendMessageStates 验证 send_message 仅运行中的子：completed → 报错引导
// resume；running → 事件进子会话 Queue（下轮采样前注入）。
func TestSendMessageStates(t *testing.T) {
	done := make(chan struct{})
	m, rc, _ := testHarness(t, func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		<-done // 挂起直到测试结束（保持 running）
		return nil, context.Canceled
	})
	id, err := m.Spawn(rc, SpawnRequest{Message: "任务"})
	if err != nil {
		t.Fatal(err)
	}
	// 等 running。
	deadline := time.Now().Add(3 * time.Second)
	for m.statusOf(id) != StatusRunning {
		if time.Now().After(deadline) {
			t.Fatal("子 agent 未进入 running")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// running：Send 成功 → 子会话 Queue 收到（滞留直到子下轮采样注入）。
	if err := m.Send(rc, id, "补充指示"); err != nil {
		t.Fatalf("Send running: %v", err)
	}
	e, _ := m.get(id)
	if got := e.session.Completions().Drain(); len(got) != 1 || !strings.Contains(got[0].Result, "补充指示") {
		t.Errorf("子会话队列应收到消息: %+v", got)
	}

	// 结束子（流返回 Canceled → 收尾为 interrupted）并等收尾。
	close(done)
	deadline2 := time.Now().Add(5 * time.Second)
	for m.statusOf(id) != StatusInterrupted {
		if time.Now().After(deadline2) {
			t.Fatalf("子 agent 未收尾（状态 %s）", m.statusOf(id))
		}
		time.Sleep(10 * time.Millisecond)
	}
	// 已结束后 Send → 报错引导 resume。
	if err := m.Send(rc, id, "再来一条"); err == nil {
		t.Error("已结束的子 Send 应报错")
	}
}

// TestInterruptSelfDenied 验证不能中断自己/父：子会话的 rc 中断自己 → 拒绝。
func TestInterruptSelfDenied(t *testing.T) {
	m, rc, _ := testHarness(t, hangingStream())
	id, err := m.Spawn(rc, SpawnRequest{Message: "任务"})
	if err != nil {
		t.Fatal(err)
	}
	// 等 running（cancel 已赋值——pending 窗口 Interrupt 会报"正在启动中"）。
	deadline := time.Now().Add(3 * time.Second)
	for m.statusOf(id) != StatusRunning {
		if time.Now().After(deadline) {
			t.Fatal("子 agent 未进入 running")
		}
		time.Sleep(10 * time.Millisecond)
	}
	e, _ := m.get(id)
	// 子的 rc（独立会话 RuntimeContext）——模拟子 agent 内部调用 interrupt_agent。
	subRC := e.session.RuntimeContext()
	if err := m.Interrupt(subRC, id); err == nil {
		t.Error("子中断自己应被拒绝")
	}
	// 父（主会话）中断子 → 允许。
	if err := m.Interrupt(rc, id); err != nil {
		t.Fatalf("父中断子应允许: %v", err)
	}
}

// TestInterruptNotifiesWithPartial 验证中断通知带中断前结果。
func TestInterruptNotifiesWithPartial(t *testing.T) {
	m, rc, q := testHarness(t, hangingStream())
	id, err := m.Spawn(rc, SpawnRequest{Name: "慢任务", Message: "慢慢分析"})
	if err != nil {
		t.Fatal(err)
	}
	// 等 running。
	deadline := time.Now().Add(3 * time.Second)
	for m.statusOf(id) != StatusRunning {
		if time.Now().After(deadline) {
			t.Fatal("未进入 running")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := m.Interrupt(rc, id); err != nil {
		t.Fatal(err)
	}
	ev := waitEvent(t, q, 5*time.Second)
	if !strings.Contains(ev[0].Result, "已中断") || !strings.Contains(ev[0].Result, "慢任务") ||
		!strings.Contains(ev[0].Result, "无") {
		t.Errorf("中断通知应带中断前结果（挂起流无产出 = 无）: %q", ev[0].Result)
	}
}

// TestSameKindAgentCached 验证同 kind 装配实例缓存共享（buildAgent 只装配一次）。
func TestSameKindAgentCached(t *testing.T) {
	m, _, _ := testHarness(t, immediateStream("ok"))
	a1, err := m.buildAgent(KindGeneralPurpose)
	if err != nil {
		t.Fatal(err)
	}
	a2, err := m.buildAgent(KindGeneralPurpose)
	if err != nil {
		t.Fatal(err)
	}
	if a1 != a2 {
		t.Error("同 kind 应共享同一实例")
	}
	// explore 与 general-purpose 不同实例；工具集不同。
	e1, _ := m.buildAgent(KindExplore)
	if e1 == a1 {
		t.Error("不同 kind 应不同实例")
	}
	if len(m.toolset(KindExplore)) != 4 {
		t.Errorf("explore 应只读 4 工具: %d", len(m.toolset(KindExplore)))
	}
	for _, t2 := range m.toolset(KindExplore) {
		n := t2.Name()
		if n != "read_file" && n != "list_dir" && n != "glob" && n != "skill" {
			t.Errorf("explore 含非只读工具: %s", n)
		}
	}
	for _, t2 := range m.toolset(KindGeneralPurpose) {
		if t2.Name() == "ask_user" {
			t.Error("general-purpose 不应含 ask_user")
		}
		if t2.Name() == "wait_task" {
			_ = t2 // 应有（子专属）
		}
	}
}

// TestSubscribeDispatch 验证子事件订阅（TUI 查看模式）。
func TestSubscribeDispatch(t *testing.T) {
	m, rc, _ := testHarness(t, immediateStream("ok"))
	id, _ := m.Spawn(rc, SpawnRequest{Message: "x"})
	var got []events.Event
	fn := func(ev events.Event) { got = append(got, ev) }
	m.Subscribe(id, fn)
	m.dispatch(id, events.Event{Type: events.EventTurnStart})
	if len(got) != 1 || got[0].Type != events.EventTurnStart {
		t.Errorf("订阅未收到: %+v", got)
	}
	m.Unsubscribe(id, fn)
	m.dispatch(id, events.Event{Type: events.EventTurnStart})
	if len(got) != 1 {
		t.Errorf("退订后不应再收: %+v", got)
	}
}

// TestShutdown 验证进程退出清理：cancel 全部 + 等收尾（幂等）。
func TestShutdown(t *testing.T) {
	m, rc, q := testHarness(t, hangingStream())
	id, _ := m.Spawn(rc, SpawnRequest{Message: "x"})
	deadline := time.Now().Add(3 * time.Second)
	for m.statusOf(id) != StatusRunning {
		if time.Now().After(deadline) {
			t.Fatal("未进入 running")
		}
		time.Sleep(10 * time.Millisecond)
	}
	m.Shutdown()
	m.Shutdown() // 幂等
	if st := m.statusOf(id); st != StatusInterrupted {
		t.Errorf("Shutdown 后子应中断: %s", st)
	}
	// 收尾通知已进父 Queue（resume 后补注入）。
	if got := q.Drain(); len(got) != 1 {
		t.Errorf("Shutdown 收尾应通知: %+v", got)
	}
}

// TestResumeOnlyDirectChild 验证 resume 仅直属子。
func TestResumeOnlyDirectChild(t *testing.T) {
	m, rc, q := testHarness(t, immediateStream("第一次完成"))
	id, _ := m.Spawn(rc, SpawnRequest{Message: "x"})
	waitEvent(t, q, 5*time.Second)

	// 非直属（模拟孙会话 id 不在注册表）：任意会话调 resume → 未找到。
	otherRC := middleware.NewRuntimeContext()
	otherRC.SessionID = "unrelated"
	if err := m.Resume(otherRC, id, "继续"); err == nil {
		t.Error("非直属 resume 应报错")
	}
	// 直属：父 resume → 再次执行 → 完成通知再来一条（多轮委托）。
	if err := m.Resume(rc, id, "继续做第二步"); err != nil {
		t.Fatalf("直属 resume: %v", err)
	}
	ev := waitEvent(t, q, 5*time.Second)
	if !strings.Contains(ev[0].Result, "第一次完成") {
		t.Errorf("resume 后完成通知: %q", ev[0].Result)
	}
}

// TestListMergesDisk 验证 list 合并运行态 + 磁盘历史。
func TestListMergesDisk(t *testing.T) {
	m, rc, q := testHarness(t, immediateStream("ok"))
	id, _ := m.Spawn(rc, SpawnRequest{Message: "x"})
	waitEvent(t, q, 5*time.Second)
	views := m.List(rc)
	if len(views) != 1 || views[0].ID != id || views[0].Status != StatusCompleted {
		t.Fatalf("List: %+v", views)
	}
	// 模拟进程重启（新 Manager 扫磁盘恢复历史）。
	m2 := NewManager(Options{Provider: &config.ProviderConfig{Model: "m1", ContextWindow: 200_000}})
	views2 := m2.List(rc)
	if len(views2) != 1 || views2[0].ID != id || views2[0].Status != StatusCompleted || views2[0].Running {
		t.Fatalf("磁盘历史恢复: %+v", views2)
	}
	// 子会话目录存在。
	if _, err := os.Stat(filepath.Join(filepath.Dir(rc.StatePath), session.DirSubagents, id)); err != nil {
		t.Errorf("子会话目录: %v", err)
	}
}

// TestSendOnlyDirectChild 验证 send_message 仅直属子（2026-08-16 修复 P1a）：
// 主会话给两个直属子均可发（各自都是直属）；子 agent 给兄弟/无关 agent 发拒绝；
// 无关会话拒绝。
func TestSendOnlyDirectChild(t *testing.T) {
	m, rc, _ := testHarness(t, hangingStream())
	id1, err := m.Spawn(rc, SpawnRequest{Message: "任务1"})
	if err != nil {
		t.Fatal(err)
	}
	waitRunning(t, m, id1)
	// 主会话给直属子1：允许。
	if err := m.Send(rc, id1, "补充1"); err != nil {
		t.Fatalf("直属 Send 应允许: %v", err)
	}
	id2, err := m.Spawn(rc, SpawnRequest{Message: "任务2"})
	if err != nil {
		t.Fatal(err)
	}
	waitRunning(t, m, id2)
	// 主会话给直属子2：也允许（各子都是主会话直属）。
	if err := m.Send(rc, id2, "补充2"); err != nil {
		t.Fatalf("直属 Send 应允许: %v", err)
	}
	// 子1 给兄弟（子2）：拒绝（子2 的父是主会话，不是子1）。
	e1, _ := m.get(id1)
	subRC1 := e1.session.RuntimeContext()
	if err := m.Send(subRC1, id2, "发给兄弟"); err == nil {
		t.Error("子给兄弟 Send 应拒绝")
	}
	// 无关会话：拒绝。
	otherRC := middleware.NewRuntimeContext()
	otherRC.SessionID = "unrelated"
	if err := m.Send(otherRC, id1, "无关会话发"); err == nil {
		t.Error("无关会话 Send 应拒绝")
	}
}

// TestInterruptOnlyDescendant 验证 interrupt 仅后代（2026-08-16 修复 P1a）：
// 兄弟拒绝；父（主会话）可中断任意后代；子可中断自己的子（孙）。
func TestInterruptOnlyDescendant(t *testing.T) {
	m, rc, _ := testHarness(t, hangingStream())
	id1, err := m.Spawn(rc, SpawnRequest{Message: "任务1"})
	if err != nil {
		t.Fatal(err)
	}
	waitRunning(t, m, id1)
	id2, err := m.Spawn(rc, SpawnRequest{Message: "任务2"})
	if err != nil {
		t.Fatal(err)
	}
	waitRunning(t, m, id2)

	// 子1 中断兄弟（子2）：拒绝。
	e1, _ := m.get(id1)
	subRC1 := e1.session.RuntimeContext()
	if err := m.Interrupt(subRC1, id2); err == nil {
		t.Error("子中断兄弟应拒绝")
	}
	// 主会话中断两个子：允许。
	if err := m.Interrupt(rc, id1); err != nil {
		t.Fatalf("主中断子1: %v", err)
	}
	if err := m.Interrupt(rc, id2); err != nil {
		t.Fatalf("主中断子2: %v", err)
	}
}

// waitRunning 等子进入 running（测试 helper）。
func waitRunning(t *testing.T, m *Manager, id string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for m.statusOf(id) != StatusRunning {
		if time.Now().After(deadline) {
			t.Fatalf("子 %s 未进入 running", id)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestResumeAfterRestart 验证进程重启后磁盘恢复 resume（2026-08-16 修复
// P1b）：新 Manager（无内存 entry）经 List 看到历史子 → Resume 磁盘加载续跑
// → 完成通知再次注入。
func TestResumeAfterRestart(t *testing.T) {
	m, rc, q := testHarness(t, immediateStream("第一次完成"))
	id, _ := m.Spawn(rc, SpawnRequest{Message: "x"})
	waitEvent(t, q, 5*time.Second)

	// 模拟进程重启：全新 Manager（无 entries），同一父 rc。
	m2 := NewManager(Options{Provider: &config.ProviderConfig{Model: "m1", ContextWindow: 200_000},
		Client: &provider.FakeClient{StreamFn: immediateStream("第二次完成")}})
	rc.Completions = q // 同一父队列（磁盘恢复后完成通知注入同一处）
	t.Cleanup(m2.Shutdown)

	// 磁盘可见（血缘校验通过）。
	views := m2.List(rc)
	if len(views) != 1 || views[0].ID != id {
		t.Fatalf("重启后 List: %+v", views)
	}
	// Resume：内存未命中 → 磁盘加载续跑。
	if err := m2.Resume(rc, id, "继续第二步"); err != nil {
		t.Fatalf("重启后 Resume: %v", err)
	}
	ev := waitEvent(t, q, 5*time.Second)
	if !strings.Contains(ev[0].Result, "第二次完成") {
		t.Errorf("重启后完成通知: %q", ev[0].Result)
	}
	if st := m2.statusOf(id); st != StatusCompleted {
		t.Errorf("重启后状态: %s", st)
	}
}
