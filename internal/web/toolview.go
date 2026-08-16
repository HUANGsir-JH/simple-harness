package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/agent-project/harness/internal/messages"
	"github.com/hexops/gotextdiff"
	"github.com/hexops/gotextdiff/myers"
	"github.com/hexops/gotextdiff/span"
)

// ToolView 是工具块的展示数据（state.timeline tool 元素 / SSE tool_result
// 事件载荷同构）。对位 tui.ToolStatus 的展示层（Content 折叠态 / Full
// 展开态），diff 额外生成 HTML 高亮（+ 绿 - 红）。
type ToolView struct {
	Name    string `json:"name"`
	Summary string `json:"summary"`
	Args    string `json:"args,omitempty"` // 格式化 JSON（截断 2KB）
	Content string `json:"content,omitempty"`
	Full    string `json:"full,omitempty"`
	Diff    string `json:"diff,omitempty"` // diff HTML（write_file/apply_patch）
	Failed  bool   `json:"failed,omitempty"`
	Done    bool   `json:"done,omitempty"`
}

// toolCallInfo 是 tool_call 时缓存的信息（tool_result 分派用）：write_file
// 覆盖场景预读旧文件（diff 需要；对位 tui prepareTool）。
type toolCallInfo struct {
	name       string
	args       json.RawMessage
	oldContent string
	oldExists  bool
}

// toolCallSummary 按工具提取块头摘要（对位 tui toolCallSummary）。
func toolCallSummary(name string, args []byte) string {
	var p struct {
		Path       string `json:"path"`
		Pattern    string `json:"pattern"`
		Command    string `json:"command"`
		KillPID    int    `json:"kill_pid"`
		Background bool   `json:"background"`
		Name       string `json:"name"`
	}
	_ = json.Unmarshal(args, &p)
	switch name {
	case "read_file", "list_dir", "write_file":
		return name + " " + p.Path
	case "glob":
		return name + " " + p.Pattern
	case "shell_command":
		if p.KillPID > 0 {
			return fmt.Sprintf("shell_command: kill %d", p.KillPID)
		}
		if p.Background {
			return "shell_command: " + p.Command + " &"
		}
		return "shell_command: " + p.Command
	case "update_todo":
		return "update_todo"
	case "apply_patch":
		return "apply_patch"
	case "skill":
		return "skill " + p.Name
	default:
		return name + " " + truncateStr(string(args), 60)
	}
}

// prepareToolCall 在 tool_call 时预处理（write_file 覆盖 diff 需执行前读
// 旧文件；对位 tui prepareTool）。
func prepareToolCall(name string, args json.RawMessage) *toolCallInfo {
	info := &toolCallInfo{name: name, args: args}
	if name != "write_file" {
		return info
	}
	var p struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(args, &p) != nil || p.Path == "" {
		return info
	}
	if old, err := os.ReadFile(p.Path); err == nil {
		info.oldContent = string(old)
		info.oldExists = true
	}
	return info
}

// toolBackground 判断 shell_command 是否 background 模式（结果展示区分）。
func toolBackground(args []byte) bool {
	var p struct {
		Background bool `json:"background"`
	}
	return json.Unmarshal(args, &p) == nil && p.Background
}

// applyToolResult 在 tool_result 时按工具分派生成展示数据（对位 tui
// applyToolResult 的 Content/Full 语义）。
func applyToolResult(info *toolCallInfo, res *messages.ToolResult) ToolView {
	v := ToolView{Name: info.name, Done: true, Failed: !res.Success}
	if info.name == "write_file" {
		v.Summary = toolCallSummary(info.name, info.args)
		v.Args = formatToolArgs(info.args)
	}
	if !res.Success {
		v.Content = truncateStr(res.Content, 240)
		v.Full = res.Content
		return v
	}
	switch info.name {
	case "read_file":
		lines := lineCount(res.Content)
		v.Content = fmt.Sprintf("%s  |  %d lines  |  %s", readFilePath(info.args), lines, humanSize(len(res.Content)))
		v.Full = res.Content
	case "write_file":
		v.writeResult(info)
	case "apply_patch":
		v.Content = res.Content
		if d := patchDiff(info.args); d != "" {
			v.Content += "\n" + d
			v.Diff = renderDiffHTML(d)
		}
		v.Full = v.Content
	case "list_dir":
		names := listDirNames(res.Content)
		v.Content = fmt.Sprintf("%d items  %s", len(names), firstN(names, 5))
		v.Full = strings.Join(names, "\n")
	case "glob":
		paths := splitLines(res.Content)
		v.Content = fmt.Sprintf("%d matches  %s", len(paths), firstN(paths, 5))
		v.Full = res.Content
	case "update_todo":
		v.Content = res.Content
		v.Full = res.Content
	case "skill":
		v.Content = fmt.Sprintf("loaded %s  |  %s", skillName(info.args), humanSize(len(res.Content)))
		v.Full = res.Content
	case "shell_command":
		if toolBackground(info.args) {
			v.Content = res.Content
			v.Full = res.Content
			break
		}
		v.Content = "exit 0" + headLines(res.Content, 5)
		v.Full = res.Content
	default:
		v.Content = truncateStr(res.Content, 200)
		v.Full = res.Content
	}
	// 展开态截断（防 MB 级输出随 state 快照全量下发；review L5）。
	v.Full = truncateFull(v.Full)
	return v
}

// writeResult 生成 write_file 块内容：新建无 diff；覆盖显示 gotextdiff。
// 旧文件内容来自 tool_call 时的预读（info.oldContent，对位 tui prepareTool）。
func (v *ToolView) writeResult(info *toolCallInfo) {
	path := readFilePath(info.args)
	newContent := writeFileContent(info.args)
	lines := strings.Count(newContent, "\n")
	if !info.oldExists && info.oldContent == "" {
		v.Content = fmt.Sprintf("created %s  |  %d lines  |  %s", path, lines, humanSize(len(newContent)))
		v.Full = newContent
		return
	}
	edits := myers.ComputeEdits(span.URIFromPath(path), info.oldContent, newContent)
	diff := fmt.Sprint(gotextdiff.ToUnified("old", "new", info.oldContent, edits))
	v.Content = fmt.Sprintf("updated %s  |  %d lines  |  %s\n%s", path, lines, humanSize(len(newContent)), diff)
	v.Full = v.Content
	v.Diff = renderDiffHTML(diff)
}

// renderDiffHTML 把 unified diff 文本渲染为 HTML（+ 行绿 / - 行红；前端
// 直接 innerHTML）。行内容经转义防注入。
func renderDiffHTML(diff string) string {
	var sb strings.Builder
	for _, line := range strings.Split(diff, "\n") {
		escaped := escapeHTML(line)
		switch {
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			sb.WriteString(`<span class="diff-add">` + escaped + "</span>\n")
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			sb.WriteString(`<span class="diff-del">` + escaped + "</span>\n")
		case strings.HasPrefix(line, "@@"):
			sb.WriteString(`<span class="diff-hunk">` + escaped + "</span>\n")
		default:
			sb.WriteString(escaped + "\n")
		}
	}
	return sb.String()
}

// --- 纯工具函数（对位 tui/tool.go） -------------------------------------------

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

func skillName(args []byte) string {
	var p struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(args, &p)
	if p.Name == "" {
		return "?"
	}
	return p.Name
}

func writeFileContent(args []byte) string {
	var p struct {
		Content string `json:"content"`
	}
	_ = json.Unmarshal(args, &p)
	return p.Content
}

// patchDiff 从 apply_patch 参数提取 +/- 行（diff 渲染；对位 tui patchDiff）。
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

func truncateStr(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "..."
}

// truncateFull 截断展开态内容（100KB；review L5）。
func truncateFull(s string) string {
	const maxFull = 100 * 1024
	if len(s) <= maxFull {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxFull]) + "\n...（内容过长已截断）"
}

// formatToolArgs 格式化工具参数（JSON 缩进，截断 2KB；前端 args 展示）。
func formatToolArgs(args []byte) string {
	if len(args) == 0 {
		return "{}"
	}
	var value any
	if err := json.Unmarshal(args, &value); err != nil {
		return truncateStr(string(args), 2048)
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return truncateStr(string(args), 2048)
	}
	formatted := strings.TrimSuffix(buf.String(), "\n")
	if formatted == "" {
		return truncateStr(string(args), 2048)
	}
	return truncateStr(formatted, 2048)
}
