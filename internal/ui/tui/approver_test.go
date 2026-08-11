package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agent-project/harness/internal/middleware"
	tea "github.com/charmbracelet/bubbletea"
)

// TestApprovalPopupAndKeys 审批请求 → 弹窗；y/s/n 各回送对应 Decision 并关闭弹窗。
func TestApprovalPopupAndKeys(t *testing.T) {
	cases := []struct {
		key  string
		want middleware.Decision
	}{
		{"y", middleware.DecisionAllow},
		{"s", middleware.DecisionAllowSession},
		{"n", middleware.DecisionDeny},
	}
	for _, tc := range cases {
		m := New(nil)
		respCh := make(chan middleware.Decision, 1)
		nm, _ := m.Update(approvalRequestMsg{req: middleware.ApprovalRequest{ToolName: "shell_command", Summary: "rm -rf x", Mode: "readonly"}, respCh: respCh})
		m = nm.(Model)
		if m.ovl == nil {
			t.Fatalf("[%s] 应有审批弹窗", tc.key)
		}
		nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tc.key)})
		m = nm.(Model)
		if m.ovl != nil {
			t.Fatalf("[%s] 弹窗应关闭", tc.key)
		}
		select {
		case d := <-respCh:
			if d != tc.want {
				t.Fatalf("[%s] Decision = %d, want %d", tc.key, d, tc.want)
			}
		default:
			t.Fatalf("[%s] respCh 应收到决策", tc.key)
		}
	}
}

// TestApprovalEscDeny Esc 拒绝 + 中断（弹窗关闭 + respCh Deny）。
func TestApprovalEscDeny(t *testing.T) {
	m := New(nil)
	respCh := make(chan middleware.Decision, 1)
	nm, _ := m.Update(approvalRequestMsg{req: middleware.ApprovalRequest{ToolName: "shell_command", Summary: "rm -rf x"}, respCh: respCh})
	m = nm.(Model)

	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = nm.(Model)
	if m.ovl != nil {
		t.Fatal("Esc 后弹窗应关闭")
	}
	select {
	case d := <-respCh:
		if d != middleware.DecisionDeny {
			t.Fatalf("Esc 应 Deny, got %d", d)
		}
	default:
		t.Fatal("Esc 后 respCh 应收到 Deny")
	}
}

// TestApprovalRender View 应含审批条（输入区上方）。
func TestApprovalRender(t *testing.T) {
	m := New(nil)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = nm.(Model)
	respCh := make(chan middleware.Decision, 1)
	nm, _ = m.Update(approvalRequestMsg{req: middleware.ApprovalRequest{ToolName: "shell_command", Summary: "rm -rf x", Mode: "readonly"}, respCh: respCh})
	m = nm.(Model)
	if !strings.Contains(m.View(), "PERMISSION REQUIRED") || !strings.Contains(m.View(), "[Y] Allow once") {
		t.Fatalf("View 应含审批条")
	}
}

// TestApproverRequestSends tuiApprover.Request 发 approvalRequestMsg + 等 respCh 回送。
func TestApproverRequestSends(t *testing.T) {
	var got approvalRequestMsg
	ready := make(chan struct{})
	c := &tuiApprover{send: func(msg tea.Msg) {
		got = msg.(approvalRequestMsg)
		close(ready)
	}}
	done := make(chan middleware.Decision, 1)
	go func() {
		d, _ := c.Request(context.Background(), middleware.ApprovalRequest{ToolName: "x"})
		done <- d
	}()
	<-ready // 等 send 执行完（got.respCh 已填充）
	got.respCh <- middleware.DecisionAllow
	select {
	case d := <-done:
		if d != middleware.DecisionAllow {
			t.Fatalf("Request 返回 %d, want Allow", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Request 超时未返回")
	}
}
