package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-project/harness/internal/middleware"
	tea "github.com/charmbracelet/bubbletea"
)

// TestPlanCommandToggle /plan 无参 toggle：on → PlanMode=true + 注入 PlanInstructions
// 一次 + 状态栏 [PLAN]；再触发 → off。
func TestPlanCommandToggle(t *testing.T) {
	c := newTestController(t, nil)
	m := New(c)
	m.width, m.height = 80, 24

	// 第一次 /plan → on。
	m.input.SetValue("/plan")
	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	if cmd != nil {
		t.Fatal("命令不应返回回合 cmd")
	}
	if !c.active.State().PlanMode {
		t.Error("/plan 后应进入 plan 模式")
	}
	conv := c.active.Conversation().Messages
	if len(conv) == 0 || !strings.Contains(conv[0].Content, "Plan 模式已激活") {
		t.Errorf("进入 plan 模式应注入 PlanInstructions，conv 首条 = %.40q", conv[0].Content)
	}
	if !strings.Contains(m.View(), "[PLAN]") {
		t.Error("状态栏应有 [PLAN] 标记")
	}

	// 第二次 /plan → off。
	m.input.SetValue("/plan")
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	if c.active.State().PlanMode {
		t.Error("再次 /plan 应关闭 plan 模式")
	}
	if strings.Contains(m.View(), "[PLAN]") {
		t.Error("关闭后状态栏应无 [PLAN]")
	}
}

// TestPlanCommandExplicitArg /plan on|off 显式参数。
func TestPlanCommandExplicitArg(t *testing.T) {
	c := newTestController(t, nil)
	m := New(c)
	m.width, m.height = 80, 24

	m.input.SetValue("/plan on")
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	if !c.active.State().PlanMode {
		t.Error("/plan on 应开启")
	}
	m.input.SetValue("/plan off")
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	if c.active.State().PlanMode {
		t.Error("/plan off 应关闭")
	}
	// 非法参数 → 系统错误行。
	m.input.SetValue("/plan bogus")
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	if !strings.Contains(m.View(), "unknown /plan arg") {
		t.Error("非法参数应报错")
	}
}

// TestPlanView /plan view 显示计划文件内容；无计划文件给提示。
func TestPlanView(t *testing.T) {
	c := newTestController(t, nil)
	m := New(c)
	m.width, m.height = 80, 24

	// 未写计划 → 提示。
	m.input.SetValue("/plan view")
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	if !strings.Contains(m.View(), "暂无计划文件") {
		t.Error("无计划文件应提示")
	}

	// 写入计划文件（直接写默认路径）。
	planPath := c.active.PlanFile()
	if err := os.MkdirAll(filepath.Dir(planPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath, []byte("## 实施计划\n1. 改 a.go"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.input.SetValue("/plan view")
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	if !strings.Contains(m.View(), "## 实施计划") {
		t.Error("/plan view 应显示计划内容")
	}
}

// TestAskModalSelection 单选：弹窗打开 → ↓ 到 B → Enter 提交 Selection[B]。
func TestAskModalSelection(t *testing.T) {
	c := newTestController(t, nil)
	m := New(c)
	m.width, m.height = 80, 24

	respCh := make(chan middleware.AskResult, 1)
	nm, _ := m.Update(askRequestMsg{req: middleware.AskRequest{
		Question: "选哪个方案？",
		Options:  []middleware.AskOption{{Label: "方案A"}, {Label: "方案B"}},
	}, respCh: respCh})
	m = nm.(Model)
	if m.ovl == nil || m.ovl.kind != overlayAsk {
		t.Fatalf("应打开 ask 弹窗，ovl=%+v", m.ovl)
	}
	if !strings.Contains(m.View(), "选哪个方案？") {
		t.Error("弹窗应显示问题")
	}

	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = nm.(Model)
	if m.ovl.ask.cursor != 1 {
		t.Errorf("↓ 后 cursor = %d, want 1", m.ovl.ask.cursor)
	}
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	if m.ovl != nil {
		t.Error("提交后弹窗应关闭")
	}
	select {
	case r := <-respCh:
		if len(r.Selection) != 1 || r.Selection[0] != "方案B" {
			t.Errorf("Selection = %v, want [方案B]", r.Selection)
		}
	default:
		t.Error("应回送回答")
	}
}

// TestAskModalCustom Other 自定义文本：打字 → Enter 提交 Custom。
func TestAskModalCustom(t *testing.T) {
	c := newTestController(t, nil)
	m := New(c)
	m.width, m.height = 80, 24

	respCh := make(chan middleware.AskResult, 1)
	nm, _ := m.Update(askRequestMsg{req: middleware.AskRequest{
		Question: "选哪个方案？",
		Options:  []middleware.AskOption{{Label: "方案A"}, {Label: "方案B"}},
	}, respCh: respCh})
	m = nm.(Model)

	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("方案C")})
	m = nm.(Model)
	if m.ovl.ask.custom != "方案C" {
		t.Errorf("custom 缓冲 = %q, want 方案C", m.ovl.ask.custom)
	}
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	select {
	case r := <-respCh:
		if r.Custom != "方案C" {
			t.Errorf("Custom = %q, want 方案C", r.Custom)
		}
	default:
		t.Error("应回送自定义回答")
	}
}

// TestAskModalMultiSelect 多选：Space 勾选 A/C → Enter 提交 [A, C]。
func TestAskModalMultiSelect(t *testing.T) {
	c := newTestController(t, nil)
	m := New(c)
	m.width, m.height = 80, 24

	respCh := make(chan middleware.AskResult, 1)
	nm, _ := m.Update(askRequestMsg{req: middleware.AskRequest{
		Question: "支持哪些？",
		Options:  []middleware.AskOption{{Label: "A"}, {Label: "B"}, {Label: "C"}},
		Multiple: true,
	}, respCh: respCh})
	m = nm.(Model)

	// A（默认 cursor=0）Space → ↓↓ 到 C → Space → Enter。
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = nm.(Model)
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = nm.(Model)
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = nm.(Model)
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = nm.(Model)
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	select {
	case r := <-respCh:
		if len(r.Selection) != 2 || r.Selection[0] != "A" || r.Selection[1] != "C" {
			t.Errorf("Selection = %v, want [A C]", r.Selection)
		}
	default:
		t.Error("应回送多选回答")
	}
}

// TestAskModalCancel Esc 取消 → 空回答。
func TestAskModalCancel(t *testing.T) {
	c := newTestController(t, nil)
	m := New(c)
	m.width, m.height = 80, 24

	respCh := make(chan middleware.AskResult, 1)
	nm, _ := m.Update(askRequestMsg{req: middleware.AskRequest{Question: "Q?"}, respCh: respCh})
	m = nm.(Model)
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = nm.(Model)
	if m.ovl != nil {
		t.Error("Esc 后弹窗应关闭")
	}
	select {
	case r := <-respCh:
		if len(r.Selection) != 0 || r.Custom != "" {
			t.Errorf("取消应返回空回答，got %+v", r)
		}
	default:
		t.Error("应回送空回答")
	}
}
