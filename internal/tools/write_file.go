package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/provider"
)

// WriteFileTool 覆盖写入一个文件的完整内容（创建或覆盖已有文件，自动创建
// 父目录）。用于整文件重写或新建；小改动优先用 apply_patch（差异编辑）。
type WriteFileTool struct{}

func (WriteFileTool) Name() string { return "write_file" }

func (WriteFileTool) Spec() provider.ToolSpec {
	return provider.ToolSpec{
		Name:        "write_file",
		Description: "覆盖写入一个文件的完整内容（创建或覆盖已有文件，自动创建父目录）。用于整文件重写或新建；小改动优先用 apply_patch 做差异编辑。路径相对进程工作目录或绝对路径。",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {"type": "string", "description": "文件路径（相对 cwd 或绝对路径）"},
				"content": {"type": "string", "description": "要写入的完整文件内容"}
			},
			"required": ["path", "content"]
		}`),
	}
}

func (WriteFileTool) Handle(_ context.Context, _ *middleware.RuntimeContext, _ string, args json.RawMessage) (messages.ToolResult, error) {
	var p struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "write_file: 参数解析失败: " + err.Error()}
	}
	if p.Path == "" {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "write_file: path 不能为空"}
	}
	if err := os.MkdirAll(filepath.Dir(p.Path), 0o755); err != nil {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "write_file: 创建目录: " + err.Error()}
	}
	if err := os.WriteFile(p.Path, []byte(p.Content), 0o644); err != nil {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "write_file: " + err.Error()}
	}
	return messages.ToolResult{Success: true, Content: "Write File: " + p.Path}, nil
}
