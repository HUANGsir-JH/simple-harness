package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/provider"
)

// ReadFileTool 读取文件内容，可限行范围。路径相对当前工作目录。
type ReadFileTool struct{}

func (ReadFileTool) Name() string { return "read_file" }

func (ReadFileTool) Spec() provider.ToolSpec {
	return provider.ToolSpec{
		Name:        "read_file",
		Description: "读取指定文件的文本内容。可传 start_line/end_line 限制行范围（1 起含）。路径相对当前工作目录。",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {"type": "string", "description": "要读取的文件路径（相对 cwd）"},
				"start_line": {"type": "integer", "description": "起始行（1 起含）"},
				"end_line": {"type": "integer", "description": "结束行（含）"}
			},
			"required": ["path"]
		}`),
	}
}

func (ReadFileTool) Handle(_ context.Context, _ *middleware.RuntimeContext, _ string, args json.RawMessage) (messages.ToolResult, error) {
	var p struct {
		Path      string `json:"path"`
		StartLine int    `json:"start_line"`
		EndLine   int    `json:"end_line"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "read_file: 参数解析失败: " + err.Error()}
	}
	if p.Path == "" {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "read_file: path 不能为空"}
	}
	content, err := readFileRange(p.Path, p.StartLine, p.EndLine)
	if err != nil {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "read_file: " + err.Error()}
	}
	return messages.ToolResult{Success: true, Content: truncate(content)}, nil
}

func readFileRange(path string, start, end int) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if start <= 0 && end <= 0 {
		return string(data), nil
	}
	lines := strings.Split(string(data), "\n")
	s, e := 1, len(lines)
	if start > 0 {
		s = start
	}
	if end > 0 {
		e = end
	}
	if s < 1 {
		s = 1
	}
	if e > len(lines) {
		e = len(lines)
	}
	if s > e {
		return "", fmt.Errorf("行范围无效（start=%d end=%d）", start, end)
	}
	return strings.Join(lines[s-1:e], "\n"), nil
}

// ListDirTool 列出目录条目（文件名 + 类型）。path 为空默认当前目录。
type ListDirTool struct{}

func (ListDirTool) Name() string { return "list_dir" }

func (ListDirTool) Spec() provider.ToolSpec {
	return provider.ToolSpec{
		Name:        "list_dir",
		Description: "列出目录条目（每条为 类型\\t名称，类型为 dir 或 file）。path 为空默认当前目录。",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {"type": "string", "description": "目录路径（相对 cwd，默认当前目录）"}
			}
		}`),
	}
}

func (ListDirTool) Handle(_ context.Context, _ *middleware.RuntimeContext, _ string, args json.RawMessage) (messages.ToolResult, error) {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "list_dir: 参数解析失败: " + err.Error()}
	}
	path := p.Path
	if path == "" {
		path = "."
	}
	content, err := listDirContents(path)
	if err != nil {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "list_dir: " + err.Error()}
	}
	return messages.ToolResult{Success: true, Content: truncate(content)}, nil
}

func listDirContents(path string) (string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, e := range entries {
		typ := "file"
		if e.IsDir() {
			typ = "dir"
		}
		fmt.Fprintf(&sb, "%s\t%s\n", typ, e.Name())
	}
	return sb.String(), nil
}

// GlobTool 按 glob 模式匹配文件路径。
type GlobTool struct{}

func (GlobTool) Name() string { return "glob" }

func (GlobTool) Spec() provider.ToolSpec {
	return provider.ToolSpec{
		Name:        "glob",
		Description: "按 glob 模式（如 *.go、**/*.md）匹配文件路径，返回匹配列表。",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"pattern": {"type": "string", "description": "glob 模式（相对 cwd）"}
			},
			"required": ["pattern"]
		}`),
	}
}

func (GlobTool) Handle(_ context.Context, _ *middleware.RuntimeContext, _ string, args json.RawMessage) (messages.ToolResult, error) {
	var p struct {
		Pattern string `json:"pattern"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "glob: 参数解析失败: " + err.Error()}
	}
	if p.Pattern == "" {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "glob: pattern 不能为空"}
	}
	matches, err := filepath.Glob(p.Pattern)
	if err != nil {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "glob: " + err.Error()}
	}
	if len(matches) == 0 {
		return messages.ToolResult{Success: true, Content: "（无匹配）"}, nil
	}
	return messages.ToolResult{Success: true, Content: truncate(strings.Join(matches, "\n"))}, nil
}
