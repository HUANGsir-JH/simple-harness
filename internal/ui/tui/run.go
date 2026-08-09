package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

// RunTUI 启动 TUI（bubbletea Program，alt-screen 全屏）。
// 入口参数 W2 起扩展（session 管理器 / agent / 事件桥 / 审批桥）。
func RunTUI() error {
	p := tea.NewProgram(New(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("tui: %w", err)
	}
	return nil
}
