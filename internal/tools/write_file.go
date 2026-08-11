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

// writeFileArgs 是 write_file 的参数形状（C4，schema 单一来源）。
type writeFileArgs struct {
	Path    string `json:"path" jsonschema:"description=文件路径（相对 cwd 或绝对路径）"`
	Content string `json:"content" jsonschema:"description=要写入的完整文件内容"`
}

func (WriteFileTool) Spec() provider.ToolSpec {
	return provider.ToolSpec{
		Name:        "write_file",
		Description: "覆盖写入一个文件的完整内容（创建或覆盖已有文件，自动创建父目录）。用于整文件重写或新建；小改动优先用 apply_patch 做差异编辑。路径相对进程工作目录或绝对路径。",
		Parameters:  schemaOf[writeFileArgs](),
	}
}

func (WriteFileTool) Handle(_ context.Context, rc *middleware.RuntimeContext, _ string, args json.RawMessage) (messages.ToolResult, error) {
	p, err := parseArgs[writeFileArgs]("write_file", args)
	if err != nil {
		return messages.ToolResult{}, err
	}
	if p.Path == "" {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "write_file: path 不能为空"}
	}
	// 相对路径以 workspace 为基解析为绝对（Bug03）。
	path := ResolveInWorkspace(rc, p.Path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "write_file: 创建目录: " + err.Error()}
	}
	if err := os.WriteFile(path, []byte(p.Content), 0o644); err != nil {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "write_file: " + err.Error()}
	}
	return messages.ToolResult{Success: true, Content: "Write File: " + path}, nil
}
