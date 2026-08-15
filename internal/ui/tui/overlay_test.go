package tui

import (
	"strings"
	"testing"

	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
	tea "github.com/charmbracelet/bubbletea"
)

// TestOverlayMutuallyExclusive 验证覆盖层互斥（Bug10）：审批挂起时队列命令
// （/help、/model）不得叠开第二层弹窗。原 appr/sel/help 三字段可并存——
// 审批未决时 runDone 消费队列命令直接开第二层，help 被审批遮住、答完审批后
// 突然浮现。收成 ovl tagged union + openOverlay 守卫后，审批挂起时新弹窗被拒。
func TestOverlayMutuallyExclusive(t *testing.T) {
	// 审批请求 → 打开审批覆盖层。
	m := New(nil)
	respCh := make(chan middleware.Decision, 1)
	nm, _ := m.Update(approvalRequestMsg{req: middleware.ApprovalRequest{ToolName: "shell_command", Summary: "rm -rf x"}, respCh: respCh})
	m = nm.(Model)
	if m.ovl == nil || m.ovl.kind != overlayApproval {
		t.Fatalf("审批请求应开审批覆盖层，ovl = %+v", m.ovl)
	}

	// 模拟 Bug10 通道：审批挂起时回合结束，runDone 消费队列里的 /help。
	m.queue = []string{"/help"}
	nm, _ = m.Update(runDoneMsg{})
	m = nm.(Model)
	if m.ovl == nil || m.ovl.kind != overlayApproval {
		t.Fatalf("审批挂起时不应被 help 覆盖，ovl = %+v", m.ovl)
	}
	if !strings.Contains(m.View(), "Permission required") {
		t.Fatal("审批弹窗应仍在渲染")
	}

	// 审批答完 → 覆盖层关闭。
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = nm.(Model)
	if m.ovl != nil {
		t.Fatalf("答完审批覆盖层应关闭，ovl = %+v", m.ovl)
	}

	// 选择器同理：审批挂起时队列 /model 不得叠开选择器。
	m = New(nil)
	respCh2 := make(chan middleware.Decision, 1)
	nm, _ = m.Update(approvalRequestMsg{req: middleware.ApprovalRequest{ToolName: "x"}, respCh: respCh2})
	m = nm.(Model)
	m.queue = []string{"/model"}
	nm, _ = m.Update(runDoneMsg{})
	m = nm.(Model)
	if m.ovl == nil || m.ovl.kind != overlayApproval {
		t.Fatalf("审批挂起时队列 /model 不应叠开选择器，ovl = %+v", m.ovl)
	}
}

func TestOverlayKeepsTimelineVisible(t *testing.T) {
	m := New(nil)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = nm.(Model)
	m.appendMessage(&MessageItem{Role: messages.RoleAssistant, Content: "existing transcript", Rendered: "existing transcript", Done: true})
	m.refresh(true)
	before := m.viewport.Height

	respCh := make(chan middleware.Decision, 1)
	nm, _ = m.Update(approvalRequestMsg{req: middleware.ApprovalRequest{
		ToolName: "shell_command",
		Summary:  "git push",
	}, respCh: respCh})
	m = nm.(Model)

	view := m.View()
	if !strings.Contains(view, "existing transcript") || !strings.Contains(view, "Permission required") {
		t.Fatalf("overlay and timeline should render together:\n%s", view)
	}
	if m.viewport.Height >= before {
		t.Fatalf("overlay should take layout space: viewport height %d, before %d", m.viewport.Height, before)
	}
}
