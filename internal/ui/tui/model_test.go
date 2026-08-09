package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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

// TestUpdateAltEnterKeepsInput Alt+Enter（换行）不清空输入区。
func TestUpdateAltEnterKeepsInput(t *testing.T) {
	m := New(nil)
	m.input.SetValue("line1")
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	if v := nm.(Model).input.Value(); v != "line1" {
		t.Fatalf("Alt+Enter 不应清空输入，got %q", v)
	}
}

// TestViewContainsInput View 应渲染输入区（含占位符）。
func TestViewContainsInput(t *testing.T) {
	m := New(nil)
	if !strings.Contains(m.View(), "输入消息") {
		t.Fatalf("View 应包含输入区占位符")
	}
}
