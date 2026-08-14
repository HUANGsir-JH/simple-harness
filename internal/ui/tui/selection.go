package tui

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// selPoint 是时间线内容坐标（viewport 内容行 + 显示列，ANSI 序列不计宽）。
type selPoint struct {
	line int
	col  int
}

// applySelection 在时间线内容上应用选区样式（ADR-043 §6.7）：中间行整行加
// 选中背景，首/末行按显示列区间加背景（ANSI 感知，行内既有前景样式不破坏）。
func (m *Model) applySelection(content string) string {
	a, b := m.selAnchor, m.selEnd
	if a == b {
		return content
	}
	if a.line > b.line || (a.line == b.line && a.col > b.col) {
		a, b = b, a
	}
	lines := strings.Split(content, "\n")
	if a.line >= len(lines) {
		return content
	}
	last := b.line
	if last >= len(lines) {
		last = len(lines) - 1
	}
	for i := a.line; i <= last; i++ {
		start, end := 0, -1 // -1 = 整行
		if i == a.line {
			start = a.col
		}
		if i == b.line {
			end = b.col
		}
		lines[i] = wrapSelected(lines[i], start, end)
	}
	return strings.Join(lines, "\n")
}

// wrapSelected 在样式化行 s 的显示列区间 [start, end)（end<0 = 行尾）加选中
// 背景。只插入背景色序列（结束用 \x1b[49m 复位背景），不扰动行内前景样式。
func wrapSelected(s string, start, end int) string {
	if end >= 0 && end <= start {
		return s
	}
	st := colToBytes(s, start)
	en := len(s)
	if end >= 0 {
		en = colToBytes(s, end)
	}
	return s[:st] + selBgOpen() + s[st:en] + "\x1b[49m" + s[en:]
}

// selBgOpen 选区背景开启序列（TokenRaised，按当前色彩档案生成；非彩色档案
// 下为空串，选区不可见但复制仍正确）。
func selBgOpen() string {
	p := lipgloss.ColorProfile()
	return p.Color(string(colorRaised)).Sequence(true)
}

// colToBytes 返回样式化行 s 中显示列 col 对应的字节偏移（ANSI 序列不计宽；
// col 超出行长时钳制到行尾）。
func colToBytes(s string, col int) int {
	if col <= 0 {
		return 0
	}
	c := 0
	i := 0
	for i < len(s) {
		r, w := utf8.DecodeRuneInString(s[i:])
		if r == 0x1b {
			i += ansiSeqLen(s, i)
			continue
		}
		if c >= col {
			break
		}
		c += runewidth.RuneWidth(r)
		i += w
	}
	return i
}

// ansiSeqLen 返回 s[i:] 处 ESC 序列的字节长度（CSI / OSC / 双字节短序列），
// 用于显示列换算时跳过样式序列。
func ansiSeqLen(s string, i int) int {
	// s[i] == 0x1b
	if i+1 >= len(s) {
		return 1
	}
	switch s[i+1] {
	case '[': // CSI：ESC [ ... final byte(0x40-0x7e)
		for j := i + 2; j < len(s); j++ {
			if s[j] >= 0x40 && s[j] <= 0x7e {
				return j + 1 - i
			}
		}
		return len(s) - i
	case ']': // OSC：ESC ] ... BEL 或 ST
		for j := i + 2; j < len(s); j++ {
			if s[j] == 0x07 {
				return j + 1 - i
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2 - i
			}
		}
		return len(s) - i
	default: // 双字节短序列（如 ESC(B）
		return 2
	}
}

// selectionText 取选区纯文本（Ctrl+C 复制用；ansi.Strip 后按显示列切）。
func (m *Model) selectionText() string {
	a, b := m.selAnchor, m.selEnd
	if a == b {
		return ""
	}
	if a.line > b.line || (a.line == b.line && a.col > b.col) {
		a, b = b, a
	}
	lines := strings.Split(m.content, "\n")
	if a.line >= len(lines) {
		return ""
	}
	var out []string
	if a.line == b.line {
		out = append(out, lineText(lines[a.line], a.col, b.col))
	} else {
		out = append(out, lineText(lines[a.line], a.col, -1))
		for i := a.line + 1; i < b.line && i < len(lines); i++ {
			out = append(out, ansi.Strip(lines[i]))
		}
		if b.line < len(lines) {
			out = append(out, lineText(lines[b.line], 0, b.col))
		}
	}
	return strings.Join(out, "\n")
}

// lineText 取样式化行的 [start, end)（end<0 = 行尾）纯文本。
func lineText(s string, start, end int) string {
	st := colToBytes(s, start)
	if end < 0 {
		return ansi.Strip(s[st:])
	}
	return ansi.Strip(s[st:colToBytes(s, end)])
}
