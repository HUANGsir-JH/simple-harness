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
	if m.ovl != nil {
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
	switch {
	case m.status.SessionName != "":
		// 会话名优先（截断防窄屏溢出）；未命名/未创建依次兜底。
		right = styleMuted.Render(truncate(m.status.SessionName, 24))
	case m.status.SessionID != "":
		right = styleMuted.Render(shortSession(m.status.SessionID))
	default:
		right = styleMuted.Render("新会话") // 懒加载：尚未创建
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
	rightParts := make([]string, 0, 6)
	if m.status.PlanMode {
		rightParts = append(rightParts, "[PLAN]")
	}
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
	lines := []string{styleRunning.Render("TODO")}
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
	stats := fmt.Sprintf("%d active  %d pending  %d done", doing, pending, done)
	lines = append(lines, styleMuted.Render("  "+stats))
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
	if m.ovl != nil {
		switch m.ovl.kind {
		case overlayApproval:
			content = renderApproval(m.ovl.appr, m.width)
		case overlaySelect:
			content = renderPopup(m.ovl.sel, m.width, m.viewport.Height)
		case overlayAsk:
			content = renderAsk(m.ovl.ask, m.width)
		case overlayHelp:
			content = renderHelp(m.width)
		}
	}
	return lipgloss.Place(m.width, m.viewport.Height, lipgloss.Center, lipgloss.Center, content,
		lipgloss.WithWhitespaceBackground(colorCanvas))
}

func renderApproval(appr *approvalPopup, width int) string {
	panelWidth := modalPanelWidth(width, 34, 76)
	bodyWidth := modalInnerWidth(panelWidth)
	summary := ansi.Hardwrap(appr.req.Summary, bodyWidth, true)
	if summary == "" {
		summary = appr.req.ToolName
	}
	hints := []string{
		styleSuccess.Render("[Y] Allow once"),
		styleRunning.Render("[S] Allow for session"),
		styleError.Render("[N] Deny"),
	}
	// 一行放不下三个提示时改为竖排，避免被边框截断或折行。
	hintLine := strings.Join(hints, "   ")
	if lipgloss.Width(ansi.Strip(hintLine)) > bodyWidth {
		hintLine = strings.Join(hints, "\n")
	}
	content := styleRunning.Render("PERMISSION REQUIRED") + "\n\n" +
		styleText.Render(ansi.Truncate(appr.req.ToolName, bodyWidth, "...")) + "\n" +
		styleMuted.Render(summary) + "\n\n" + hintLine
	return modalStyle(panelWidth).Render(content)
}

// renderAsk 渲染提问弹窗（ADR-036）：header + 问题 + 选项列表（单选 Enter 高亮 /
// 多选 Space 勾选）+ Other 自定义输入行 + 提示。
func renderAsk(ask *askPopup, width int) string {
	panelWidth := modalPanelWidth(width, 40, 84)
	bodyWidth := modalInnerWidth(panelWidth)
	header := ask.req.Header
	if header == "" {
		header = "QUESTION"
	}
	question := ansi.Hardwrap(ask.req.Question, bodyWidth, true)
	rows := make([]string, 0, len(ask.req.Options))
	for i, o := range ask.req.Options {
		mark := "  "
		if ask.req.Multiple && i < len(ask.selected) && ask.selected[i] {
			mark = "[x] "
		}
		prefix := mark
		if i == ask.cursor {
			prefix = "> " + mark
		}
		row := ansi.Truncate(prefix+o.Label, maxInt(1, bodyWidth), "...")
		if i == ask.cursor {
			row = styleSelected.Width(bodyWidth).Render(row)
		} else {
			row = lipgloss.NewStyle().Width(bodyWidth).Render(row)
		}
		rows = append(rows, row)
	}
	hint := "Enter confirm   Esc cancel"
	if ask.req.Multiple {
		hint = "Space toggle   Enter confirm   Esc cancel"
	}
	if ask.req.AllowCustom {
		hint += "   type = custom"
	}
	content := styleRunning.Render(header) + "\n\n" +
		styleText.Render(question)
	if len(rows) > 0 {
		content += "\n\n" + strings.Join(rows, "\n")
	}
	if ask.req.AllowCustom {
		// Custom 输入行仅在允许自定义时渲染（ADR-036 修订：AllowCustom 约束对齐
		// run 模式 ParseAskAnswer，禁止自定义时不诱导输入）。
		content += "\n\n" + styleMuted.Render(ansi.Truncate("Custom: "+ask.custom+"_", maxInt(1, bodyWidth), "..."))
	}
	content += "\n" + styleMuted.Render(hint)
	return modalStyle(panelWidth).Render(content)
}

func renderHelp(width int) string {
	panelWidth := modalPanelWidth(width, 38, 78)
	innerWidth := modalInnerWidth(panelWidth)
	left := strings.Join([]string{
		styleAssistant.Render("COMMANDS"),
		"/switch      change session",
		"/model       change model",
		"/effort      reasoning effort",
		"/permission  approval policy",
		"/plan        toggle plan mode",
		"/exit        leave Harness",
	}, "\n")
	right := strings.Join([]string{
		styleAssistant.Render("KEYS"),
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
	return modalStyle(panelWidth).Render(content)
}

const (
	modalBorderWidth  = 2 // 左右各一列 ASCII 边框
	modalPaddingWidth = 4 // Padding(1, 2) 的左右内边距
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

// modalInnerWidth 是 modalStyle 渲染后真正可用的文本宽度。
// lipgloss 的 Width 包含 padding 但不含 border，所以两者都要扣掉。
func modalInnerWidth(panelWidth int) int {
	return maxInt(1, panelWidth-modalBorderWidth-modalPaddingWidth)
}

func modalStyle(panelWidth int) lipgloss.Style {
	return lipgloss.NewStyle().
		Width(modalInnerWidth(panelWidth)+modalPaddingWidth).
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
	// appendCell writes one timeline cell followed by a single blank separator
	// line. localStart/localEnd are line offsets inside the cell that the hit
	// target covers; localEnd < 0 means "the whole cell".
	appendCell := func(cell string, hit *hitTarget, localStart, localEnd int) {
		cell = strings.TrimRight(cell, "\n")
		if cell == "" {
			return
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
		sb.WriteString("\n\n")
		// cell occupies `height` lines plus the one blank separator line.
		line += height + 1
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
			appendCell(cell.body, hit, cell.thinkingStart, cell.thinkingEnd)
		case itemTool:
			cell := renderToolBlock(item.tool, m.contentWidth, m.isToolSelected(item.tool))
			var hit *hitTarget
			if item.tool.Expandable() {
				h := hitTarget{kind: hitTool, tool: item.tool}
				hit = &h
			}
			appendCell(cell, hit, 0, -1)
		}
	}
	if m.stream != nil && (m.stream.Text != "" || (m.showThinking && m.stream.Thinking != "")) {
		appendCell(renderStream(m.stream, m.contentWidth, m.showThinking), nil, 0, -1)
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
		label := "SYSTEM"
		style := styleSystem
		if item.Err {
			label = "ERROR"
			style = styleError
		}
		return messageCell{
			body:          style.Render(label+"  ") + styleMuted.Render(ansi.Hardwrap(item.Content, width-9, true)),
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
		body := lipgloss.NewStyle().
			Foreground(colorText).
			BorderStyle(lipgloss.Border{Left: "|"}).
			BorderLeft(true).
			BorderForeground(colorCyan).
			PaddingLeft(1).
			Render(content)
		return messageCell{
			body:          styleUser.Render("YOU") + "\n" + body,
			thinkingStart: -1,
			thinkingEnd:   -1,
		}
	}

	var parts []string
	thinkingStart, thinkingEnd := -1, -1
	parts = append(parts, styleAssistant.Render("ASSISTANT"))
	if showThinking && item.Thinking != "" {
		thinkingLabel := fmt.Sprintf("THINKING  [collapsed]  %d chars", len([]rune(item.Thinking)))
		if item.ThinkingExpanded {
			thinkingLabel = "THINKING  [expanded]"
		}
		if thinkingSelected {
			thinkingLabel = styleSelected.Render("> ") + styleMuted.Render(thinkingLabel)
		} else {
			thinkingLabel = styleMuted.Render(thinkingLabel)
		}
		block := thinkingLabel
		if item.ThinkingExpanded {
			thinking := ansi.Hardwrap(strings.TrimSpace(item.Thinking), width-2, true)
			block = thinkingLabel + "\n" + styleMuted.Render(thinking)
		}
		// 前面已有的部分（ASSISTANT 标题）决定 thinking 块的起始行。
		thinkingStart = lipgloss.Height(strings.Join(parts, "\n"))
		thinkingEnd = thinkingStart + lipgloss.Height(block) - 1
		parts = append(parts, block)
	}
	if content != "" {
		parts = append(parts, content)
	}
	return messageCell{
		body:          strings.Join(parts, "\n"),
		thinkingStart: thinkingStart,
		thinkingEnd:   thinkingEnd,
	}
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

// fmtTokens 把 token 数格式化为紧凑显示（<1M 用 k、≥1M 用 M），footer 的
// `ctx 128k/1.0M` 与 /usage 用量展示共用。
func fmtTokens(n int64) string {
	if n >= 1000000 {
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
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
