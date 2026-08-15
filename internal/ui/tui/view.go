package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/messages"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var (
	colorSurface = lipgloss.Color("236")
	colorRaised  = lipgloss.Color("237")
	colorBorder  = lipgloss.Color("241")
	colorMuted   = lipgloss.Color("245")
	colorText    = lipgloss.Color("252")
	colorAccent  = lipgloss.Color("209")
	colorGreen   = lipgloss.Color("78")
	colorYellow  = lipgloss.Color("221")
	colorRed     = lipgloss.Color("203")

	styleBrand     = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	styleText      = lipgloss.NewStyle().Foreground(colorText)
	styleMuted     = lipgloss.NewStyle().Foreground(colorMuted)
	styleUser      = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	styleAssistant = lipgloss.NewStyle().Foreground(colorAccent)
	styleSystem    = lipgloss.NewStyle().Foreground(colorMuted)
	styleError     = lipgloss.NewStyle().Foreground(colorRed)
	styleSuccess   = lipgloss.NewStyle().Foreground(colorGreen)
	styleRunning   = lipgloss.NewStyle().Foreground(colorYellow)
	styleAdd       = lipgloss.NewStyle().Foreground(colorGreen)
	styleDelete    = lipgloss.NewStyle().Foreground(colorRed)
	styleSelected  = lipgloss.NewStyle().Background(colorRaised).Foreground(colorText)
	stylePanel     = lipgloss.NewStyle().Foreground(colorText)
	styleBorder    = lipgloss.NewStyle().Foreground(colorBorder)
)

func (m Model) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	parts := []string{
		m.headerView(),
		m.viewport.View(),
	}
	if aux := m.auxiliaryView(); aux != "" {
		parts = append(parts, aux)
	}
	parts = append(parts, m.composerView(), m.footerView())
	return strings.Join(parts, "\n")
}

func (m Model) headerView() string {
	left := styleBrand.Render("Harness")
	if m.status.Model != "" {
		left += styleMuted.Render("  " + m.status.Model)
	}
	right := ""
	switch {
	case m.status.SessionName != "":
		// 会话名优先（截断防窄屏溢出）；未命名/未创建依次兜底。
		right = styleMuted.Render(truncate(m.status.SessionName, 28))
	case m.status.SessionID != "":
		right = styleMuted.Render(shortSession(m.status.SessionID))
	default:
		right = styleMuted.Render("New session")
	}
	if m.status.PlanMode {
		left += styleRunning.Render("  plan")
	}
	return alignRow(left, right, m.width, 1)
}

func (m Model) composerView() string {
	label := "message"
	if m.focus == focusTimeline {
		label = "timeline focused"
	}
	lineStyle := styleBorder
	if m.focus == focusComposer {
		lineStyle = lipgloss.NewStyle().Foreground(colorAccent)
	}
	top := ruleWithLabel(label, m.width, lineStyle)
	prompt := styleBrand.Render("❯") + " " + m.input.View()
	bottom := lineStyle.Render(strings.Repeat("─", maxInt(1, m.width)))
	return top + "\n" + prompt + "\n" + bottom
}

func (m Model) footerView() string {
	left := ""
	if m.toast != "" {
		left = styleMuted.Render("  " + m.toast)
	} else if m.selection.text != "" {
		left = styleAssistant.Render(fmt.Sprintf("  Selected %d chars · Ctrl+C to copy", len([]rune(m.selection.text))))
	} else if m.running {
		left = styleRunning.Render("  " + m.sp.View() + " Working")
	} else {
		left = styleMuted.Render("  Ready")
	}
	rightParts := make([]string, 0, 6)
	if m.status.Permission != "" {
		rightParts = append(rightParts, m.status.Permission)
	}
	if m.status.ThinkingEffort != "" {
		rightParts = append(rightParts, "effort:"+m.status.ThinkingEffort)
	}
	if m.status.ContextTokens > 0 && m.status.ContextWindow > 0 {
		// 当前上下文占用（ADR-037 用量展示）：`ctx 128k/1.0M`。
		rightParts = append(rightParts, "ctx "+fmtTokens(m.status.ContextTokens)+"/"+fmtTokens(int64(m.status.ContextWindow)))
	}
	if m.status.TodoCount > 0 {
		rightParts = append(rightParts, fmt.Sprintf("todo:%d", m.status.TodoCount))
	}
	if len(m.queue) > 0 {
		rightParts = append(rightParts, fmt.Sprintf("queued:%d", len(m.queue)))
	}
	right := styleMuted.Render(strings.Join(rightParts, " · "))
	return alignRow(left, right, m.width, 0)
}

func (m Model) auxiliaryView() string {
	budget := m.height - headerHeight - footerHeight - lipgloss.Height(m.composerView()) - 3
	if budget <= 0 {
		return ""
	}
	if m.ovl != nil {
		return m.overlayView(budget)
	}
	if completion := m.completionView(); completion != "" {
		return firstLines(completion, budget)
	}
	todo := renderTodoBar(&m)
	queue := renderQueueBar(&m)
	if queue == "" {
		return firstLines(todo, budget)
	}
	queueHeight := minInt(lipgloss.Height(queue), budget)
	queue = firstLines(queue, queueHeight)
	remaining := budget - queueHeight
	if todo == "" || remaining <= 0 {
		return queue
	}
	return firstLines(todo, remaining) + "\n" + queue
}

func (m Model) completionView() string {
	if !m.completionVisible() {
		return ""
	}
	items := completionItems(m.input.Value())
	const visible = 5
	start := 0
	if m.completion >= visible {
		start = m.completion - visible + 1
	}
	if start+visible > len(items) {
		start = maxInt(0, len(items)-visible)
	}
	end := minInt(len(items), start+visible)
	items = items[start:end]
	rows := make([]string, 0, end-start)
	for offset, item := range items {
		index := start + offset
		prefix := "  "
		if index == m.completion {
			prefix = "❯ "
		}
		row := fitLine(fmt.Sprintf("%s/%-12s %s", prefix, item.name, item.short), maxInt(1, m.width))
		if index == m.completion {
			row = styleSelected.Render(row)
		} else {
			row = stylePanel.Render(row)
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
	lines := []string{styleRunning.Render("  Tasks")}
	const maxVisible = 5
	visible := 0
	for _, todo := range m.status.Todos {
		switch todo.Status {
		case agentstate.TodoCompleted:
			done++
		case agentstate.TodoInProgress:
			doing++
		default:
			pending++
		}
		if visible < maxVisible {
			description := strings.ReplaceAll(strings.TrimSpace(todo.Description), "\n", " ")
			prefix := "  " + todoMark(todo) + " "
			bodyWidth := maxInt(8, m.width-lipgloss.Width(prefix)-2)
			wrapped := ansi.Hardwrap(description, bodyWidth, true)
			wrappedLines := strings.Split(wrapped, "\n")
			for i, line := range wrappedLines {
				if i == 0 {
					lines = append(lines, styleText.Render(prefix+line))
				} else {
					lines = append(lines, styleText.Render(strings.Repeat(" ", lipgloss.Width(prefix))+line))
				}
			}
			visible++
		}
	}
	if extra := len(m.status.Todos) - visible; extra > 0 {
		lines = append(lines, styleMuted.Render(fmt.Sprintf("  ... +%d more", extra)))
	}
	stats := fmt.Sprintf("%d active · %d pending · %d done", doing, pending, done)
	lines[0] += styleMuted.Render("  " + stats)
	return strings.Join(lines, "\n")
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
		label := "  Queued"
		if i > 0 {
			label = "        "
		}
		line := fmt.Sprintf("%s  %d. %s", label, i+1, content)
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
		return "✓"
	case agentstate.TodoInProgress:
		return "●"
	default:
		return "○"
	}
}

func (m Model) overlayView(availableHeight int) string {
	var content string
	if m.ovl != nil {
		switch m.ovl.kind {
		case overlayApproval:
			content = renderApproval(m.ovl.appr, m.width)
		case overlaySelect:
			content = renderPopup(m.ovl.sel, m.width, availableHeight)
		case overlayAsk:
			content = renderAsk(m.ovl.ask, m.width)
		case overlayHelp:
			content = renderHelp(m.width)
		}
	}
	content = firstLines(content, availableHeight)
	return lipgloss.PlaceHorizontal(m.width, lipgloss.Center, content)
}

func renderApproval(appr *approvalPopup, width int) string {
	panelWidth := modalPanelWidth(width, 34, 76)
	bodyWidth := modalInnerWidth(panelWidth)
	summary := ansi.Hardwrap(appr.req.Summary, bodyWidth, true)
	if summary == "" {
		summary = appr.req.ToolName
	}
	lines := []string{styleText.Render("Tool  ") + styleAssistant.Render(appr.req.ToolName)}
	for _, line := range strings.Split(summary, "\n") {
		lines = append(lines, styleMuted.Render(line))
	}
	lines = append(lines, "",
		styleSuccess.Render("❯ [Y] Allow once"),
		styleRunning.Render("  [S] Allow for this session"),
		styleError.Render("  [N] Deny"),
	)
	return renderInlinePanel("Permission required", lines, panelWidth, styleRunning)
}

// renderAsk 渲染提问弹窗（ADR-036）：header + 问题 + 选项列表（单选 Enter 高亮 /
// 多选 Space 勾选）+ Other 自定义输入行 + 提示。
func renderAsk(ask *askPopup, width int) string {
	panelWidth := modalPanelWidth(width, 40, 84)
	bodyWidth := modalInnerWidth(panelWidth)
	header := ask.req.Header
	if header == "" {
		header = "Question"
	}
	question := ansi.Hardwrap(ask.req.Question, bodyWidth, true)
	rows := make([]string, 0, len(ask.req.Options))
	for i, o := range ask.req.Options {
		mark := "○ "
		if ask.req.Multiple && i < len(ask.selected) && ask.selected[i] {
			mark = "● "
		}
		prefix := "  " + mark
		if i == ask.cursor {
			prefix = "❯ " + mark
		}
		row := ansi.Truncate(prefix+o.Label, maxInt(1, bodyWidth), "...")
		if i == ask.cursor {
			row = styleSelected.Render(fitLine(row, bodyWidth))
		}
		rows = append(rows, row)
	}
	hint := "Enter confirm · Esc cancel"
	if ask.req.Multiple {
		hint = "Space toggle · Enter confirm · Esc cancel"
	}
	if ask.req.AllowCustom {
		hint += " · type for custom"
	}
	lines := make([]string, 0, len(rows)+8)
	lines = append(lines, strings.Split(styleText.Render(question), "\n")...)
	lines = append(lines, "")
	lines = append(lines, rows...)
	if ask.req.AllowCustom {
		lines = append(lines, "", styleMuted.Render(ansi.Truncate("Custom  "+ask.custom+"_", maxInt(1, bodyWidth), "...")))
	}
	lines = append(lines, styleMuted.Render(hint))
	return renderInlinePanel(header, lines, panelWidth, styleRunning)
}

func renderHelp(width int) string {
	panelWidth := modalPanelWidth(width, 38, 78)
	innerWidth := modalInnerWidth(panelWidth)
	left := strings.Join([]string{
		styleAssistant.Render("Commands"),
		"/switch      change session",
		"/model       change model",
		"/effort      reasoning effort",
		"/thinking    toggle thinking",
		"/permission  approval policy",
		"/plan        toggle plan mode",
		"/usage       show token usage",
		"/compact     compact context",
		"/rename      rename session",
		"/exit        leave Harness",
	}, "\n")
	right := strings.Join([]string{
		styleAssistant.Render("Keys"),
		"Tab          change focus",
		"Enter/Space  toggle block",
		"PgUp/PgDn    scroll",
		"Shift+Enter  new line",
		"Ctrl+C       copy composer",
		"Esc          interrupt turn",
	}, "\n")
	content := left + "\n\n" + right
	// 两列并排需要能同时容纳最宽的命令行和按键行，否则退化为单列竖排。
	leftWidth := blockWidth(left) + 2
	if innerWidth >= leftWidth+blockWidth(right) {
		content = lipgloss.JoinHorizontal(lipgloss.Top,
			lipgloss.NewStyle().Width(leftWidth).Render(left), right)
	}
	return renderInlinePanel("Help", strings.Split(content, "\n"), panelWidth, styleAssistant)
}

const (
	modalBorderWidth  = 0
	modalPaddingWidth = 4
)

// modalPanelWidth 把期望宽度收敛到 [minWidth, maxWidth] 且不超出屏幕。
// 返回值是弹窗的外框总宽（含边框）。
func modalPanelWidth(screenWidth, minWidth, maxWidth int) int {
	limit := maxInt(modalBorderWidth+modalPaddingWidth+1, screenWidth)
	if maxWidth > limit {
		maxWidth = limit
	}
	if minWidth > maxWidth {
		minWidth = maxWidth
	}
	return clamp(screenWidth-8, minWidth, maxWidth)
}

// modalInnerWidth returns the text width after the inline panel's padding.
func modalInnerWidth(panelWidth int) int {
	return maxInt(1, panelWidth-modalBorderWidth-modalPaddingWidth)
}

func renderInlinePanel(title string, lines []string, panelWidth int, titleStyle lipgloss.Style) string {
	panelWidth = maxInt(1, panelWidth)
	innerWidth := modalInnerWidth(panelWidth)
	result := []string{ruleWithLabel(title, panelWidth, titleStyle)}
	for _, line := range lines {
		for _, wrapped := range strings.Split(ansi.Hardwrap(line, innerWidth, true), "\n") {
			result = append(result, fitLine("  "+wrapped, panelWidth))
		}
	}
	result = append(result, styleBorder.Render(strings.Repeat("─", panelWidth)))
	return strings.Join(result, "\n")
}

// renderTimeline returns both the viewport content and relative line hit boxes.
func renderTimeline(m *Model) (string, []hitTarget) {
	var sb strings.Builder
	var hits []hitTarget
	line := 0
	wroteCell := false
	// appendCell inserts the separator before each cell so collapsed tools can
	// use a compact boundary without changing spacing between other messages.
	appendCell := func(cell string, hit *hitTarget, localStart, localEnd int, compactBefore bool) {
		cell = strings.TrimRight(cell, "\n")
		if cell == "" {
			return
		}
		if wroteCell {
			if compactBefore {
				sb.WriteString("\n")
			} else {
				sb.WriteString("\n\n")
				line++
			}
		}
		height := lipgloss.Height(cell)
		if hit != nil {
			if localEnd < 0 {
				localEnd = height - 1
			}
			hit.start = line + clamp(localStart, 0, height-1)
			hit.end = line + clamp(localEnd, 0, height-1)
			hits = append(hits, *hit)
		}
		sb.WriteString(cell)
		line += height
		wroteCell = true
	}

	for _, item := range m.items {
		switch item.kind {
		case itemMessage:
			cell := renderMessageItem(item.msg, m.contentWidth, m.showThinking, m.isThinkingSelected(item.msg))
			var hit *hitTarget
			if cell.thinkingStart >= 0 {
				h := hitTarget{kind: hitThinking, message: item.msg}
				hit = &h
			}
			appendCell(cell.body, hit, cell.thinkingStart, cell.thinkingEnd, false)
		case itemTool:
			cell := renderToolBlock(item.tool, m.contentWidth, m.isToolSelected(item.tool))
			var hit *hitTarget
			if item.tool.Expandable() {
				h := hitTarget{kind: hitTool, tool: item.tool}
				hit = &h
			}
			appendCell(cell, hit, 0, -1, item.tool.Collapsed)
		}
	}
	if m.stream != nil && (m.stream.Text != "" || (m.showThinking && m.stream.Thinking != "")) {
		appendCell(renderStream(m.stream, m.contentWidth, m.showThinking), nil, 0, -1, false)
	}
	if sb.Len() == 0 {
		brand := styleBrand.Render("Harness")
		prompt := styleMuted.Render("What would you like to build?")
		empty := lipgloss.PlaceHorizontal(m.width, lipgloss.Center, brand+"\n"+prompt)
		sb.WriteString("\n\n" + empty)
	}
	return strings.TrimRight(sb.String(), "\n"), hits
}

// renderSelectedTimeline applies reverse video to selected rune ranges while
// retaining the timeline's existing ANSI colors. Stripping ANSI here would
// make an incidental mouse-release event turn the entire timeline white.
func renderSelectedTimeline(content string, selection textSelection) string {
	if content == "" || selection.text == "" {
		return content
	}
	lines := strings.Split(content, "\n")
	start, end := selection.anchor, selection.focus
	if start.line > end.line || (start.line == end.line && start.column > end.column) {
		start, end = end, start
	}
	start.line = clamp(start.line, 0, len(lines)-1)
	end.line = clamp(end.line, 0, len(lines)-1)
	for i, line := range lines {
		runes := []rune(line)
		from, to := 0, 0
		switch {
		case i < start.line || i > end.line:
			continue
		case start.line == end.line:
			from, to = start.column, end.column
		case i == start.line:
			from, to = start.column, len(runes)
		case i == end.line:
			from, to = 0, end.column
		default:
			from, to = 0, len(runes)
		}
		from = clamp(from, 0, len(runes))
		to = clamp(to, from, len(runes))
		if from < to {
			lines[i] = highlightANSISelection(line, from, to)
		}
	}
	return strings.Join(lines, "\n")
}

func highlightANSISelection(line string, from, to int) string {
	const reverseOn = "\x1b[7m"
	const reverseOff = "\x1b[27m"
	var out strings.Builder
	runeIndex := 0
	selected := false
	for i := 0; i < len(line); {
		if line[i] == '\x1b' {
			n := ansiSequenceLength(line[i:])
			out.WriteString(line[i : i+n])
			i += n
			continue
		}
		if runeIndex == from {
			out.WriteString(reverseOn)
			selected = true
		}
		if runeIndex == to && selected {
			out.WriteString(reverseOff)
			selected = false
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		if size == 0 {
			break
		}
		out.WriteRune(r)
		runeIndex++
		i += size
	}
	if selected {
		out.WriteString(reverseOff)
	}
	return out.String()
}

func ansiSequenceLength(s string) int {
	if len(s) < 2 || s[0] != '\x1b' {
		return 1
	}
	// CSI sequences terminate at the first byte in the final-byte range.
	start := 1
	if s[1] == '[' {
		start = 2
	}
	for i := start; i < len(s); i++ {
		if s[i] >= 0x40 && s[i] <= 0x7e {
			return i + 1
		}
	}
	return 1
}

// renderMessages remains as a small compatibility helper for focused view tests.
func renderMessages(m *Model) string {
	content, _ := renderTimeline(m)
	return content
}

// messageCell is a rendered message plus the line range (relative to the cell)
// occupied by its thinking block. thinkingStart < 0 means there is none.
type messageCell struct {
	body          string
	thinkingStart int
	thinkingEnd   int
}

func renderMessageItem(item *MessageItem, width int, showThinking, thinkingSelected bool) messageCell {
	width = maxInt(16, width)
	if item.Role == "" {
		mark := styleSystem.Render("·")
		style := styleSystem
		if item.Err {
			mark = styleError.Render("!")
			style = styleError
		}
		return messageCell{
			body:          mark + " " + style.Render(ansi.Hardwrap(item.Content, maxInt(8, width-2), true)),
			thinkingStart: -1,
			thinkingEnd:   -1,
		}
	}
	content := item.Content
	if item.Done && item.Rendered != "" {
		content = item.Rendered
	}
	content = ansi.Hardwrap(strings.TrimSpace(content), width-2, true)
	if item.Role == messages.RoleUser {
		lines := strings.Split(content, "\n")
		for i, line := range lines {
			prefix := "  "
			if i == 0 {
				prefix = styleUser.Render("❯") + " "
			}
			lines[i] = lipgloss.NewStyle().Background(colorSurface).Render(fitLine(" "+prefix+line, width))
		}
		return messageCell{
			body:          strings.Join(lines, "\n"),
			thinkingStart: -1,
			thinkingEnd:   -1,
		}
	}

	var parts []string
	thinkingStart, thinkingEnd := -1, -1
	if showThinking && item.Thinking != "" {
		thinkingLabel := fmt.Sprintf("▸ Thinking · %d chars", len([]rune(item.Thinking)))
		if item.ThinkingExpanded {
			thinkingLabel = "▾ Thinking"
		}
		if thinkingSelected {
			thinkingLabel = styleSelected.Render(fitLine("❯ "+thinkingLabel, width))
		} else {
			thinkingLabel = styleMuted.Render("  " + thinkingLabel)
		}
		block := thinkingLabel
		if item.ThinkingExpanded {
			thinking := ansi.Hardwrap(strings.TrimSpace(item.Thinking), maxInt(8, width-5), true)
			block = thinkingLabel + "\n" + lipgloss.NewStyle().
				BorderStyle(lipgloss.Border{Left: "│"}).
				BorderLeft(true).
				BorderForeground(colorBorder).
				PaddingLeft(1).
				MarginLeft(2).
				Foreground(colorMuted).
				Render(thinking)
		}
		thinkingStart = 0
		if len(parts) > 0 {
			thinkingStart = lipgloss.Height(strings.Join(parts, "\n"))
		}
		thinkingEnd = thinkingStart + lipgloss.Height(block) - 1
		parts = append(parts, block)
	}
	if content != "" {
		parts = append(parts, prefixBlock(content, styleAssistant.Render("●")+" ", "  "))
	}
	return messageCell{
		body:          strings.Join(parts, "\n"),
		thinkingStart: thinkingStart,
		thinkingEnd:   thinkingEnd,
	}
}

func renderStream(stream *StreamState, width int, showThinking bool) string {
	var parts []string
	if showThinking && stream.Thinking != "" {
		thinking := ansi.Hardwrap(stream.Thinking, maxInt(8, width-4), true)
		parts = append(parts, styleMuted.Render("  "+strings.ReplaceAll(thinking, "\n", "\n  ")))
	}
	if stream.Text != "" {
		text := ansi.Hardwrap(stream.Text, maxInt(8, width-2), true)
		parts = append(parts, prefixBlock(text, styleRunning.Render("●")+" ", "  "))
	} else {
		parts = append(parts, styleRunning.Render("●")+styleMuted.Render(" Thinking…"))
	}
	return strings.Join(parts, "\n")
}

func renderToolBlock(tool *ToolStatus, width int, selected bool) string {
	status := "●"
	statusStyle := styleRunning
	if tool.Done {
		status = "✓"
		statusStyle = styleSuccess
		if tool.Failed {
			status = "×"
			statusStyle = styleError
		}
	}
	prefix := "  "
	if selected {
		prefix = "❯ "
	}
	head := prefix + statusStyle.Render(status) + " " + styleText.Render(ansi.Truncate(toolDisplaySummary(tool), maxInt(8, width-6), "..."))
	if selected {
		head = styleSelected.Render(fitLine(head, width))
	}

	content := tool.Content
	if !tool.Collapsed {
		content = expandedToolContent(tool)
	}
	if content == "" {
		content = "waiting for result"
	}
	lines := strings.Split(strings.TrimSpace(content), "\n")
	total := len(lines)
	if tool.Collapsed && len(lines) > 3 {
		lines = lines[:3]
	}
	for i, line := range lines {
		lines[i] = colorDiffLine(ansi.Hardwrap(line, maxInt(8, width-7), true))
	}
	body := strings.Join(lines, "\n")
	if tool.Expandable() {
		state := "Enter to collapse"
		if tool.Collapsed {
			state = fmt.Sprintf("%d lines · Enter to expand", total)
		}
		body += "\n" + styleMuted.Render("↳ "+state)
	}
	return head + "\n" + lipgloss.NewStyle().
		BorderStyle(lipgloss.Border{Left: "│"}).
		BorderLeft(true).
		BorderForeground(colorBorder).
		PaddingLeft(1).
		MarginLeft(3).
		Render(body)
}

func expandedToolContent(tool *ToolStatus) string {
	if tool == nil {
		return ""
	}
	parts := []string{styleAssistant.Render("Arguments"), formatToolArgs(tool.Args)}
	result := tool.Full
	if result == "" {
		result = tool.Content
	}
	if result == "" {
		result = "waiting for result"
	}
	result = compactToolResult(result)
	parts = append(parts, styleAssistant.Render("Result"), result)
	return strings.Join(parts, "\n")
}

func compactToolResult(result string) string {
	result = strings.ReplaceAll(result, "\r\n", "\n")
	result = strings.ReplaceAll(result, "\r", "\n")
	lines := strings.Split(result, "\n")
	start, end := 0, len(lines)
	for start < end && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	for end > start && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return strings.Join(lines[start:end], "\n")
}

func toolDisplaySummary(tool *ToolStatus) string {
	if tool == nil {
		return "Tool"
	}
	summary := strings.TrimSpace(tool.Summary)
	switch tool.Name {
	case "shell_command":
		return "Ran " + strings.TrimSpace(strings.TrimPrefix(summary, "shell_command:"))
	case "read_file":
		return "Read " + strings.TrimSpace(strings.TrimPrefix(summary, "read_file"))
	case "list_dir":
		return "Listed " + strings.TrimSpace(strings.TrimPrefix(summary, "list_dir"))
	case "write_file":
		return "Wrote " + strings.TrimSpace(strings.TrimPrefix(summary, "write_file"))
	case "glob":
		return "Searched " + strings.TrimSpace(strings.TrimPrefix(summary, "glob"))
	case "apply_patch":
		return "Applied patch"
	case "update_todo":
		return "Updated tasks"
	default:
		return summary
	}
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
		// Tool details contain many raw JSON/result lines. Give them an
		// explicit foreground and reset so terminal color state cannot leak
		// into the rest of the timeline after expanding a block.
		return styleText.Render(line)
	}
}

func (m Model) selectedTarget() *hitTarget {
	if m.focus != focusTimeline || m.selectedHit < 0 || m.selectedHit >= len(m.hits) {
		return nil
	}
	return &m.hits[m.selectedHit]
}

func (m Model) isThinkingSelected(message *MessageItem) bool {
	selected := m.selectedTarget()
	return selected != nil && selected.kind == hitThinking && selected.message == message
}

func (m Model) isToolSelected(tool *ToolStatus) bool {
	selected := m.selectedTarget()
	return selected != nil && selected.kind == hitTool && selected.tool == tool
}

// blockWidth 返回多行文本中最宽一行的显示宽度。
func blockWidth(block string) int {
	width := 0
	for _, line := range strings.Split(block, "\n") {
		width = maxInt(width, lipgloss.Width(line))
	}
	return width
}

func firstLines(block string, limit int) string {
	if limit <= 0 || block == "" {
		return ""
	}
	lines := strings.Split(block, "\n")
	if len(lines) <= limit {
		return block
	}
	return strings.Join(lines[:limit], "\n")
}

func prefixBlock(block, firstPrefix, restPrefix string) string {
	lines := strings.Split(block, "\n")
	for i, line := range lines {
		if i == 0 {
			lines[i] = firstPrefix + line
		} else {
			lines[i] = restPrefix + line
		}
	}
	return strings.Join(lines, "\n")
}

func fitLine(line string, width int) string {
	width = maxInt(1, width)
	line = ansi.Truncate(line, width, "")
	return line + strings.Repeat(" ", maxInt(0, width-lipgloss.Width(line)))
}

func ruleWithLabel(label string, width int, style lipgloss.Style) string {
	width = maxInt(1, width)
	line := "─ " + strings.TrimSpace(label) + " "
	line = ansi.Truncate(line, width, "")
	line += strings.Repeat("─", maxInt(0, width-lipgloss.Width(line)))
	return style.Render(line)
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

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func shortSession(id string) string {
	if len(id) <= 13 {
		return id
	}
	return id[:8] + "..." + id[len(id)-4:]
}

// fmtTokens 把 token 数格式化为紧凑显示（<1K 显示原值、<1M 用 k、≥1M 用 M），
// footer 的 `ctx 128k/1.0M` 与 /usage 用量展示共用。
// 小值显示原数字而非 0k（ADR-037 勘误：input 小但 cache 大时避免误导）。
func fmtTokens(n int64) string {
	if n >= 1000000 {
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	}
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%dk", n/1000)
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
