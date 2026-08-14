package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// ---- 统一弹窗框架（ADR-043）----
//
// 四个弹窗（审批/ask/选择器/帮助）共用一个骨架：标题行 + 分隔线 + 内容 + 提示
// 行，圆角边框 + panel 背景。几何继续经 modalPanelWidth/modalInnerWidth 收敛
// （ADR-032 单一来源），title/body/hint 由调用方完成样式（颜色语义归调用方）。

const (
	modalBorderWidth  = 2 // 左右各一列边框
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

// modalInnerWidth 是弹窗渲染后真正可用的文本宽度（lipgloss Width 含 padding
// 不含 border，两者都要扣掉）。
func modalInnerWidth(panelWidth int) int {
	return maxInt(1, panelWidth-modalBorderWidth-modalPaddingWidth)
}

// dialogSpec 是统一弹窗的排版输入（title/body/hint 均已按需样式化与换行）。
type dialogSpec struct {
	title string
	body  string
	hint  string
	width int
}

// renderDialog 渲染统一弹窗骨架：标题 + 分隔线 + 内容 + 提示行。
func renderDialog(spec dialogSpec) string {
	inner := modalInnerWidth(spec.width)
	parts := []string{
		spec.title,
		styleBorder.Render(strings.Repeat("─", inner)),
	}
	if spec.body != "" {
		parts = append(parts, spec.body)
	}
	if spec.hint != "" {
		parts = append(parts, spec.hint)
	}
	return dialogStyle(spec.width).Render(strings.Join(parts, "\n"))
}

// dialogStyle 统一弹窗外框：圆角边框（主题 Border）+ panel 背景 + 内边距。
func dialogStyle(panelWidth int) lipgloss.Style {
	return lipgloss.NewStyle().
		Width(modalInnerWidth(panelWidth)+modalPaddingWidth).
		Padding(1, 2).
		Background(colorPanel).
		Foreground(colorText).
		BorderStyle(currentTheme().Border).
		BorderForeground(colorBorder)
}

// renderApproval 渲染审批弹窗（键位 y/s/n/esc 不变；标题与提示文案为 e2e 契约）。
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
	body := styleText.Render(ansi.Truncate(appr.req.ToolName, bodyWidth, "...")) + "\n" +
		styleMuted.Render(summary)
	return renderDialog(dialogSpec{
		title: styleRunning.Render("PERMISSION REQUIRED"),
		body:  body,
		hint:  hintLine,
		width: panelWidth,
	})
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
	body := styleText.Render(question)
	if len(rows) > 0 {
		body += "\n\n" + strings.Join(rows, "\n")
	}
	if ask.req.AllowCustom {
		// Custom 输入行仅在允许自定义时渲染（ADR-036 修订：AllowCustom 约束对齐
		// run 模式 ParseAskAnswer，禁止自定义时不诱导输入）。
		body += "\n\n" + styleMuted.Render(ansi.Truncate("Custom: "+ask.custom+"_", maxInt(1, bodyWidth), "..."))
	}
	return renderDialog(dialogSpec{
		title: styleRunning.Render(header),
		body:  body,
		hint:  styleMuted.Render(hint),
		width: panelWidth,
	})
}

// renderPopup 渲染选择器弹窗（/switch /model /effort /permission /thinking）。
func renderPopup(sel *selectPopup, screenWidth, availableHeight int) string {
	panelWidth := modalPanelWidth(screenWidth, 34, 64)
	maxRows := maxInt(3, availableHeight-7) // 统一骨架比旧版多 1 行（分隔线+提示），预留高度相应调整
	start := 0
	if len(sel.items) > maxRows {
		start = sel.cursor - maxRows/2
		if start < 0 {
			start = 0
		}
		if start+maxRows > len(sel.items) {
			start = len(sel.items) - maxRows
		}
	}
	end := start + maxRows
	if end > len(sel.items) {
		end = len(sel.items)
	}
	listWidth := modalInnerWidth(panelWidth)
	var rows []string
	for i := start; i < end; i++ {
		prefix := "  "
		if i == sel.cursor {
			prefix = "> "
		}
		row := ansi.Truncate(prefix+sel.items[i].label, maxInt(1, listWidth), "...")
		if i == sel.cursor {
			row = styleSelected.Width(listWidth).Render(row)
		} else {
			row = lipgloss.NewStyle().Width(listWidth).Render(row)
		}
		rows = append(rows, row)
	}
	return renderDialog(dialogSpec{
		title: styleAssistant.Render(sel.title),
		body:  strings.Join(rows, "\n"),
		hint:  styleMuted.Render(ansi.Truncate("Enter confirm   Esc cancel", maxInt(1, listWidth), "...")),
		width: panelWidth,
	})
}

// renderHelp 渲染帮助弹窗：命令与键位双列（窄屏退化为单列竖排）。
func renderHelp(width int) string {
	panelWidth := modalPanelWidth(width, 38, 78)
	innerWidth := modalInnerWidth(panelWidth)
	left := strings.Join([]string{
		styleAssistant.Render("COMMANDS"),
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
	return renderDialog(dialogSpec{
		title: styleAssistant.Render("HELP"),
		body:  content,
		hint:  styleMuted.Render("Esc close"),
		width: panelWidth,
	})
}
