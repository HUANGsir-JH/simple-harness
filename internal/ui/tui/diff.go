package tui

import (
	"bytes"
	"strings"

	"github.com/alecthomas/chroma/v2/quick"
	_ "github.com/alecthomas/chroma/v2/styles" // 注册内置样式（github-dark 等）
)

// colorDiffLine diff 行着色：+++/--- 与 @@ hunk 元信息 muted、+ 新增绿、- 删除红。
func colorDiffLine(line string) string {
	switch {
	case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") || strings.HasPrefix(line, "@@"):
		return styleMuted.Render(line)
	case strings.HasPrefix(line, "+"):
		return styleAdd.Render(line)
	case strings.HasPrefix(line, "-"):
		return styleDelete.Render(line)
	default:
		return line
	}
}

// highlightFileContent 按文件扩展名做语法高亮（chroma terminal256 + github-dark，
// ADR-043 diff.go）；语言未识别/失败时回退原文（渲染增强，不改变内容语义）。
func highlightFileContent(path, content string) string {
	content = strings.TrimRight(content, "\n")
	if content == "" {
		return ""
	}
	var buf bytes.Buffer
	if err := quick.Highlight(&buf, content, path, "terminal256", "github-dark"); err != nil {
		return content
	}
	// terminal256 格式器无行号；换行由调用方按块宽度 Hardwrap。
	return strings.TrimRight(buf.String(), "\n")
}
