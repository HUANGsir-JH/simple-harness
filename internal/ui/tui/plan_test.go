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
		Question:    "选哪个方案？",
		Options:     []middleware.AskOption{{Label: "方案A"}, {Label: "方案B"}},
		AllowCustom: true, // 真实调用方（ask_user/plan_enter/plan_done）都传 true
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
		Question:    "选哪个方案？",
		Options:     []middleware.AskOption{{Label: "方案A"}, {Label: "方案B"}},
		AllowCustom: true, // 真实调用方（ask_user/plan_enter/plan_done）都传 true
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

// --- 并发弹窗待决队列（ADR-036 修订，缺陷 03）----

// TestPendingQueueAskThenAsk 连续两个 ask：第一个开弹窗、第二个入队；逐个回答
// 逐个弹出，respCh 都被写入，无 goroutine 泄漏。
func TestPendingQueueAskThenAsk(t *testing.T) {
	c := newTestController(t, nil)
	m := New(c)
	m.width, m.height = 80, 24

	resp1 := make(chan middleware.AskResult, 1)
	resp2 := make(chan middleware.AskResult, 1)
	nm, _ := m.Update(askRequestMsg{req: middleware.AskRequest{Question: "q1", Options: []middleware.AskOption{{Label: "A"}}, AllowCustom: true}, respCh: resp1})
	m = nm.(Model)
	nm, _ = m.Update(askRequestMsg{req: middleware.AskRequest{Question: "q2", Options: []middleware.AskOption{{Label: "B"}}, AllowCustom: true}, respCh: resp2})
	m = nm.(Model)

	if m.ovl == nil || m.ovl.kind != overlayAsk || m.ovl.ask.req.Question != "q1" {
		t.Fatalf("第一个弹窗应打开 q1，got %+v", m.ovl)
	}
	if len(m.pending) != 1 {
		t.Fatalf("第二个应入队，pending=%d", len(m.pending))
	}

	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	select {
	case r := <-resp1:
		if len(r.Selection) != 1 || r.Selection[0] != "A" {
			t.Errorf("resp1 Selection = %v, want [A]", r.Selection)
		}
	default:
		t.Error("resp1 应收到回答")
	}
	if m.ovl == nil || m.ovl.ask.req.Question != "q2" {
		t.Fatalf("第二个应自动弹出 q2，got %+v", m.ovl)
	}
	if len(m.pending) != 0 {
		t.Errorf("pending 应空，got %d", len(m.pending))
	}

	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	select {
	case r := <-resp2:
		if len(r.Selection) != 1 || r.Selection[0] != "B" {
			t.Errorf("resp2 Selection = %v, want [B]", r.Selection)
		}
	default:
		t.Error("resp2 应收到回答")
	}
	if m.ovl != nil || len(m.pending) != 0 {
		t.Errorf("全部关闭后 ovl=%v pending=%d", m.ovl, len(m.pending))
	}
}

// TestPendingQueueApprovalThenAsk review 场景：审批挂起时 ask 到达 → ask 入队，
// 审批回答后 ask 自动弹出（审批不静默消失）。
func TestPendingQueueApprovalThenAsk(t *testing.T) {
	c := newTestController(t, nil)
	m := New(c)
	m.width, m.height = 80, 24

	respA := make(chan middleware.Decision, 1)
	respB := make(chan middleware.AskResult, 1)
	nm, _ := m.Update(approvalRequestMsg{req: middleware.ApprovalRequest{Summary: "git push", Mode: "readonly"}, respCh: respA})
	m = nm.(Model)
	nm, _ = m.Update(askRequestMsg{req: middleware.AskRequest{Question: "q", Options: []middleware.AskOption{{Label: "A"}}, AllowCustom: true}, respCh: respB})
	m = nm.(Model)

	if m.ovl == nil || m.ovl.kind != overlayApproval {
		t.Fatalf("审批应先弹窗，got kind=%v", m.ovl.kind)
	}
	if len(m.pending) != 1 {
		t.Fatalf("ask 应入队，pending=%d", len(m.pending))
	}

	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = nm.(Model)
	select {
	case d := <-respA:
		if d != middleware.DecisionAllow {
			t.Errorf("respA = %v, want Allow", d)
		}
	default:
		t.Error("respA 应收到决策")
	}
	if m.ovl == nil || m.ovl.kind != overlayAsk {
		t.Fatalf("ask 应自动弹出，got kind=%v", m.ovl.kind)
	}

	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	select {
	case <-respB:
	default:
		t.Error("respB 应收到回答")
	}
	if m.ovl != nil || len(m.pending) != 0 {
		t.Errorf("全部关闭后 ovl=%v pending=%d", m.ovl, len(m.pending))
	}
}

// TestPendingQueueAskThenApproval 反序混排：ask 开、approval 入队；Esc 关 ask →
// approval 自动弹出。
func TestPendingQueueAskThenApproval(t *testing.T) {
	c := newTestController(t, nil)
	m := New(c)
	m.width, m.height = 80, 24

	respA := make(chan middleware.Decision, 1)
	respB := make(chan middleware.AskResult, 1)
	nm, _ := m.Update(askRequestMsg{req: middleware.AskRequest{Question: "q", Options: []middleware.AskOption{{Label: "A"}}, AllowCustom: true}, respCh: respB})
	m = nm.(Model)
	nm, _ = m.Update(approvalRequestMsg{req: middleware.ApprovalRequest{Summary: "git push", Mode: "readonly"}, respCh: respA})
	m = nm.(Model)

	if m.ovl == nil || m.ovl.kind != overlayAsk {
		t.Fatalf("ask 应先弹窗，got kind=%v", m.ovl.kind)
	}
	if len(m.pending) != 1 {
		t.Fatalf("approval 应入队，pending=%d", len(m.pending))
	}

	// Esc 关 ask（running=false 不触发 requestInterrupt）→ 空回答 + approval 弹出。
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = nm.(Model)
	select {
	case r := <-respB:
		if len(r.Selection) != 0 || r.Custom != "" {
			t.Errorf("Esc 取消应空回答，got %+v", r)
		}
	default:
		t.Error("respB 应收到空回答")
	}
	if m.ovl == nil || m.ovl.kind != overlayApproval {
		t.Fatalf("approval 应自动弹出，got kind=%v", m.ovl.kind)
	}

	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = nm.(Model)
	select {
	case d := <-respA:
		if d != middleware.DecisionDeny {
			t.Errorf("respA = %v, want Deny", d)
		}
	default:
		t.Error("respA 应收到决策")
	}
	if m.ovl != nil || len(m.pending) != 0 {
		t.Errorf("全部关闭后 ovl=%v pending=%d", m.ovl, len(m.pending))
	}
}

// TestPendingQueuedApprovalNotLost 并发两个审批：第二个入队，第一个回答后弹出
// ——审批请求不静默消失（review 场景 2 的核心修复）。
func TestPendingQueuedApprovalNotLost(t *testing.T) {
	c := newTestController(t, nil)
	m := New(c)
	m.width, m.height = 80, 24

	respA := make(chan middleware.Decision, 1)
	respB := make(chan middleware.Decision, 1)
	nm, _ := m.Update(approvalRequestMsg{req: middleware.ApprovalRequest{Summary: "rm -rf /", Mode: "readonly"}, respCh: respA})
	m = nm.(Model)
	nm, _ = m.Update(approvalRequestMsg{req: middleware.ApprovalRequest{Summary: "git push", Mode: "readonly"}, respCh: respB})
	m = nm.(Model)

	if len(m.pending) != 1 {
		t.Fatalf("第二个审批应入队，pending=%d", len(m.pending))
	}

	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = nm.(Model)
	select {
	case d := <-respA:
		if d != middleware.DecisionDeny {
			t.Errorf("respA = %v, want Deny", d)
		}
	default:
		t.Error("respA 应收到决策")
	}
	if m.ovl == nil || m.ovl.kind != overlayApproval || m.ovl.appr.req.Summary != "git push" {
		t.Fatalf("第二个审批应弹出，got %+v", m.ovl)
	}
	if len(m.pending) != 0 {
		t.Errorf("pending 应空，got %d", len(m.pending))
	}
}

// TestPendingClearedOnInterrupt 审批挂起 + ask 排队时 Esc 中断：requestInterrupt
// 清空 pending（ctx cancel 让阻塞 goroutine 经 ctx.Done 释放），当前审批收 Deny，
// 不残留孤儿弹窗。
func TestPendingClearedOnInterrupt(t *testing.T) {
	c := newTestController(t, nil)
	m := New(c)
	m.width, m.height = 80, 24
	m.running = true

	respA := make(chan middleware.Decision, 1)
	respB := make(chan middleware.AskResult, 1)
	nm, _ := m.Update(approvalRequestMsg{req: middleware.ApprovalRequest{Summary: "rm -rf /", Mode: "readonly"}, respCh: respA})
	m = nm.(Model)
	nm, _ = m.Update(askRequestMsg{req: middleware.AskRequest{Question: "q", Options: []middleware.AskOption{{Label: "A"}}, AllowCustom: true}, respCh: respB})
	m = nm.(Model)

	// Esc 中断当前审批（running → requestInterrupt 清 pending）。
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = nm.(Model)
	select {
	case d := <-respA:
		if d != middleware.DecisionDeny {
			t.Errorf("respA = %v, want Deny（Esc 中断）", d)
		}
	default:
		t.Error("respA 应收到 Deny")
	}
	if m.ovl != nil {
		t.Error("中断后当前弹窗应关闭")
	}
	if len(m.pending) != 0 {
		t.Errorf("pending 应被清空，got %d", len(m.pending))
	}
}

// TestAskAllowCustomFalse AllowCustom=false 约束：打字不入 custom，Enter 提交
// Selection（非 Custom），View 不渲染 Custom 行。
func TestAskAllowCustomFalse(t *testing.T) {
	c := newTestController(t, nil)
	m := New(c)
	m.width, m.height = 80, 24

	respCh := make(chan middleware.AskResult, 1)
	nm, _ := m.Update(askRequestMsg{req: middleware.AskRequest{
		Question: "选哪个？",
		Options:  []middleware.AskOption{{Label: "A"}, {Label: "B"}},
		// AllowCustom 缺省 = false
	}, respCh: respCh})
	m = nm.(Model)

	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("xyz")})
	m = nm.(Model)
	if m.ovl.ask.custom != "" {
		t.Errorf("AllowCustom=false 时打字不应入 custom，got %q", m.ovl.ask.custom)
	}

	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	select {
	case r := <-respCh:
		if len(r.Selection) != 1 || r.Selection[0] != "A" {
			t.Errorf("Selection = %v, want [A]", r.Selection)
		}
		if r.Custom != "" {
			t.Errorf("Custom 应为空，got %q", r.Custom)
		}
	default:
		t.Error("应回送回答")
	}

	// View 不渲染 Custom 输入行（AllowCustom=false）。
	if s := m.View(); strings.Contains(s, "Custom:") {
		t.Errorf("AllowCustom=false 不应渲染 Custom 行:\n%s", s)
	}
}
