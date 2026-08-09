package tui

import (
	"fmt"
	"strings"

	"github.com/agent-project/harness/internal/messages"
	"github.com/charmbracelet/lipgloss"
)

// lipgloss 样式（无 emoji/图标，纯文本 + 颜色，ADR-030 风格约束）。
var (
	styleUser  = lipgloss.NewStyle().Foreground(lipgloss.Color("33")).Bold(true) // 蓝
	styleAsst  = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true) // 绿
	styleSys   = lipgloss.NewStyle().Foreground(lipgloss.Color("246"))           // 灰
	styleDim   = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))           // thinking/次级灰
	styleErr   = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))           // 红
	styleOK    = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true) // 成功绿
	styleAdd   = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))            // diff + 绿
	styleDel   = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))           // diff - 红
	styleHdr   = lipgloss.NewStyle().Foreground(lipgloss.Color("178")).Bold(true) // 工具块头黄
)

// View 渲染整个屏幕（纯函数）：消息区 + 状态栏 + 审批条 + 输入区。
func (m Model) View() string {
	var sb strings.Builder
	if v := m.viewport.View(); v != "" {
		sb.WriteString(v)
		sb.WriteString("\n")
	}
	sb.WriteString(m.statusLine())
	sb.WriteString("\n")
	if m.appr != nil {
		sb.WriteString(renderApproval(m.appr))
		sb.WriteString("\n")
	}
	sb.WriteString(m.input.View())
	return sb.String()
}

// renderApproval 渲染审批弹窗条（输入区上方；无 emoji，纯文本 + 颜色）。
func renderApproval(appr *approvalPopup) string {
	req := appr.req
	line := styleHdr.Render("[审批] " + req.ToolName)
	if req.Summary != "" {
		line += " " + req.Summary
	}
	return line + "\n" + styleDim.Render("  模式 "+req.Mode+" | 允许(y) / 本会话记住(s) / 拒绝(n) / Esc 拒绝并中断")
}

// statusLine 渲染底部状态栏（模型 | 权限 | todo | spinner）。
func (m Model) statusLine() string {
	var parts []string
	if m.status.Model != "" {
		parts = append(parts, m.status.Model)
	}
	if m.status.Permission != "" {
		parts = append(parts, "权限:"+m.status.Permission)
	}
	if m.status.TodoCount > 0 {
		parts = append(parts, fmt.Sprintf("todo:%d", m.status.TodoCount))
	}
	if m.running {
		parts = append(parts, m.sp.View())
	}
	line := strings.Join(parts, " | ")
	if m.running {
		line += "   Esc 中断"
	}
	return styleSys.Render(line)
}

// renderMessages 把消息区渲染成字符串（工具块内插 + 流式块在最后）。
func renderMessages(m *Model) string {
	var sb strings.Builder
	for _, it := range m.msgs {
		renderMessageItem(&sb, it)
	}
	// 工具折叠块（消息流内插：出现在回合消息之后）。
	for _, ts := range m.tools {
		renderToolBlock(&sb, ts)
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
		content := it.Content
		if it.Err {
			sb.WriteString(styleErr.Render(content) + "\n")
		} else {
			sb.WriteString(styleSys.Render(content) + "\n")
		}
	}
}

// renderToolBlock 渲染一个工具折叠块（块头 + 内容 + 折叠提示；diff 行着色）。
func renderToolBlock(sb *strings.Builder, ts *ToolStatus) {
	// 块头：[摘要] [状态]
	statusStr := "[执行中]"
	headStyle := styleHdr
	if ts.Done {
		if ts.Failed {
			statusStr = "[ERR]"
			headStyle = styleErr
		} else {
			statusStr = "[OK]"
			headStyle = styleOK
		}
	}
	sb.WriteString(headStyle.Render(ts.Summary + " " + statusStr) + "\n")

	// 内容：折叠态（限 6 行）/ 展开态全文。
	content := ts.Content
	if !ts.Collapsed && ts.Full != "" {
		content = ts.Full
	}
	lines := strings.Split(content, "\n")
	overflow := len(lines) > 6
	if ts.Collapsed && overflow {
		lines = lines[:6]
	}
	for _, line := range lines {
		sb.WriteString("  " + colorDiffLine(line) + "\n")
	}
	if overflow || (ts.Done && ts.Full != "" && ts.Full != ts.Content) {
		sb.WriteString("  " + styleDim.Render("（Enter/点击展开看全文）") + "\n")
	}
}

// colorDiffLine 对 diff 行着色（+ 绿 / - 红；排除 +++/--- 头）。
func colorDiffLine(line string) string {
	switch {
	case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
		return styleDim.Render(line)
	case strings.HasPrefix(line, "+"):
		return styleAdd.Render(line)
	case strings.HasPrefix(line, "-"):
		return styleDel.Render(line)
	default:
		return line
	}
}
