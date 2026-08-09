package tui

import (
	"fmt"
	"strings"

	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/messages"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var (
	colorCanvas = lipgloss.Color("234")
	colorPanel  = lipgloss.Color("235")
	colorRaised = lipgloss.Color("237")
	colorBorder = lipgloss.Color("240")
	colorMuted  = lipgloss.Color("244")
	colorText   = lipgloss.Color("252")
	colorCyan   = lipgloss.Color("81")
	colorGreen  = lipgloss.Color("78")
	colorYellow = lipgloss.Color("220")
	colorRed    = lipgloss.Color("203")

	styleBrand     = lipgloss.NewStyle().Foreground(colorCyan).Bold(true)
	styleText      = lipgloss.NewStyle().Foreground(colorText)
	styleMuted     = lipgloss.NewStyle().Foreground(colorMuted)
	styleUser      = lipgloss.NewStyle().Foreground(colorCyan).Bold(true)
	styleAssistant = lipgloss.NewStyle().Foreground(colorText).Bold(true)
	styleSystem    = lipgloss.NewStyle().Foreground(colorMuted)
	styleError     = lipgloss.NewStyle().Foreground(colorRed)
	styleSuccess   = lipgloss.NewStyle().Foreground(colorGreen)
	styleRunning   = lipgloss.NewStyle().Foreground(colorYellow)
	styleAdd       = lipgloss.NewStyle().Foreground(colorGreen)
	styleDelete    = lipgloss.NewStyle().Foreground(colorRed)
	styleSelected  = lipgloss.NewStyle().Background(colorRaised).Foreground(colorText)
	stylePanel     = lipgloss.NewStyle().Background(colorPanel).Foreground(colorText)
	styleBorder    = lipgloss.NewStyle().Foreground(colorBorder)

	// Compatibility aliases used by tool/markdown tests.
	styleSys  = styleSystem
	styleDim  = styleMuted
	styleErr  = styleError
	styleOK   = styleSuccess
	styleHdr  = styleRunning
	styleAsst = styleAssistant
	styleDel  = styleDelete

	asciiBorder = lipgloss.Border{
		Top: "-", Bottom: "-", Left: "|", Right: "|",
		TopLeft: "+", TopRight: "+", BottomLeft: "+", BottomRight: "+",
	}
)

func (m Model) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	main := m.viewport.View()
	if m.appr != nil || m.sel != nil || m.help {
		main = m.modalArea()
	}
	parts := []string{
		m.headerView(),
		main,
	}
	if aux := m.auxiliaryView(); aux != "" {
		parts = append(parts, aux)
	}
	parts = append(parts, m.composerView(), m.footerView())
	return lipgloss.NewStyle().Background(colorCanvas).Render(strings.Join(parts, "\n"))
}

func (m Model) headerView() string {
	left := styleBrand.Render("HARNESS")
	if m.status.Model != "" {
		left += styleMuted.Render("  " + m.status.Model)
	}
	right := ""
	if m.status.SessionID != "" {
		right = styleMuted.Render(shortSession(m.status.SessionID))
	}
	line := alignRow(left, right, m.width, 1)
	divider := styleBorder.Render(strings.Repeat("-", maxInt(1, m.width)))
	return line + "\n" + divider
}

func (m Model) composerView() string {
	label := "MESSAGE"
	if m.focus == focusTimeline {
		label = "MESSAGE  [inactive]"
	}
	labelLine := styleMuted.Render(label)
	body := m.input.View()
	innerWidth := maxInt(1, m.width-4)
	box := lipgloss.NewStyle().
		Width(innerWidth).
		Padding(0, 1).
		BorderStyle(asciiBorder).
		BorderForeground(colorBorder)
	if m.focus == focusComposer {
		box = box.BorderForeground(colorCyan)
	}
	return box.Render(labelLine + "\n" + body)
}

func (m Model) footerView() string {
	left := ""
	if m.toast != "" {
		left = styleMuted.Render(m.toast)
	} else if m.running {
		left = styleRunning.Render(m.sp.View() + " RUNNING")
	} else {
		left = styleSuccess.Render("READY")
	}
	rightParts := make([]string, 0, 4)
	if m.status.Permission != "" {
		rightParts = append(rightParts, m.status.Permission)
	}
	if m.status.ThinkingEffort != "" {
		rightParts = append(rightParts, "effort:"+m.status.ThinkingEffort)
	}
	if m.status.TodoCount > 0 {
		rightParts = append(rightParts, fmt.Sprintf("todo:%d", m.status.TodoCount))
	}
	if len(m.queue) > 0 {
		rightParts = append(rightParts, fmt.Sprintf("queued:%d", len(m.queue)))
	}
	right := styleMuted.Render(strings.Join(rightParts, "  "))
	return alignRow(left, right, m.width, 1)
}

func (m Model) auxiliaryView() string {
	var parts []string
	if completion := m.completionView(); completion != "" {
		parts = append(parts, completion)
	}
	if todo := renderTodoBar(&m); todo != "" {
		parts = append(parts, todo)
	}
	if queue := renderQueueBar(&m); queue != "" {
		parts = append(parts, queue)
	}
	return strings.Join(parts, "\n")
}

func (m Model) completionView() string {
	if !m.completionVisible() {
		return ""
	}
	items := completionItems(m.input.Value())
	if len(items) > 5 {
		items = items[:5]
	}
	rows := make([]string, 0, len(items))
	for i, item := range items {
		row := fmt.Sprintf("  /%-12s %s", item.name, item.short)
		row = ansi.Truncate(row, maxInt(1, m.width-2), "")
		if i == m.completion {
			row = styleSelected.Width(maxInt(1, m.width)).Render("> " + strings.TrimPrefix(row, "  "))
		} else {
			row = stylePanel.Width(maxInt(1, m.width)).Render(row)
		}
		rows = append(rows, row)
	}
	return strings.Join(rows, "\n")
}

func renderTodoBar(m *Model) string {
	if len(m.status.Todos) == 0 {
		return ""
	}
	var done, doing, pending int
	labels := make([]string, 0, 5)
	for _, todo := range m.status.Todos {
		switch todo.Status {
		case agentstate.TodoCompleted:
			done++
		case agentstate.TodoInProgress:
			doing++
		default:
			pending++
		}
		if len(labels) < 5 {
			labels = append(labels, todoMark(todo)+" "+todo.Description)
		}
	}
	line := "  TODO  " + strings.Join(labels, "  |  ")
	if extra := len(m.status.Todos) - len(labels); extra > 0 {
		line += fmt.Sprintf("  ... +%d", extra)
	}
	stats := fmt.Sprintf("%d active  %d pending  %d done", doing, pending, done)
	return styleRunning.Render(ansi.Truncate(line, maxInt(1, m.width), "...")) + "\n" +
		alignRow("", styleMuted.Render(stats), m.width, 0)
}

func renderQueueBar(m *Model) string {
	if len(m.queue) == 0 {
		return ""
	}
	limit := len(m.queue)
	if limit > 3 {
		limit = 3
	}
	lines := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		content := strings.ReplaceAll(m.queue[i], "\n", " ")
		label := "  QUEUED"
		if i > 0 {
			label = "        "
		}
		line := fmt.Sprintf("%s  %d  %s", label, i+1, content)
		if i == limit-1 && len(m.queue) > limit {
			line += fmt.Sprintf("  ... +%d", len(m.queue)-limit)
		}
		lines = append(lines, styleMuted.Render(ansi.Truncate(line, maxInt(1, m.width), "...")))
	}
	return strings.Join(lines, "\n")
}

func todoMark(todo agentstate.TodoItem) string {
	switch todo.Status {
	case agentstate.TodoCompleted:
		return "[x]"
	case agentstate.TodoInProgress:
		return "[>]"
	default:
		return "[ ]"
	}
}

func (m Model) modalArea() string {
	var content string
	switch {
	case m.appr != nil:
		content = renderApproval(m.appr, m.width)
	case m.sel != nil:
		content = renderPopup(m.sel, m.width, m.viewport.Height)
	case m.help:
		content = renderHelp(m.width)
	}
	return lipgloss.Place(m.width, m.viewport.Height, lipgloss.Center, lipgloss.Center, content,
		lipgloss.WithWhitespaceBackground(colorCanvas))
}

func renderApproval(appr *approvalPopup, width int) string {
	panelWidth := clamp(width-8, 34, 76)
	bodyWidth := maxInt(20, panelWidth-4)
	summary := ansi.Hardwrap(appr.req.Summary, bodyWidth, true)
	if summary == "" {
		summary = appr.req.ToolName
	}
	content := styleRunning.Render("PERMISSION REQUIRED") + "\n\n" +
		styleText.Render(appr.req.ToolName) + "\n" + styleMuted.Render(summary) + "\n\n" +
		styleSuccess.Render("[Y] Allow once") + "   " +
		styleRunning.Render("[S] Allow for session") + "   " +
		styleError.Render("[N] Deny")
	return modalStyle(panelWidth).Render(content)
}

func renderHelp(width int) string {
	panelWidth := clamp(width-8, 38, 78)
	left := strings.Join([]string{
		styleAssistant.Render("COMMANDS"),
		"/switch      change session",
		"/model       change model",
		"/effort      reasoning effort",
		"/permission  approval policy",
		"/exit        leave Harness",
	}, "\n")
	right := strings.Join([]string{
		styleAssistant.Render("KEYS"),
		"Tab          change focus",
		"PgUp/PgDn    scroll",
		"Shift+Enter  new line",
		"Ctrl+C       copy composer",
		"Esc          interrupt turn",
	}, "\n")
	content := left
	if panelWidth >= 66 {
		content = lipgloss.JoinHorizontal(lipgloss.Top,
			lipgloss.NewStyle().Width(panelWidth/2-2).Render(left), right)
	} else {
		content += "\n\n" + right
	}
	return modalStyle(panelWidth).Render(content)
}

func modalStyle(width int) lipgloss.Style {
	return lipgloss.NewStyle().
		Width(maxInt(20, width-4)).
		Padding(1, 2).
		Background(colorPanel).
		Foreground(colorText).
		BorderStyle(asciiBorder).
		BorderForeground(colorBorder)
}

// renderTimeline returns both the viewport content and relative line hit boxes.
func renderTimeline(m *Model) (string, []hitTarget) {
	var sb strings.Builder
	var hits []hitTarget
	line := 0
	appendCell := func(cell string, hit *hitTarget) {
		cell = strings.TrimRight(cell, "\n")
		if cell == "" {
			return
		}
		height := lipgloss.Height(cell)
		if hit != nil {
			hit.start = line
			hit.end = line + height - 1
			hits = append(hits, *hit)
		}
		sb.WriteString(cell)
		sb.WriteString("\n\n")
		line += height + 2
	}

	for _, item := range m.items {
		switch item.kind {
		case itemMessage:
			cell := renderMessageItem(item.msg, m.contentWidth, m.showThinking)
			var hit *hitTarget
			if m.showThinking && item.msg.Thinking != "" {
				h := hitTarget{kind: hitThinking, id: item.msg.ID}
				hit = &h
			}
			appendCell(cell, hit)
		case itemTool:
			cell := renderToolBlock(item.tool, m.contentWidth, m.isSelected(hitTool, item.tool.ID))
			hit := hitTarget{kind: hitTool, id: item.tool.ID}
			appendCell(cell, &hit)
		}
	}
	if m.stream != nil && (m.stream.Text != "" || (m.showThinking && m.stream.Thinking != "")) {
		appendCell(renderStream(m.stream, m.contentWidth, m.showThinking), nil)
	}
	if sb.Len() == 0 {
		empty := lipgloss.PlaceHorizontal(m.width, lipgloss.Center,
			styleMuted.Render("Start a conversation"))
		sb.WriteString("\n\n" + empty)
	}
	return strings.TrimRight(sb.String(), "\n"), hits
}

// renderMessages remains as a small compatibility helper for focused view tests.
func renderMessages(m *Model) string {
	content, _ := renderTimeline(m)
	return content
}

func renderMessageItem(item *MessageItem, width int, showThinking bool) string {
	width = maxInt(16, width)
	if item.Role == "" {
		label := "SYSTEM"
		style := styleSystem
		if item.Err {
			label = "ERROR"
			style = styleError
		}
		return style.Render(label+"  ") + styleMuted.Render(ansi.Hardwrap(item.Content, width-9, true))
	}
	content := item.Content
	if item.Done && item.Rendered != "" {
		content = item.Rendered
	}
	content = ansi.Hardwrap(strings.TrimSpace(content), width-2, true)
	if item.Role == messages.RoleUser {
		body := lipgloss.NewStyle().
			Foreground(colorText).
			BorderStyle(lipgloss.Border{Left: "|"}).
			BorderLeft(true).
			BorderForeground(colorCyan).
			PaddingLeft(1).
			Render(content)
		return styleUser.Render("YOU") + "\n" + body
	}

	var parts []string
	parts = append(parts, styleAssistant.Render("ASSISTANT"))
	if showThinking && item.Thinking != "" {
		if item.ThinkingExpanded {
			thinking := ansi.Hardwrap(strings.TrimSpace(item.Thinking), width-2, true)
			parts = append(parts, styleMuted.Render("THINKING  [expanded]")+"\n"+styleMuted.Render(thinking))
		} else {
			parts = append(parts, styleMuted.Render(fmt.Sprintf("THINKING  [collapsed]  %d chars", len([]rune(item.Thinking)))))
		}
	}
	if content != "" {
		parts = append(parts, content)
	}
	return strings.Join(parts, "\n")
}

func renderStream(stream *StreamState, width int, showThinking bool) string {
	parts := []string{styleAssistant.Render("ASSISTANT")}
	if showThinking && stream.Thinking != "" {
		parts = append(parts, styleMuted.Render(ansi.Hardwrap(stream.Thinking, width-2, true)))
	}
	if stream.Text != "" {
		parts = append(parts, styleText.Render(ansi.Hardwrap(stream.Text, width-2, true)))
	}
	return strings.Join(parts, "\n")
}

func renderToolBlock(tool *ToolStatus, width int, selected bool) string {
	status := "[RUN]"
	statusStyle := styleRunning
	if tool.Done {
		status = "[OK]"
		statusStyle = styleSuccess
		if tool.Failed {
			status = "[ERR]"
			statusStyle = styleError
		}
	}
	head := statusStyle.Render(status) + " " + styleText.Render(ansi.Truncate(tool.Summary, maxInt(8, width-10), "..."))
	if selected {
		head = styleSelected.Render("> ") + head
	}

	content := tool.Content
	if !tool.Collapsed && tool.Full != "" {
		content = tool.Full
	}
	if content == "" {
		content = "waiting for result"
	}
	lines := strings.Split(strings.TrimSpace(content), "\n")
	total := len(lines)
	if tool.Collapsed && len(lines) > 6 {
		lines = lines[:6]
	}
	for i, line := range lines {
		lines[i] = "  " + colorDiffLine(ansi.Hardwrap(line, maxInt(8, width-4), true))
	}
	body := strings.Join(lines, "\n")
	if tool.Expandable() {
		state := "expanded"
		if tool.Collapsed {
			state = fmt.Sprintf("collapsed  %d lines", total)
		}
		body += "\n" + styleMuted.Render("  ["+state+"]")
	}
	return head + "\n" + lipgloss.NewStyle().
		BorderStyle(lipgloss.Border{Left: "|"}).
		BorderLeft(true).
		BorderForeground(colorBorder).
		PaddingLeft(1).
		Render(body)
}

func colorDiffLine(line string) string {
	switch {
	case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
		return styleMuted.Render(line)
	case strings.HasPrefix(line, "+"):
		return styleAdd.Render(line)
	case strings.HasPrefix(line, "-"):
		return styleDelete.Render(line)
	default:
		return line
	}
}

func (m Model) isSelected(kind hitKind, id string) bool {
	return m.focus == focusTimeline && m.selectedHit >= 0 && m.selectedHit < len(m.hits) &&
		m.hits[m.selectedHit].kind == kind && m.hits[m.selectedHit].id == id
}

func alignRow(left, right string, width, padding int) string {
	available := maxInt(1, width-padding*2)
	left = ansi.Truncate(left, available, "")
	rightWidth := lipgloss.Width(right)
	leftWidth := lipgloss.Width(left)
	if leftWidth+rightWidth+1 > available {
		right = ansi.Truncate(right, maxInt(0, available-leftWidth-1), "")
		rightWidth = lipgloss.Width(right)
	}
	gap := maxInt(0, available-leftWidth-rightWidth)
	return strings.Repeat(" ", padding) + left + strings.Repeat(" ", gap) + right + strings.Repeat(" ", padding)
}

func shortSession(id string) string {
	if len(id) <= 13 {
		return id
	}
	return id[:8] + "..." + id[len(id)-4:]
}

func clamp(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}
