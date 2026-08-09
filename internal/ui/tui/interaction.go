package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const (
	headerHeight = 2
	footerHeight = 1
)

func (m *Model) layout() {
	if m.width < 1 {
		m.width = 1
	}
	if m.height < 1 {
		m.height = 1
	}
	outerPad := 2
	if m.width < 56 {
		outerPad = 0
	}
	m.contentWidth = maxInt(16, m.width-outerPad-4)
	m.input.SetWidth(maxInt(10, m.width-6))
	if m.width < 48 {
		m.input.SetHeight(2)
	} else {
		m.input.SetHeight(3)
	}

	composerHeight := lipgloss.Height(m.composerView())
	auxHeight := lipgloss.Height(m.auxiliaryView())
	m.mainTop = headerHeight
	m.composerTop = m.height - footerHeight - composerHeight
	mainHeight := m.height - headerHeight - footerHeight - composerHeight - auxHeight
	if mainHeight < 1 {
		mainHeight = 1
	}
	m.viewport.Width = m.width
	m.viewport.Height = mainHeight
	m.composerTop = headerHeight + mainHeight + auxHeight
}

func (m Model) completionVisible() bool {
	if m.focus != focusComposer || m.appr != nil || m.sel != nil || m.help {
		return false
	}
	value := strings.TrimSpace(m.input.Value())
	return strings.HasPrefix(value, "/") && !strings.Contains(strings.TrimPrefix(value, "/"), " ") && len(completionItems(value)) > 0
}

func normalizeCompletion(value string, current int) int {
	items := completionItems(value)
	if len(items) == 0 {
		return -1
	}
	if current < 0 || current >= len(items) {
		return 0
	}
	return current
}

func (m *Model) moveCompletion(delta int) {
	items := completionItems(m.input.Value())
	if len(items) == 0 {
		m.completion = -1
		return
	}
	if m.completion < 0 {
		m.completion = 0
	} else {
		m.completion = (m.completion + delta + len(items)) % len(items)
	}
	m.layout()
	m.refresh(false)
}

func (m *Model) acceptCompletion() {
	items := completionItems(m.input.Value())
	if len(items) == 0 {
		return
	}
	idx := m.completion
	if idx < 0 || idx >= len(items) {
		idx = 0
	}
	m.input.SetValue("/" + items[idx].name + " ")
	m.input.CursorEnd()
	m.completion = -1
	m.layout()
	m.refresh(false)
}

func (m *Model) recallHistory(direction int) {
	if len(m.inputHistory) == 0 {
		return
	}
	if m.historyPos < 0 {
		m.draft = m.input.Value()
		m.historyPos = len(m.inputHistory)
	}
	m.historyPos += direction
	if m.historyPos < 0 {
		m.historyPos = 0
	}
	if m.historyPos >= len(m.inputHistory) {
		m.historyPos = -1
		m.input.SetValue(m.draft)
	} else {
		m.input.SetValue(m.inputHistory[m.historyPos])
	}
	m.input.CursorEnd()
	m.refresh(false)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
