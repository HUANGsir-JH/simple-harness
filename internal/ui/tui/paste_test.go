package tui

import (
	"testing"

	"github.com/agent-project/harness/internal/messages"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// upd 把 Model.Update 的 tea.Model 接口断言回 Model。
func upd(m Model, msg tea.Msg) Model {
	nm, _ := m.Update(msg)
	return nm.(Model)
}

// TestPasteMultiLineSingleMessage 验证 bracketed paste 解析后的 KeyRunes（含
// \n）整段进 textarea（不触发 submit）——粘贴多行是一条完整消息而非拆条。
// bubbletea 把 bracketed paste 解析为单个 Key{Type:KeyRunes, Paste:true}
// （key_sequences.go），这里模拟该 KeyMsg。
func TestPasteMultiLineSingleMessage(t *testing.T) {
	c, _ := newLazyController(t)
	m := upd(New(c), tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("line1\nline2\nline3")})
	if got := m.input.Value(); got != "line1\nline2\nline3" {
		t.Fatalf("粘贴应整段进 textarea（不触发提交），got %q", got)
	}
	// 高度随内容行数增长（3 行内容 → 高度 3）。
	if h := lipgloss.Height(m.input.View()); h != 3 {
		t.Errorf("textarea 高度应随内容增长到 3，got %d", h)
	}

	// 提交 → 一条完整多行消息。
	nm, cmd := m.submit()
	m2 := nm.(Model)
	if m2.input.Value() != "" {
		t.Errorf("提交后输入应清空，got %q", m2.input.Value())
	}
	if cmd == nil {
		t.Fatal("提交应返回 run cmd")
	}
	msg := cmd()
	if rd, ok := msg.(runDoneMsg); !ok || rd.err != nil {
		t.Fatalf("Run 结果: %+v", msg)
	}
	conv := c.active.Conversation().Messages
	var userContent string
	for _, m := range conv {
		if m.Role == messages.RoleUser {
			userContent = m.Content
		}
	}
	if userContent != "line1\nline2\nline3" {
		t.Errorf("conversation 应是一条完整多行 user 消息，got %q", userContent)
	}
}

// TestComposerHeightGrows 验证高度动态：默认 1 行、随换行增长至多 5 行
// （超出内部滚动）、Reset 回落。
func TestComposerHeightGrows(t *testing.T) {
	c, _ := newLazyController(t)
	m := New(c)
	if h := lipgloss.Height(m.input.View()); h != 1 {
		t.Fatalf("默认高度应为 1 行，got %d", h)
	}
	// 6 行内容 → 高度封顶 5（超出内部滚动）。
	for i := 0; i < 6; i++ {
		m = upd(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x\n")})
	}
	if h := lipgloss.Height(m.input.View()); h != 5 {
		t.Errorf("高度应封顶 5，got %d", h)
	}
	// submit Reset 后高度回落 1（多行内容提交清空）。
	nm, cmd := m.submit()
	m2 := nm.(Model)
	if cmd != nil {
		_ = cmd() // 执行 Run 使路径完整
	}
	if h := lipgloss.Height(m2.input.View()); h != 1 {
		t.Errorf("提交后高度应回落 1，got %d", h)
	}
}
