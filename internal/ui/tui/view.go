package tui

import (
	"strings"

	"github.com/agent-project/harness/internal/messages"
	"github.com/charmbracelet/lipgloss"
)

// lipgloss 样式（无 emoji/图标，纯文本 + 颜色，ADR-030 风格约束）。
var (
	styleUser = lipgloss.NewStyle().Foreground(lipgloss.Color("33")).Bold(true) // 蓝
	styleAsst = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true) // 绿
	styleSys  = lipgloss.NewStyle().Foreground(lipgloss.Color("246"))           // 灰
	styleDim  = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))           // thinking 灰
	styleErr  = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))           // 红
)

// View 渲染整个屏幕（纯函数）：消息区（viewport）+ 输入区。
func (m Model) View() string {
	var sb strings.Builder
	sb.WriteString(m.viewport.View())
	if m.viewport.View() != "" {
		sb.WriteString("\n")
	}
	sb.WriteString(m.input.View())
	return sb.String()
}

// renderMessages 把消息区渲染成字符串（流式块在最后）。
func renderMessages(m *Model) string {
	var sb strings.Builder
	for _, it := range m.msgs {
		renderMessageItem(&sb, it)
	}
	// 流式块（Thinking 灰显 + Text 原始，块完成才 md 渲染）。
	if m.stream != nil && (m.stream.Text != "" || m.stream.Thinking != "") {
		sb.WriteString(styleAsst.Render("[Assistant] "))
		if m.stream.Thinking != "" {
			sb.WriteString(styleDim.Render(m.stream.Thinking) + "\n")
		}
		sb.WriteString(m.stream.Text)
		sb.WriteString("\n")
	}
	return sb.String()
}

// renderMessageItem 渲染一条消息。
func renderMessageItem(sb *strings.Builder, it *MessageItem) {
	switch it.Role {
	case messages.RoleUser:
		sb.WriteString(styleUser.Render("[User] ") + it.Rendered + "\n\n")
	case messages.RoleAssistant:
		sb.WriteString(styleAsst.Render("[Assistant] "))
		if it.Done {
			sb.WriteString(it.Rendered)
		} else {
			sb.WriteString(it.Content)
		}
		if it.Thinking != "" {
			sb.WriteString("\n" + styleDim.Render("[thinking] 推理内容（点击展开）"))
		}
		sb.WriteString("\n\n")
	default:
		// 系统行 / 错误 / 工具占位（W3 折叠块替换）。
		content := it.Content
		if it.Err {
			sb.WriteString(styleErr.Render(content) + "\n")
		} else {
			sb.WriteString(styleSys.Render(content) + "\n")
		}
	}
}

// toolCallSummary 提取工具调用摘要（名称 + 参数前 60 字符）。
func toolCallSummary(tc *messages.ToolCall) string {
	if tc == nil {
		return ""
	}
	s := string(tc.Args)
	if len(s) > 60 {
		s = s[:60] + "…"
	}
	return tc.Name + " " + s
}
