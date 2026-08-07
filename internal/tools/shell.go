package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"time"

	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/provider"
)

// ShellCommandTool 在 shell 中执行命令。平台分派：Windows 用 cmd /C，
// POSIX 用 sh -c（与 codex 语义一致）。命令非零退出返回错误文本（可重试）。
type ShellCommandTool struct{}

func (ShellCommandTool) Name() string { return "shell_command" }

func (ShellCommandTool) Spec() provider.ToolSpec {
	return provider.ToolSpec{
		Name:        "shell_command",
		Description: "在 shell 中执行命令并返回输出（stdout+stderr 合并，截断 20k 字符）。Windows 用 cmd /C，POSIX 用 sh -c。命令非零退出或超时返回错误文本。",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"command": {"type": "string", "description": "要执行的命令"},
				"workdir": {"type": "string", "description": "工作目录（默认当前目录）"},
				"timeout_ms": {"type": "integer", "description": "超时毫秒（默认 30000）"}
			},
			"required": ["command"]
		}`),
	}
}

func (ShellCommandTool) Handle(ctx context.Context, _ string, args json.RawMessage) (messages.ToolResult, error) {
	var p struct {
		Command   string `json:"command"`
		Workdir   string `json:"workdir"`
		TimeoutMS int    `json:"timeout_ms"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "shell_command: 参数解析失败: " + err.Error()}
	}
	if p.Command == "" {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "shell_command: command 不能为空"}
	}
	timeout := time.Duration(p.TimeoutMS) * time.Millisecond
	if p.TimeoutMS <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := platformShellCommand(ctx, p.Command)
	if p.Workdir != "" {
		cmd.Dir = p.Workdir
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: fmt.Sprintf("shell_command: 命令超时（%v）", timeout)}
	}
	if err != nil {
		msg := "shell_command: " + err.Error()
		if out.Len() > 0 {
			msg += "\n" + truncate(out.String())
		}
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: msg}
	}
	return messages.ToolResult{Success: true, Content: truncate(out.String())}, nil
}

// platformShellCommand 按平台选择 shell：Windows cmd /C，POSIX sh -c。
func platformShellCommand(ctx context.Context, command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "cmd", "/C", command)
	}
	return exec.CommandContext(ctx, "sh", "-c", command)
}
