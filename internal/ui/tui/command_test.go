package tui

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/config"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/middleware/impl"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// cfgWithModels 构造带模型的配置（弹窗数据源测试；APIKey 供 Resolve）。
func cfgWithModels() config.Config {
	return config.Config{Providers: map[string]config.ProviderSpec{
		"p": {APIKey: "test-key", Models: map[string]config.Model{"m1": {}, "m2": {}}},
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
	if m.ovl == nil || m.ovl.sel.kind != popupModel {
		t.Fatalf("应打开模型弹窗，sel = %+v", m.ovl.sel)
	}
	if m.ovl.sel.items[0].value != "m1" {
		t.Fatalf("模型列表应实时来自配置，got %v", m.ovl.sel.items)
	}

	// ↓ 到 m2 → Enter 确认。
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = nm.(Model)
	if m.ovl.sel.cursor != 1 {
		t.Fatalf("↓ 后 cursor = %d, want 1", m.ovl.sel.cursor)
	}
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	if m.ovl != nil {
		t.Fatal("确认后弹窗应关闭")
	}
	if got := m.c.active.Model(); got != "m2" {
		t.Fatalf("SetModel 后模型 = %q, want m2", got)
	}
	if !strings.Contains(m.View(), "Model set to m2") {
		t.Fatalf("View 应含系统行")
	}
}

// TestCommandModelsOnlyCurrentProvider 验证 /model 弹窗只列当前 provider
// （default_provider）的模型——跨 provider 模型列出也选不中（Bug05）。
func TestCommandModelsOnlyCurrentProvider(t *testing.T) {
	c := newTestController(t, nil)
	c.cfg = config.Config{
		DefaultProvider: "alpha",
		Providers: map[string]config.ProviderSpec{
			"alpha": {APIKey: "k", Models: map[string]config.Model{"a1": {}, "a2": {}}},
			"beta":  {APIKey: "k", Models: map[string]config.Model{"b1": {}}},
		},
	}
	m := New(c)
	m.input.SetValue("/model")
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	if m.ovl == nil {
		t.Fatal("应打开模型弹窗")
	}
	var got []string
	for _, it := range m.ovl.sel.items {
		got = append(got, it.value)
	}
	if strings.Join(got, ",") != "a1,a2" {
		t.Fatalf("Models 应只列当前 provider 的模型，got %v", got)
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
	if m.ovl != nil {
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
	if m.ovl == nil || len(m.ovl.sel.items) != len(impl.Modes) {
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

// TestCommandThinkingPopup /thinking → 弹窗含 on/off + 确认切换（2026-08-10
// 删配置 enabled，thinking 默认开启，开关为会话级偏好）。
func TestCommandThinkingPopup(t *testing.T) {
	c := newTestController(t, nil)
	m := New(c)
	m.input.SetValue("/thinking")
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	if m.ovl == nil || len(m.ovl.sel.items) != 2 {
		t.Fatalf("thinking 弹窗应有 2 项，sel = %+v", m.ovl.sel)
	}
	// 默认开启 → current = enabled（光标在第一项）。
	if m.ovl.sel.cursor != 0 {
		t.Fatalf("默认 thinking 开启，cursor 应在 enabled，got %d", m.ovl.sel.cursor)
	}
	// ↓ 到 disabled → Enter 确认 → SetThinking(false) 持久化 AgentState。
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = nm.(Model)
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	st := m.c.active.State()
	if st.ThinkingEnabled == nil || *st.ThinkingEnabled {
		t.Fatalf("thinking 应切为 disabled，got %+v", st.ThinkingEnabled)
	}
	if !strings.Contains(m.View(), "Thinking disabled") {
		t.Fatalf("View 应含系统行")
	}
}

// TestCommandThinkingArg /thinking off 直接设（参数分支，对齐 /permission 双通道）。
func TestCommandThinkingArg(t *testing.T) {
	c := newTestController(t, nil)
	m := New(c)
	m.input.SetValue("/thinking off")
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	if m.ovl != nil {
		t.Fatal("带参数不应开弹窗")
	}
	st := m.c.active.State()
	if st.ThinkingEnabled == nil || *st.ThinkingEnabled {
		t.Fatalf("thinking 应关闭，got %+v", st.ThinkingEnabled)
	}
	if !strings.Contains(m.View(), "Thinking disabled") {
		t.Fatalf("View 应含系统行")
	}
}

// TestQueueCommand run 期间提交 / 命令 → 进队列；runDone 消费 → 弹窗（非发 agent）。
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

	// runDone → 消费 /model → 打开弹窗（不启动回合）。
	nm, cmd = m.Update(runDoneMsg{})
	m = nm.(Model)
	if m.ovl == nil {
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
	todoAt := strings.Index(v, "TODO")
	activeAt := strings.Index(v, "[>] 写代码")
	if todoAt < 0 || activeAt < 0 || !strings.Contains(v[todoAt:activeAt], "\n") || !strings.Contains(v, "[ ] 测试") {
		t.Fatalf("todo 条应含进行中项，got:\n%s", v)
	}
	if !strings.Contains(v, "1 active  1 pending  1 done") {
		t.Fatalf("todo 条应含统计小字")
	}
}

// TestQueueBarRender 队列条渲染（待发送内容可见）。
func TestQueueBarRender(t *testing.T) {
	m := New(nil)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = nm.(Model)
	m.queue = []string{"第二条", "/model"}
	if !strings.Contains(m.View(), "QUEUED  1  第二条") || !strings.Contains(m.View(), "2  /model") {
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
	m.ovl = &overlay{kind: overlaySelect, sel: &selectPopup{kind: popupModel, title: "切换模型", items: []popupItem{
		{label: "m1", value: "m1"},
		{label: "m2", value: "m2"},
	}}}
	v := m.View()
	if !strings.Contains(v, "切换模型") || !strings.Contains(v, "> m1") {
		t.Fatalf("弹窗应含标题与光标项，got:\n%s", v)
	}
}

// 弹窗内容宽度必须与外框一致：任何一行超过外框宽度都会在真实终端折行，
// 导致选项被拆成两行（modalStyle 的 Width 含 padding 但不含 border）。
func TestModalsFitPanelWidth(t *testing.T) {
	longLabel := "deepseek-v4-flash-preview-extra-long-model-name"
	cases := []struct {
		name  string
		lines int // 期望的外框总行数（含边框与上下 padding），折行会让它变大
		build func(screenWidth int) (panel string, width int)
	}{
		{"select", 8, func(w int) (string, int) {
			sel := &selectPopup{title: "MODELS", items: []popupItem{
				{label: "deepseek-v4-flash", value: "deepseek-v4-flash"},
				{label: longLabel, value: longLabel},
			}}
			return renderPopup(sel, w, 20), modalPanelWidth(w, 34, 64)
		}},
		// approval/help 的行数随宽度变化（提示竖排、帮助单列），这里只校验宽度。
		{"approval", 0, func(w int) (string, int) {
			appr := &approvalPopup{req: middleware.ApprovalRequest{
				ToolName: "shell_command",
				Summary:  "运行一个相当长的命令用于验证换行行为 " + longLabel,
			}}
			return renderApproval(appr, w), modalPanelWidth(w, 34, 76)
		}},
		{"help", 0, func(w int) (string, int) {
			return renderHelp(w), modalPanelWidth(w, 38, 78)
		}},
	}
	for _, screenWidth := range []int{30, 40, 56, 80, 120, 200} {
		for _, tc := range cases {
			panel, panelWidth := tc.build(screenWidth)
			if tc.lines > 0 {
				if got := lipgloss.Height(panel); got != tc.lines {
					t.Fatalf("%s@%d 弹窗 %d 行，期望 %d 行（多出的行说明内容被折行）：\n%s",
						tc.name, screenWidth, got, tc.lines, ansi.Strip(panel))
				}
			}
			for i, line := range strings.Split(panel, "\n") {
				if got := lipgloss.Width(line); got != panelWidth {
					t.Fatalf("%s@%d 第 %d 行宽度 %d，期望 %d：%q",
						tc.name, screenWidth, i, got, panelWidth, ansi.Strip(line))
				}
			}
			if panelWidth > maxInt(screenWidth, modalBorderWidth+modalPaddingWidth+1) {
				t.Fatalf("%s@%d 弹窗宽度 %d 超出屏幕", tc.name, screenWidth, panelWidth)
			}
		}
	}
}
