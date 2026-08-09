package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/agent-project/harness/internal/messages"
	"github.com/hexops/gotextdiff"
	"github.com/hexops/gotextdiff/myers"
	"github.com/hexops/gotextdiff/span"
)

// ToolStatus 是一个工具调用折叠块（ADR-030：消息流内插、按工具分派）。
// Collapsed 默认折叠；点击/Enter 切换展开（交互 W4，W3 先做状态机 + 渲染）。
type ToolStatus struct {
	ID      string // tool call id（ToolResult 关联）
	Name    string
	Args    []byte // 调用参数（result 分派用：write_file diff / read_file 统计）
	Summary string // 块头（调用摘要，如 shell_command: ls -la）
	Done    bool   // 执行完成
	Failed  bool   // 失败（非零退出/错误/审批拒绝）
	Content string // 折叠态内容（按工具分派生成）
	Full    string // 展开态全文（shell 输出 / diff / 完整枚举）
	// Collapsed 是否折叠（默认 true；展开交互 W4：点击/Enter 切换）。
	Collapsed bool

	oldContent string // write_file diff 用：ToolCall 时预读的旧文件（覆盖场景）
	oldExists  bool
}

// Expandable reports whether the block has detail beyond its compact view.
func (t *ToolStatus) Expandable() bool {
	if t == nil || t.Name == "read_file" || t.Full == "" {
		return false
	}
	return t.Full != t.Content || len(strings.Split(strings.TrimSpace(t.Full), "\n")) > 6
}

// toolCallSummary 按工具提取块头摘要（只含关键参数，body 参数不展示）。
func toolCallSummary(name string, args []byte) string {
	var p struct {
		Path    string `json:"path"`
		Pattern string `json:"pattern"`
		Command string `json:"command"`
	}
	_ = json.Unmarshal(args, &p)
	switch name {
	case "read_file", "list_dir", "write_file":
		return name + " " + p.Path
	case "glob":
		return name + " " + p.Pattern
	case "shell_command":
		return "shell_command: " + p.Command
	case "update_todo":
		return "update_todo"
	case "apply_patch":
		return "apply_patch"
	default:
		return name + " " + truncate(string(args), 60)
	}
}

// prepareTool 在 ToolCall 时预处理（write_file 覆盖 diff 需执行前读旧文件）。
func prepareTool(ts *ToolStatus) {
	if ts.Name != "write_file" {
		return
	}
	var p struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(ts.Args, &p) != nil || p.Path == "" {
		return
	}
	if old, err := os.ReadFile(p.Path); err == nil {
		ts.oldContent = string(old)
		ts.oldExists = true
	}
}

// applyToolResult 在 ToolResult 时按工具分派生成折叠态/展开态内容。
// 失败（错误/审批拒绝）时 Content 含错误信息（对齐失败态红 [ERR] + 错误 + 输出）。
func applyToolResult(ts *ToolStatus, res *messages.ToolResult) {
	ts.Done = true
	ts.Failed = !res.Success
	if !res.Success {
		ts.Content = truncate(res.Content, 240)
		ts.Full = res.Content
		return
	}
	switch ts.Name {
	case "read_file":
		// 元信息单行（不渲染内容）：行数 + 大小。
		lines := lineCount(res.Content)
		ts.Content = fmt.Sprintf("%s  |  %d lines  |  %s", readFilePath(ts.Args), lines, humanSize(len(res.Content)))
	case "write_file":
		ts.writeResult(res)
	case "apply_patch":
		// report（文件列表）+ diff（从 args.patch 提取 +/- 行）。
		ts.Content = res.Content
		if d := patchDiff(ts.Args); d != "" {
			ts.Content += "\n" + d
		}
		ts.Full = ts.Content
	case "list_dir":
		names := listDirNames(res.Content)
		ts.Content = fmt.Sprintf("%d items  %s", len(names), firstN(names, 5))
		ts.Full = strings.Join(names, "\n")
	case "glob":
		paths := splitLines(res.Content)
		ts.Content = fmt.Sprintf("%d matches  %s", len(paths), firstN(paths, 5))
		ts.Full = res.Content
	case "update_todo":
		ts.Content = res.Content // 完整 checklist
		ts.Full = res.Content
	case "shell_command":
		ts.Content = "exit 0" + headLines(res.Content, 5)
		ts.Full = res.Content
	default:
		ts.Content = truncate(res.Content, 200)
		ts.Full = res.Content
	}
}

// writeResult 生成 write_file 块内容：新建无 diff；覆盖显示 gotextdiff。
func (ts *ToolStatus) writeResult(res *messages.ToolResult) {
	path := readFilePath(ts.Args)
	newContent := writeFileContent(ts.Args)
	lines := strings.Count(newContent, "\n")
	if !ts.oldExists && ts.oldContent == "" {
		ts.Content = fmt.Sprintf("created %s  |  %d lines  |  %s", path, lines, humanSize(len(newContent)))
		return
	}
	edits := myers.ComputeEdits(span.URIFromPath(path), ts.oldContent, newContent)
	diff := fmt.Sprint(gotextdiff.ToUnified("old", "new", ts.oldContent, edits))
	ts.Content = fmt.Sprintf("updated %s  |  %d lines  |  %s\n%s", path, lines, humanSize(len(newContent)), diff)
	ts.Full = ts.Content
}

// readFilePath 提取 read_file/write_file 的 path 参数。
func readFilePath(args []byte) string {
	var p struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(args, &p)
	if p.Path == "" {
		return "?"
	}
	return p.Path
}

// writeFileContent 提取 write_file 的 content 参数。
func writeFileContent(args []byte) string {
	var p struct {
		Content string `json:"content"`
	}
	_ = json.Unmarshal(args, &p)
	return p.Content
}

// patchDiff 从 apply_patch 参数提取 +/- 行（diff 渲染；patch 自带 diff 语义）。
func patchDiff(args []byte) string {
	var p struct {
		Patch string `json:"patch"`
	}
	if json.Unmarshal(args, &p) != nil {
		return ""
	}
	var sb strings.Builder
	for _, line := range strings.Split(p.Patch, "\n") {
		if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
			sb.WriteString(line + "\n")
		}
	}
	return strings.TrimSuffix(sb.String(), "\n")
}

// splitLines 按行拆分并过滤空行（工具输出常以 \n 结尾）。
func splitLines(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func listDirNames(content string) []string {
	lines := splitLines(content)
	for i, line := range lines {
		if _, name, ok := strings.Cut(line, "\t"); ok {
			lines[i] = name
		}
	}
	return lines
}

func lineCount(content string) int {
	if content == "" {
		return 0
	}
	return strings.Count(strings.TrimSuffix(content, "\n"), "\n") + 1
}

// firstN 取前 n 项拼接（省略显示 +N）。
func firstN(items []string, n int) string {
	if len(items) == 0 {
		return "(empty)"
	}
	shown := items
	suffix := ""
	if len(items) > n {
		shown = items[:n]
		suffix = fmt.Sprintf(" ... +%d", len(items)-n)
	}
	return strings.Join(shown, " ") + suffix
}

// headLines 取前 n 行（补省略提示）。
func headLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
		return ""
	}
	if len(lines) <= n {
		return "\n" + strings.Join(lines, "\n")
	}
	return "\n" + strings.Join(lines[:n], "\n") + fmt.Sprintf("\n... +%d lines", len(lines)-n)
}

// humanSize 人类可读大小（B/KB/MB）。
func humanSize(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1fMB", float64(n)/(1024*1024))
	}
}
