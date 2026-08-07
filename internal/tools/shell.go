package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/provider"
)

// ShellCommandTool 在 shell 中执行命令。平台分派：Windows 用 cmd，POSIX 用
// sh -c。命令非零退出返回错误文本（可重试）。
type ShellCommandTool struct{}

func (ShellCommandTool) Name() string { return "shell_command" }

func (ShellCommandTool) Spec() provider.ToolSpec {
	return provider.ToolSpec{
		Name:        "shell_command",
		Description: "在 shell 中执行命令并返回输出（stdout+stderr 合并，截断 20k 字符）。Windows 用 cmd，POSIX 用 sh -c。命令非零退出或超时返回错误文本。",
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

	cmd, cleanup, err := platformShellCommand(ctx, p.Command, p.Workdir)
	if err != nil {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "shell_command: " + err.Error()}
	}
	if cleanup != nil {
		defer cleanup()
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	err = cmd.Run()
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

// platformShellCommand 按平台构造 shell 命令，返回 (cmd, cleanup)。
//
// Windows 坑（真实验证）：cmd.exe 的引号转义用 `""`，不认 Go exec 的 `\"`
// 转义——含引号的命令（如 `dir "D:\path"`）经 exec.Command 参数转义后传给
// cmd 会破坏路径（"syntax is incorrect"）。解决：把命令写入临时 .bat 由
// cmd /C 执行，命令原样（引号/中文均正常），cleanup 删除临时文件。
func platformShellCommand(ctx context.Context, command, workdir string) (*exec.Cmd, func(), error) {
	if runtime.GOOS == "windows" {
		bat := filepath.Join(os.TempDir(), fmt.Sprintf("harness_%d.bat", time.Now().UnixNano()))
		content := "@echo off\r\n" + command + "\r\nexit /b %errorlevel%\r\n"
		if err := os.WriteFile(bat, []byte(content), 0o644); err != nil {
			return nil, nil, fmt.Errorf("创建临时批处理: %w", err)
		}
		cmd := exec.CommandContext(ctx, "cmd", "/C", bat)
		if workdir != "" {
			cmd.Dir = workdir
		}
		return cmd, func() { _ = os.Remove(bat) }, nil
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	if workdir != "" {
		cmd.Dir = workdir
	}
	return cmd, nil, nil
}
