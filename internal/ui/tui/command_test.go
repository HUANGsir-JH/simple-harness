package tui

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agent-project/harness/internal/agent"
	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/middleware/impl"
	"github.com/agent-project/harness/internal/provider"
	tea "github.com/charmbracelet/bubbletea"
)

// cfgWithModels 构造带模型的配置（弹窗数据源测试；APIKey 供 Resolve）。
func cfgWithModels() provider.Config {
	return provider.Config{Providers: map[string]provider.ProviderConfig{
		"p": {APIKey: "test-key", Models: map[string]provider.Model{"m1": {}, "m2": {}}},
	}}
}

// TestCommandPopupModel /model → 弹窗（↑↓ 选 + Enter 确认 → SetModel + 系统行）。
func TestCommandPopupModel(t *testing.T) {
	c := newTestController(t, nil)
	c.cfg = cfgWithModels()
	m := New(c)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = nm.(Model)

	m.input.SetValue("/model")
	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	if cmd != nil {
		t.Fatal("命令弹窗不应返回回合 cmd")
	}
	if m.sel == nil || m.sel.kind != popupModel {
		t.Fatalf("应打开模型弹窗，sel = %+v", m.sel)
	}
	if m.sel.items[0].value != "m1" {
		t.Fatalf("模型列表应实时来自配置，got %v", m.sel.items)
	}

	// ↓ 到 m2 → Enter 确认。
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = nm.(Model)
	if m.sel.cursor != 1 {
		t.Fatalf("↓ 后 cursor = %d, want 1", m.sel.cursor)
	}
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	if m.sel != nil {
		t.Fatal("确认后弹窗应关闭")
	}
	if got := m.c.active.Model(); got != "m2" {
		t.Fatalf("SetModel 后模型 = %q, want m2", got)
	}
	if !strings.Contains(m.View(), "已切换模型 m2") {
		t.Fatalf("View 应含系统行")
	}
}

// TestCommandPopupEsc Esc 取消弹窗（不执行）。
func TestCommandPopupEsc(t *testing.T) {
	c := newTestController(t, nil)
	c.cfg = cfgWithModels()
	m := New(c)
	m.input.SetValue("/model")
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = nm.(Model)
	if m.sel != nil {
		t.Fatal("Esc 应关闭弹窗")
	}
	if m.c.active.Model() != "test-model" {
		t.Fatalf("Esc 不应执行切换，model = %q", m.c.active.Model())
	}
}

// TestCommandPermissionPopup /permission → 弹窗含三档 + 确认切换模式。
func TestCommandPermissionPopup(t *testing.T) {
	c := newTestController(t, nil)
	m := New(c)
	m.input.SetValue("/permission")
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	if m.sel == nil || len(m.sel.items) != len(impl.Modes) {
		t.Fatalf("权限弹窗应有 %d 项", len(impl.Modes))
	}
	// 选 readonly（第一项）。
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	st := m.c.active.State()
	if st.Permission == nil || st.Permission.Mode != "readonly" {
		t.Fatalf("权限应切为 readonly，got %+v", st.Permission)
	}
}

// TestQueueCommand run 期间提交 / 命令 → 进队列；turn_done 消费 → 弹窗（非发 agent）。
func TestQueueCommand(t *testing.T) {
	var calls atomic.Int32
	c := newTestController(t, &calls)
	c.cfg = cfgWithModels()
	m := New(c)
	m.running = true

	// 回合中提交 /model → 进队列。
	m.input.SetValue("/model")
	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	if cmd != nil {
		t.Fatal("running 中提交不应立即执行")
	}
	if len(m.queue) != 1 || m.queue[0] != "/model" {
		t.Fatalf("queue = %v, want [/model]", m.queue)
	}

	// turn_done → 消费 /model → 打开弹窗（不启动回合）。
	nm, cmd = m.Update(agentEventMsg{ev: agent.Event{Type: agent.EventTurnDone}})
	m = nm.(Model)
	if m.sel == nil {
		t.Fatal("turn_done 消费 /model 应打开弹窗")
	}
	if cmd != nil {
		t.Fatal("命令消费不应启动 agent 回合")
	}
	if calls.Load() != 0 {
		t.Fatalf("不应执行 agent.Run，got %d", calls.Load())
	}
}

// TestTodoBarRender todo 常驻条渲染（≤5 项 + 统计小字）。
func TestTodoBarRender(t *testing.T) {
	c := newTestController(t, nil)
	m := New(c)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = nm.(Model)
	// 直接塞状态栏缓存（refresh 会从 session 读，测试用假数据）。
	m.status.Todos = sortTodos([]agentstate.TodoItem{
		{Position: 1, Description: "写代码", Status: "in_progress"},
		{Position: 2, Description: "测试", Status: "pending"},
		{Position: 3, Description: "提交", Status: "completed"},
	})
	v := m.View()
	if !strings.Contains(v, "todo  ") || !strings.Contains(v, "[>] 写代码") {
		t.Fatalf("todo 条应含进行中项，got:\n%s", v)
	}
	if !strings.Contains(v, "1 完成 · 1 进行中 · 1 待办") {
		t.Fatalf("todo 条应含统计小字")
	}
}

// TestQueueBarRender 队列条渲染（待发送内容可见）。
func TestQueueBarRender(t *testing.T) {
	m := New(nil)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = nm.(Model)
	m.queue = []string{"第二条", "/model"}
	if !strings.Contains(m.View(), "待发送: 第二条 | /model") {
		t.Fatalf("队列条应含待发送内容")
	}
}

// TestCommandPersist 斜杠命令执行时落盘（transcript command 行，resume 可读）。
func TestCommandPersist(t *testing.T) {
	c := newTestController(t, nil)
	c.cfg = cfgWithModels()
	m := New(c)
	m.input.SetValue("/model")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	cmds, err := c.active.Commands()
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 1 || cmds[0] != "/model" {
		t.Fatalf("Commands = %v, want [/model]", cmds)
	}
}

// TestPopupRender 弹窗渲染含标题与当前项高亮。
func TestPopupRender(t *testing.T) {
	m := New(nil)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = nm.(Model)
	m.sel = &selectPopup{kind: popupModel, title: "切换模型", items: []popupItem{
		{label: "m1", value: "m1"},
		{label: "m2", value: "m2"},
	}}
	v := m.View()
	if !strings.Contains(v, "切换模型") || !strings.Contains(v, "> m1") {
		t.Fatalf("弹窗应含标题与光标项，got:\n%s", v)
	}
}
