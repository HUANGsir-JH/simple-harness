package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agent-project/harness/internal/completion"
	"github.com/agent-project/harness/internal/config"
	"github.com/agent-project/harness/internal/events"
	"github.com/agent-project/harness/internal/provider"
	"github.com/agent-project/harness/internal/session"
	"github.com/agent-project/harness/internal/subagent"
	tea "github.com/charmbracelet/bubbletea"
)

// subagentTestHarness 构造父会话 + 子 agent Manager（mock 立即完成的子）。
// 返回：Controller（已 SetSubagents + setSend 收集）+ 子 id + send 收集器。
func subagentTestHarness(t *testing.T) (*Controller, string, *[]tea.Msg) {
	t.Helper()
	root := t.TempDir()
	store := session.NewAt(root)
	proj := &session.Project{Path: root, Dir: store.ProjectDir(root)}
	parent, err := proj.Create("m1", root, "acceptedit")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { parent.Close() })

	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return provider.NewFakeStream([]provider.Event{
			{Type: provider.EventTextDone, Text: "子任务结果"},
			{Type: provider.EventDone},
		}), nil
	}}
	m := subagent.NewManager(subagent.Options{
		Provider: &config.ProviderConfig{Model: "m1", ContextWindow: 200_000},
		Client:   fc,
	})
	t.Cleanup(m.Shutdown)

	c := NewController(nil, proj, config.Config{}, parent, nil, context.Background())
	c.SetSubagents(m)
	var sent []tea.Msg
	c.setSend(func(msg tea.Msg) { sent = append(sent, msg) })

	rc := parent.RuntimeContext()
	q := completion.New(t.TempDir() + "/completions.json")
	rc.Completions = q
	id, err := m.Spawn(rc, subagent.SpawnRequest{Name: "探查", Message: "分析目录"})
	if err != nil {
		t.Fatal(err)
	}
	// 等子完成（收尾 close 后目录可读）。
	deadline := time.Now().Add(5 * time.Second)
	for m.List(rc) == nil || len(m.List(rc)) == 0 || m.List(rc)[0].Status != subagent.StatusCompleted {
		if time.Now().After(deadline) {
			t.Fatal("子 agent 未完成")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return c, id, &sent
}

// subagentEvent 构造一个子事件（转发断言用）。
func subagentEvent() events.Event {
	return events.Event{Type: events.EventTextDone, MsgID: "sub-m1", Text: "子输出"}
}

// TestControllerViewSubagent 验证查看模式：ViewSubagent 切 active 到子会话 +
// 订阅 + 事件转发；ExitSubagentView 退订 + 切回父。
func TestControllerViewSubagent(t *testing.T) {
	c, id, sent := subagentTestHarness(t)
	parentID := c.ActiveID()

	// 进入查看。
	if err := c.ViewSubagent(id); err != nil {
		t.Fatalf("ViewSubagent: %v", err)
	}
	if !c.IsViewingSubagent() || c.ActiveID() != id {
		t.Fatalf("查看模式: active=%s viewing=%v", c.ActiveID(), c.IsViewingSubagent())
	}
	// 子会话历史可加载（磁盘恢复 + timeline 数据源）。
	if lines, _, err := c.active.TranscriptLines(); err != nil || len(lines) == 0 {
		t.Errorf("子会话 transcript: %v %d", err, len(lines))
	}

	// 子事件经订阅转发渲染（send 收集）。
	before := len(*sent)
	c.viewSubFn(subagentEvent())
	if len(*sent) != before+1 {
		t.Errorf("查看中应转发子事件")
	}

	// 退出查看。
	c.ExitSubagentView()
	if c.IsViewingSubagent() || c.ActiveID() != parentID {
		t.Fatalf("退出查看: active=%s viewing=%v", c.ActiveID(), c.IsViewingSubagent())
	}
	// 退出后已退订清理（viewSubFn 置 nil；Unsubscribe 由 ExitSubagentView 内
	// 经 Manager 完成——subagent 包测试覆盖退订语义）。
	before = len(*sent)
	c.mu.Lock()
	fnCleared := c.viewSubFn == nil && c.viewSubID == ""
	c.mu.Unlock()
	if !fnCleared {
		t.Error("退出后查看字段应清理")
	}
	_ = before
	// 幂等。
	c.ExitSubagentView()
}

// TestControllerListSubagents 验证 /subagents 列表（运行态 + 磁盘历史合并）。
func TestControllerListSubagents(t *testing.T) {
	c, id, _ := subagentTestHarness(t)
	views := c.ListSubagents()
	if len(views) != 1 || views[0].ID != id {
		t.Fatalf("ListSubagents: %+v", views)
	}
	if !strings.Contains(views[0].Name, "探查") {
		t.Errorf("name: %+v", views[0])
	}
}

// TestModelViewingSubagentDisablesInput 验证只读查看时输入禁用（打字/回车无效）。
func TestModelViewingSubagentDisablesInput(t *testing.T) {
	m := New(nil)
	m.viewingSubagent = true
	m.input.SetValue("hello")
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = nm.(Model)
	if got := m.input.Value(); got != "hello" {
		t.Errorf("查看模式输入不应变化: %q", got)
	}
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	if got := m.input.Value(); got != "hello" {
		t.Errorf("查看模式回车不应提交: %q", got)
	}
}

// TestModelViewingSubagentEscExits 验证 Esc 退出查看（2026-08-16 修复：输入框
// 禁用导致 /switch 无法输入、查看模式困死——Esc 是唯一不依赖输入的退出路径）：
// Esc → 退出查看 + active 回父会话 + controller 查看状态清除；c == nil 时安全
// 清 flag 不 panic。
func TestModelViewingSubagentEscExits(t *testing.T) {
	c, id, _ := subagentTestHarness(t)
	parentID := c.ActiveID()
	m := New(c)
	if err := c.ViewSubagent(id); err != nil {
		t.Fatalf("ViewSubagent: %v", err)
	}
	m.viewingSubagent = true

	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = nm.(Model)
	if m.viewingSubagent || c.IsViewingSubagent() {
		t.Error("Esc 应退出查看模式")
	}
	if c.ActiveID() != parentID {
		t.Errorf("active 应回父会话: %s", c.ActiveID())
	}

	// c == nil（无 controller 的纯模型）：安全清 flag 不 panic。
	m2 := New(nil)
	m2.viewingSubagent = true
	nm2, _ := m2.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if nm2.(Model).viewingSubagent {
		t.Error("c==nil 时 Esc 也应清查看标志")
	}
}
