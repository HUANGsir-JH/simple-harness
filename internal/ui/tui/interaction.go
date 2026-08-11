package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const (
	headerHeight = 2
	footerHeight = 1
	// maxComposerHeight 是输入框最大高度（行）：内容行数增长至此为止，超出
	// textarea 内部跟随光标滚动查看（MaxHeight 同步设为 5）。
	maxComposerHeight = 5
)

// updateComposerHeight 让输入框高度随内容行数动态增长：默认 1 行，每多一个
// 显式换行高一行，至多 maxComposerHeight（超过内部滚动）。每次输入变化
// （键入/换行/粘贴/历史/补全）后调用，高度变化经 layout 重排消息区。
func (m *Model) updateComposerHeight() {
	lines := strings.Count(m.input.Value(), "\n") + 1
	m.input.SetHeight(clamp(lines, 1, maxComposerHeight))
	m.layout()
}

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
	// 高度不由 layout 重置：由 updateComposerHeight 按内容行数管理
	// （WindowSizeMsg 触发的 layout 不覆盖动态高度）。

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
	if m.focus != focusComposer || m.ovl != nil {
		return false
	}
	value := strings.TrimSpace(m.input.Value())
	if !strings.HasPrefix(value, "/") {
		return false
	}
	prefix := strings.TrimPrefix(value, "/")
	if strings.Contains(prefix, " ") {
		return false
	}
	items := completionItems(value)
	if len(items) == 0 {
		return false
	}
	for _, item := range items {
		if item.name == prefix {
			return false
		}
	}
	return true
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
	m.updateComposerHeight()
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
	m.updateComposerHeight()
	m.refresh(false)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
