package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ---- 时间线组装 + cell 渲染缓存（ADR-043 Phase 5）----

// cellCache 是单个 timeline cell 的渲染缓存。key 覆盖影响该 cell 渲染的全部
// 输入：宽度、选中态、折叠态（message=ThinkingExpanded / tool=!Collapsed）、
// thinking 展示开关。运行中的工具块不缓存（耗时实时变化）；流式尾块不缓存
// （每 delta 变化）。正确性优先于缓存收益——key 不全会导致错渲染。
type cellCache struct {
	body          string
	width         int
	selected      bool
	expanded      bool
	showThinking  bool
	thinkingStart int
	thinkingEnd   int
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

	for i := range m.items {
		item := &m.items[i]
		switch item.kind {
		case itemMessage:
			cell := renderMessageCached(m, item, m.isThinkingSelected(item.msg))
			var hit *hitTarget
			if cell.thinkingStart >= 0 {
				h := hitTarget{kind: hitThinking, message: item.msg}
				hit = &h
			}
			appendCell(cell.body, hit, cell.thinkingStart, cell.thinkingEnd)
		case itemTool:
			body := renderToolCached(m, item, m.isToolSelected(item.tool))
			var hit *hitTarget
			if item.tool.Expandable() {
				h := hitTarget{kind: hitTool, tool: item.tool}
				hit = &h
			}
			appendCell(body, hit, 0, -1)
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

// renderMessageCached 消息 cell 渲染缓存：key = 宽度/选中/thinking 折叠/展示开关。
// Done 消息的内容与 Rendered 不可变（流式经 renderStream 单独渲染），缓存安全。
func renderMessageCached(m *Model, item *timelineItem, selected bool) messageCell {
	c := &item.cache
	msg := item.msg
	if c.body != "" && c.width == m.contentWidth && c.selected == selected &&
		c.expanded == msg.ThinkingExpanded && c.showThinking == m.showThinking {
		return messageCell{body: c.body, thinkingStart: c.thinkingStart, thinkingEnd: c.thinkingEnd}
	}
	cell := renderMessageItem(msg, m.contentWidth, m.showThinking, selected)
	c.body = cell.body
	c.width = m.contentWidth
	c.selected = selected
	c.expanded = msg.ThinkingExpanded
	c.showThinking = m.showThinking
	c.thinkingStart = cell.thinkingStart
	c.thinkingEnd = cell.thinkingEnd
	return cell
}

// renderToolCached 工具 cell 渲染缓存：运行中的块完全绕过缓存（耗时实时变化
// 且 Done 翻转会改变渲染，缓存读写都不做）；Done 后 key = 宽度/选中/折叠态。
func renderToolCached(m *Model, item *timelineItem, selected bool) string {
	c := &item.cache
	tool := item.tool
	if !tool.Done {
		return renderToolBlock(tool, m.contentWidth, selected)
	}
	if c.body != "" && c.width == m.contentWidth && c.selected == selected &&
		c.expanded == !tool.Collapsed {
		return c.body
	}
	body := renderToolBlock(tool, m.contentWidth, selected)
	c.body = body
	c.width = m.contentWidth
	c.selected = selected
	c.expanded = !tool.Collapsed
	return body
}

// renderMessages remains as a small compatibility helper for focused view tests.
func renderMessages(m *Model) string {
	content, _ := renderTimeline(m)
	return content
}
