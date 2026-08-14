package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/agent-project/harness/internal/messages"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// messageCell is a rendered message plus the line range (relative to the cell)
// occupied by its thinking block. thinkingStart < 0 means there is none.
type messageCell struct {
	body          string
	thinkingStart int
	thinkingEnd   int
}

// renderMessageItem 渲染消息 cell（ADR-043 视觉重构）：
//   - user：首行 `›` 前缀（accent bold）+ 内容，后续行缩进 2（codex
//     UserHistoryCell 同款，去掉 YOU 标签）；
//   - assistant：无标签，thinking 折叠块（含标题/字符数/耗时）+ 正文；
//   - system/error：muted/红色 SYSTEM/ERROR 标签行 + 内容。
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
		return messageCell{
			body:          renderUserCell(content),
			thinkingStart: -1,
			thinkingEnd:   -1,
		}
	}

	var parts []string
	thinkingStart, thinkingEnd := -1, -1
	if showThinking && item.Thinking != "" {
		block := renderThinkingBlock(item, width, thinkingSelected)
		// 无 ASSISTANT 标签后，thinking 块是 cell 首行。
		thinkingStart = 0
		thinkingEnd = lipgloss.Height(block) - 1
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

// renderUserCell 渲染用户消息：`›` 前缀 + 首行；后续行缩进 2 列。
func renderUserCell(content string) string {
	lines := strings.Split(content, "\n")
	first := styleUser.Render("›") + " " + lines[0]
	for i := 1; i < len(lines); i++ {
		lines[i] = "  " + lines[i]
	}
	lines[0] = first
	return strings.Join(lines, "\n")
}

// thinkingTitle 抽取 thinking 文本中首个 `**粗体标题**`（codex extract_first_bold
// 同款）；无标题/超长返回空。
func thinkingTitle(s string) string {
	first := strings.Index(s, "**")
	if first < 0 {
		return ""
	}
	rest := s[first+2:]
	end := strings.Index(rest, "**")
	if end < 0 {
		return ""
	}
	title := strings.TrimSpace(rest[:end])
	if title == "" || len([]rune(title)) > 40 {
		return ""
	}
	return title
}

// renderThinkingBlock 渲染 thinking 折叠块（ADR-043）：单行折叠 + 展开淡化全文。
// 折叠行含耗时（用户追加需求，纯 UI 计时；历史块无时间戳则省略）。
func renderThinkingBlock(item *MessageItem, width int, selected bool) string {
	label := "Thinking"
	if title := thinkingTitle(item.Thinking); title != "" {
		label += " · " + title
	}
	label += fmt.Sprintf(" · %d chars", len([]rune(item.Thinking)))
	if item.ThinkingDuration > 0 {
		label += " · " + formatDuration(item.ThinkingDuration)
	}
	state := "expanded"
	if !item.ThinkingExpanded {
		state = "collapsed"
	}
	label += "  [" + state + "]"
	if selected {
		label = styleSelected.Render("> ") + styleMuted.Render(label)
	} else {
		label = styleMuted.Render(label)
	}
	block := label
	if item.ThinkingExpanded {
		thinking := ansi.Hardwrap(strings.TrimSpace(item.Thinking), width-2, true)
		if title := thinkingTitle(item.Thinking); title != "" {
			thinking = strings.Replace(thinking, "**"+title+"**", styleText.Render(title), 1)
		}
		block = label + "\n" + styleMuted.Render(thinking)
	}
	return block
}

// renderStream 渲染流式中的 assistant 块：无标签；thinking 灰显 + 实时耗时（首
// 个 thinking 增量打点），正文纯文本（块完成才走 markdown，ADR-030 决策不变）。
func renderStream(stream *StreamState, m *Model, width int, showThinking bool) string {
	var parts []string
	if showThinking && stream.Thinking != "" {
		label := styleMuted.Render("Thinking")
		if m.thinkingSince != nil {
			label += styleMuted.Render(" · " + formatDuration(time.Since(*m.thinkingSince)))
		}
		parts = append(parts, label)
		parts = append(parts, styleMuted.Render(ansi.Hardwrap(stream.Thinking, width-2, true)))
	}
	if stream.Text != "" {
		parts = append(parts, styleText.Render(ansi.Hardwrap(stream.Text, width-2, true)))
	}
	return strings.Join(parts, "\n")
}

// formatDuration 人类可读耗时：<1s 毫秒、<1m 一位小数秒、≥1m 分秒。
func formatDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		m := int(d.Minutes())
		return fmt.Sprintf("%dm%02ds", m, int(d.Seconds())%60)
	}
}
