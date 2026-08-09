package tui

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/agent-project/harness/internal/agent"
	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/provider"
	"github.com/agent-project/harness/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

// newTestSession 建临时 workspace + 会话（HARNESS_HOME 隔离）。
func newTestSession(t *testing.T) (*session.Session, *session.Project) {
	t.Helper()
	root := t.TempDir()
	store := session.NewAt(root)
	proj := &session.Project{Path: root, Dir: store.ProjectDir(root)}
	sess, err := proj.Create("test-model", root, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess, proj
}

// newTestController 构造带 FakeClient 的桥（每次 Stream 返回固定纯文本回合）。
func newTestController(t *testing.T, calls *atomic.Int32) *Controller {
	t.Helper()
	sess, proj := newTestSession(t)
	client := &provider.FakeClient{
		StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
			if calls != nil {
				calls.Add(1)
			}
			return provider.NewFakeStream([]provider.Event{
				{Type: provider.EventTextDelta, Text: "回复"},
				{Type: provider.EventTextDone, Text: "回复"},
				{Type: provider.EventDone},
			}), nil
		},
	}
	return NewController(agent.New(client, "test-model"), proj, provider.Config{}, sess, context.Background())
}

// collectSend 收集 program.Send 的消息（模拟 bubbletea 事件循环）。
func collectSend(c *Controller) *[]tea.Msg {
	msgs := &[]tea.Msg{}
	c.setSend(func(msg tea.Msg) { *msgs = append(*msgs, msg) })
	return msgs
}

// TestTurnFlowText 完整回合：提交 → agent.Run（FakeClient 纯文本）→ 事件桥
// → msgs 收到 md 渲染的 assistant 消息。
func TestTurnFlowText(t *testing.T) {
	c := newTestController(t, nil)
	m := New(c)
	m.width = 80
	collected := collectSend(c)

	m.input.SetValue("你好")
	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("提交应返回启动回合的 cmd")
	}
	_ = cmd() // 执行回合（同步；事件经 send 收集）
	m = nm.(Model)

	// 把收集的 agentEventMsg 逐个喂 Update（模拟事件循环）。
	for _, msg := range *collected {
		nm, _ := m.Update(msg)
		m = nm.(Model)
	}

	// 找 assistant 完成消息。
	var found *MessageItem
	for _, it := range m.msgs {
		if it.Role == messages.RoleAssistant && it.Done {
			found = it
		}
	}
	if found == nil {
		t.Fatalf("msgs 应有 assistant 完成消息，got %+v", m.msgs)
	}
	if found.Content != "回复" {
		t.Fatalf("Content = %q, want %q", found.Content, "回复")
	}
	if found.Rendered == "" || found.Rendered == found.Content {
		t.Fatalf("Rendered 应为 md 渲染结果（≠原始文本），got %q", found.Rendered)
	}
}

// TestQueueWhileRunning run 期间提交进队列，不立即启动回合。
func TestQueueWhileRunning(t *testing.T) {
	c := newTestController(t, nil)
	m := New(c)
	m.running = true // 模拟回合进行中

	m.input.SetValue("second")
	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("running 中提交不应立即启动回合")
	}
	m = nm.(Model)
	if len(m.queue) != 1 || m.queue[0] != "second" {
		t.Fatalf("queue = %v, want [second]", m.queue)
	}
}

// TestQueueAutoRun runDone 后队列自动启动下一条（逐条连跑）。
func TestQueueAutoRun(t *testing.T) {
	var calls atomic.Int32
	c := newTestController(t, &calls)
	m := New(c)
	m.queue = []string{"second"}

	nm, cmd := m.Update(runDoneMsg{})
	m = nm.(Model)
	if cmd == nil {
		t.Fatal("queue 非空时 runDone 应自动启动下一条")
	}
	if len(m.queue) != 0 {
		t.Fatalf("queue 应清空，got %v", m.queue)
	}
	_ = cmd() // 执行下一条回合
	if calls.Load() != 1 {
		t.Fatalf("应执行 1 次 agent.Run，got %d", calls.Load())
	}
}

func TestTurnDoneDoesNotRaceNextRun(t *testing.T) {
	m := New(nil)
	m.running = true
	m.queue = []string{"second"}
	nm, cmd := m.Update(agentEventMsg{ev: agent.Event{Type: agent.EventTurnDone}})
	m = nm.(Model)
	if cmd != nil || !m.running || len(m.queue) != 1 {
		t.Fatalf("turn_done must not consume queue: running=%v queue=%v", m.running, m.queue)
	}
}

// TestStreamDeltaAccumulate 流式 delta 累积，块完成 flush 成消息。
func TestStreamDeltaAccumulate(t *testing.T) {
	m := New(nil)
	m.width = 80

	feed := func(ev agent.Event) {
		nm, _ := m.Update(agentEventMsg{ev: ev})
		m = nm.(Model)
	}
	feed(agent.Event{Type: agent.EventTurnStart})
	feed(agent.Event{Type: agent.EventTextDelta, Text: "foo"})
	feed(agent.Event{Type: agent.EventTextDelta, Text: "bar"})
	if m.stream == nil || m.stream.Text != "foobar" {
		t.Fatalf("stream.Text = %+v, want foobar", m.stream)
	}
	feed(agent.Event{Type: agent.EventTextDone, Text: "foobar"})
	if m.stream != nil {
		t.Fatalf("块完成后 stream 应清空")
	}
	if len(m.msgs) != 1 || m.msgs[0].Content != "foobar" {
		t.Fatalf("msgs = %+v, want [foobar]", m.msgs)
	}
}

// TestThinkingFlush thinking + text 同块：完成时合并进消息。
func TestThinkingFlush(t *testing.T) {
	m := New(nil)
	m.width = 80

	feed := func(ev agent.Event) {
		nm, _ := m.Update(agentEventMsg{ev: ev})
		m = nm.(Model)
	}
	feed(agent.Event{Type: agent.EventThinkingDelta, Text: "想想"})
	feed(agent.Event{Type: agent.EventThinkingDone, Text: "想想"})
	feed(agent.Event{Type: agent.EventTextDelta, Text: "答案"})
	feed(agent.Event{Type: agent.EventTextDone, Text: "答案"})

	if len(m.msgs) != 1 || m.msgs[0].Thinking != "想想" {
		t.Fatalf("msgs[0] = %+v, want Thinking=想想", m.msgs)
	}
}
