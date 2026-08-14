package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/agent-project/harness/internal/agentstate"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func (m Model) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	main := lipgloss.JoinHorizontal(lipgloss.Top, m.viewport.View(), m.scrollbarView())
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
	divider := styleBorder.Render(strings.Repeat("─", maxInt(1, m.width)))
	return line + "\n" + divider
}

// composerView 输入框（ADR-043 视觉）：圆角边框块，焦点 accent / 失焦 border；
// 去掉 MESSAGE 文字标签，焦点状态由边框色表达（Tab 语义不变）。
func (m Model) composerView() string {
	body := m.input.View()
	innerWidth := maxInt(1, m.width-4)
	box := lipgloss.NewStyle().
		Width(innerWidth).
		Padding(0, 1).
		BorderStyle(currentTheme().Border).
		BorderForeground(colorBorder)
	if m.focus == focusComposer {
		box = box.BorderForeground(colorCyan)
	}
	return box.Render(body)
}

func (m Model) footerView() string {
	left := ""
	switch {
	case m.toast != "":
		left = styleMuted.Render(m.toast)
	case m.running:
		// 运行态实时耗时（ADR-043 用户追加需求：回合打点 → 渲染时 time.Since）。
		left = styleRunning.Render(m.sp.View() + " RUNNING")
		if m.turnStarted != nil {
			left += styleMuted.Render(" " + formatDuration(time.Since(*m.turnStarted)))
		}
	default:
		left = styleSuccess.Render("READY")
		if m.lastTurn > 0 {
			// 空闲态残留上一回合总耗时（codex elapsed 同款）。
			left += styleMuted.Render(" · last " + formatDuration(m.lastTurn))
		}
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
	lines := []string{auxHeader("TODO", styleRunning, m.width)}
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
	lines := []string{auxHeader("QUEUED", styleMuted, m.width)}
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

// auxHeader 是 todo/queue 面板的统一头部：`TITLE ────…`（标题 + 分隔填充）。
func auxHeader(title string, style lipgloss.Style, width int) string {
	fill := maxInt(1, width-lipgloss.Width(title)-1)
	return style.Render(title) + " " + styleBorder.Render(strings.Repeat("─", fill))
}

// ---- 右侧滚动条（ADR-043 §6.2.1）----

// scrollbarView 渲染右侧 1 列滚动条：轨道 │（border 淡化）+ 拇指 █（accent）；
// 内容总行数不超过视口时不显示（返回空串，View 侧 JoinHorizontal 自动收缩）。
func (m Model) scrollbarView() string {
	total := m.viewport.TotalLineCount()
	vis := m.viewport.Height
	if vis <= 0 || total <= vis {
		return ""
	}
	thumb := clamp(vis*vis/total, 1, vis)
	pos := 0
	if maxOff := total - vis; maxOff > 0 {
		pos = clamp(m.viewport.YOffset*(vis-thumb)/maxOff, 0, vis-thumb)
	}
	var sb strings.Builder
	for i := 0; i < vis; i++ {
		if i >= pos && i < pos+thumb {
			sb.WriteString(thumbStyle.Render("█"))
		} else {
			sb.WriteString(trackStyle.Render("│"))
		}
		sb.WriteString("\n")
	}
	return strings.TrimSuffix(sb.String(), "\n")
}

// scrollbarAt 报告鼠标坐标是否落在滚动条列（仅滚动条可见时）。
func (m *Model) scrollbarAt(ev tea.MouseEvent) bool {
	if ev.X != m.width-1 || m.ovl != nil {
		return false
	}
	if ev.Y < m.mainTop || ev.Y >= m.mainTop+m.viewport.Height {
		return false
	}
	return m.viewport.TotalLineCount() > m.viewport.Height
}

// jumpScrollbar 点击/拖拽滚动条：按 Y 比例设置视口偏移（贴底判定同步）。
func (m *Model) jumpScrollbar(evY int) {
	total := m.viewport.TotalLineCount()
	vis := m.viewport.Height
	if total <= vis {
		return
	}
	maxOff := total - vis
	target := clamp(evY-m.mainTop, 0, maxInt(1, vis-1))
	m.viewport.SetYOffset(clamp(target*maxOff/maxInt(1, vis-1), 0, maxOff))
	m.autoScroll = m.viewport.AtBottom()
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
		appendCell(renderStream(m.stream, m, m.contentWidth, m.showThinking), nil, 0, -1)
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
