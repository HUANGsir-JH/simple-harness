package tui

import (
	"strings"
	"testing"

	"github.com/agent-project/harness/internal/agent"
	"github.com/agent-project/harness/internal/messages"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// TestUpdateExit 提交 /exit 应返回 Quit。
func TestUpdateExit(t *testing.T) {
	m := New(nil)
	m.input.SetValue("/exit")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("提交 /exit 应返回非 nil cmd（tea.Quit）")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("cmd 应产生 QuitMsg，got %T", cmd())
	}
}

// TestUpdateEnterClearsInput Enter 提交后输入区应清空。
func TestUpdateEnterClearsInput(t *testing.T) {
	m := New(nil)
	m.input.SetValue("hello")
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if v := nm.(Model).input.Value(); v != "" {
		t.Fatalf("提交后输入区应清空，got %q", v)
	}
}

// TestUpdateAltEnterAddsNewline Alt+Enter adds a newline without submitting.
func TestUpdateAltEnterAddsNewline(t *testing.T) {
	m := New(nil)
	m.input.SetValue("line1")
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	if v := nm.(Model).input.Value(); v != "line1\n" {
		t.Fatalf("Alt+Enter should insert a newline, got %q", v)
	}
}

// TestViewContainsInput View 应渲染输入区（含占位符）。
func TestViewContainsInput(t *testing.T) {
	m := New(nil)
	if !strings.Contains(m.View(), "Ask anything") {
		t.Fatalf("View 应包含输入区占位符")
	}
}

func TestFocusAndInputHistory(t *testing.T) {
	m := New(nil)
	m.inputHistory = []string{"first", "second"}
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = nm.(Model)
	if got := m.input.Value(); got != "second" {
		t.Fatalf("history up = %q, want second", got)
	}
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = nm.(Model)
	if m.focus != focusTimeline || m.input.Focused() {
		t.Fatal("Tab should move focus to timeline")
	}
}

func TestCommandCompletion(t *testing.T) {
	m := New(nil)
	m.input.SetValue("/mo")
	m.completion = 0
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = nm.(Model)
	if got := m.input.Value(); got != "/model " {
		t.Fatalf("completion = %q, want /model ", got)
	}
}

func TestExitCommandIsPersisted(t *testing.T) {
	c := newTestController(t, nil)
	m := New(c)
	m.input.SetValue("/exit")
	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("/exit should quit")
	}
	m = nm.(Model)
	commands, err := c.active.Commands()
	if err != nil || len(commands) != 1 || commands[0] != "/exit" {
		t.Fatalf("exit command not persisted: commands=%v err=%v", commands, err)
	}
}

func TestResponsiveView(t *testing.T) {
	for _, size := range []struct{ width, height int }{{20, 8}, {40, 12}, {120, 30}} {
		m := New(nil)
		nm, _ := m.Update(tea.WindowSizeMsg{Width: size.width, Height: size.height})
		m = nm.(Model)
		view := m.View()
		if lipgloss.Width(view) > size.width {
			t.Fatalf("view width %d exceeds terminal width %d", lipgloss.Width(view), size.width)
		}
		if lipgloss.Height(view) > size.height+1 {
			t.Fatalf("view height %d exceeds terminal height %d", lipgloss.Height(view), size.height)
		}
	}
}

func TestMouseTogglesToolBlock(t *testing.T) {
	m := New(nil)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = nm.(Model)
	tc := &messages.ToolCall{ID: "mouse-tool", Name: "shell_command", Args: []byte(`{"command":"echo hi"}`)}
	m.onToolCall(tc)
	m.onToolResult(agent.Event{ToolCall: tc, ToolResult: &messages.ToolResult{Success: true, Content: "one\ntwo\nthree\nfour\nfive\nsix\nseven"}})
	m.refresh(true)
	if len(m.hits) != 1 || !m.tools[0].Collapsed {
		t.Fatalf("expected one collapsed tool hit, hits=%v", m.hits)
	}
	hit := m.hits[0]
	y := m.mainTop + hit.start - m.viewport.YOffset
	nm, _ = m.Update(tea.MouseMsg{X: 4, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = nm.(Model)
	if m.tools[0].Collapsed {
		t.Fatal("mouse click should expand tool block")
	}
}
