package tui

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// ---- 工具 cell 渲染（ADR-043 双形态）----

// renderToolBlock 渲染工具 cell：
//   - Inline（单行）：read_file/list_dir/glob/update_todo/后台 shell ——
//     徽章 + 摘要 + 耗时一行，不参与折叠展开；
//   - Block（边框块）：shell 输出 / write_file diff / apply_patch diff / 失败
//     详情 —— 状态行 + 折叠内容 + `… +N lines` 提示；展开态 = 完整 args
//     （不截断，用户追加需求）+ 完整结果（diff 着色 / 代码高亮）。
func renderToolBlock(tool *ToolStatus, width int, selected bool) string {
	status, statusStyle := "[RUN]", styleRunning
	if tool.Done {
		status = "[OK]"
		statusStyle = styleSuccess
		if tool.Failed {
			status = "[ERR]"
			statusStyle = styleError
		}
	}
	head := statusStyle.Render(status) + " " + styleText.Render(ansi.Truncate(tool.Summary, maxInt(8, width-10), "..."))
	if dur := toolDuration(tool); dur != "" {
		head += styleMuted.Render(" · " + dur)
	}
	if selected {
		head = styleSelected.Render("> ") + head
	}
	if tool.Inline {
		body := tool.InlineBody
		if body == "" && !tool.Done {
			body = "running…"
		}
		body = "  " + styleMuted.Render(ansi.Truncate(body, maxInt(8, width-4), "..."))
		return head + "\n" + body
	}

	content := tool.Content
	if !tool.Collapsed && tool.Full != "" {
		content = tool.Full
	}
	if !tool.Collapsed {
		// 展开态 = 完整 args（不截断）+ 完整结果（用户追加需求）。
		result := content
		if result == "" {
			result = "waiting for result"
		}
		content = renderArgsSection(tool, width) + "\n" + result
		if tool.Name == "write_file" && !tool.oldExists && tool.oldContent == "" && tool.Full != "" {
			// 新建文件：args + meta 行 + 正文（按扩展名语法高亮）。
			content = renderArgsSection(tool, width) + "\n" + tool.Content + "\n" +
				highlightFileContent(readFilePath(tool.Args), tool.Full)
		}
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
	if tool.Expandable() && tool.Collapsed {
		body += "\n" + styleMuted.Render("  … +"+strconv.Itoa(total-6)+" lines")
	}
	return head + "\n" + lipgloss.NewStyle().
		BorderStyle(lipgloss.Border{Left: "|"}).
		BorderLeft(true).
		BorderForeground(colorBorder).
		PaddingLeft(1).
		Render(body)
}

// toolDuration 工具调用耗时（运行中实时、结束后定格；resume 历史无打点不显示）。
func toolDuration(tool *ToolStatus) string {
	if tool.Done {
		if tool.Duration > 0 {
			return formatDuration(tool.Duration)
		}
		return ""
	}
	if !tool.Started.IsZero() {
		return formatDuration(time.Since(tool.Started))
	}
	return ""
}

// prettyArgs 参数 JSON 缩进排版（非 JSON 兜底原文；展开态不截断的原始数据）。
func prettyArgs(args []byte) string {
	if len(args) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(args, &v); err != nil {
		return string(args)
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return string(args)
	}
	return string(b)
}

// renderArgsSection 展开态的完整参数段：`args` 标签 + 缩进排版全文。
func renderArgsSection(tool *ToolStatus, width int) string {
	pretty := prettyArgs(tool.Args)
	if pretty == "" {
		return ""
	}
	lines := strings.Split(ansi.Hardwrap(pretty, maxInt(8, width-4), true), "\n")
	for i, line := range lines {
		lines[i] = "  " + line
	}
	return styleMuted.Render("  args") + "\n" + strings.Join(lines, "\n")
}
