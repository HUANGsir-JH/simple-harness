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
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/provider"
)

// powershellUTF8Prefix 是 PowerShell 脚本的 UTF-8 输出前缀（对标 codex 的
// codex-shell-command）：显式设置输出编码为 UTF-8，避免中文等经 console
// 代码页（默认 GBK）乱码。
const powershellUTF8Prefix = "try { [Console]::OutputEncoding=[System.Text.Encoding]::UTF8 } catch {}\n"

// ShellCommandTool 在 shell 中执行命令（ADR-038）。平台分派：Windows 用
// PowerShell，POSIX 用 sh -c。三种模式：
//   - 前台（默认）：同步阻塞至命令退出/超时/Esc 中断。Esc 与超时都**杀整棵
//     进程树**（Windows Job Object / POSIX 进程组，含派生孙进程），并回填
//     "已中断/已超时"提示；输出超长时完整版落盘 evictions/。
//   - background：后台启动立即返回 PID + 日志路径（长任务/服务启动用），
//     进程不绑定回合（Esc 不杀），用 read_file/grep 轮询日志，配套 kill_pid
//     终止；harness 退出时自动清理。
//   - kill_pid：终止 background 启动的进程（仅注册表内 PID，防误杀系统进程）。
type ShellCommandTool struct{}

func (ShellCommandTool) Name() string { return "shell_command" }

// shellCommandArgs 是 shell_command 的参数形状（C4，schema 单一来源）。
// command 为 omitempty：kill_pid 模式无需 command；Handle 校验二者至少其一
// 非空（都空报参数错误）。workdir/timeout_ms 的 omitempty 使其可选。
type shellCommandArgs struct {
	Command    string `json:"command,omitempty" jsonschema:"description=要执行的命令（kill_pid 模式可省略）"`
	Workdir    string `json:"workdir,omitempty" jsonschema:"description=工作目录（默认当前目录）"`
	TimeoutMS  int    `json:"timeout_ms,omitempty" jsonschema:"description=超时毫秒（默认 30000；background/kill_pid 模式忽略）"`
	Background bool   `json:"background,omitempty" jsonschema:"description=true 时后台启动并立即返回 PID 与日志路径（长任务/服务启动用；输出写入日志文件，用 read_file/grep 轮询；配套 kill_pid 终止）"`
	KillPID    int    `json:"kill_pid,omitempty" jsonschema:"description=终止指定后台进程（background 启动返回的 PID；提供时忽略 command）"`
}

func (ShellCommandTool) Spec() provider.ToolSpec {
	return provider.ToolSpec{
		Name: "shell_command",
		Description: "在 shell 中执行命令并返回输出（stdout+stderr 合并）。Windows 用 PowerShell，POSIX 用 sh -c。" +
			"Esc 中断与超时都会终止整个进程树。长任务/服务启动用 background: true 后台运行（返回 PID+日志路径，用 read_file/grep 轮询，kill_pid 终止）。" +
			"命令非零退出或超时返回错误文本（输出超长时完整版会保存到 evictions/ 目录并用 read_file 提示，错误信息含路径）。",
		Parameters: schemaOf[shellCommandArgs](),
	}
}

func (ShellCommandTool) Handle(ctx context.Context, rc *middleware.RuntimeContext, _ string, args json.RawMessage) (messages.ToolResult, error) {
	p, err := parseArgs[shellCommandArgs]("shell_command", args)
	if err != nil {
		return messages.ToolResult{}, err
	}

	// kill_pid 模式优先：终止后台进程（仅注册表内 PID，防误杀系统进程）。
	if p.KillPID > 0 {
		return handleKill(p.KillPID)
	}
	if p.Command == "" {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "shell_command: command 与 kill_pid 至少提供一个"}
	}

	// background 模式：启动即返回，不参与 timeout/ctx（进程寿命跨回合，
	// Esc 中断回合不杀；仅 kill_pid 与退出清理终止，ADR-038）。
	if p.Background {
		pid, logPath, err := startBackground(rc, p.Command, p.Workdir)
		if err != nil {
			return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "shell_command: 后台启动失败: " + err.Error()}
		}
		return messages.ToolResult{Success: true, Content: fmt.Sprintf(
			"已后台启动 PID %d，日志：%s\n用 read_file/grep 轮询日志判断进度；用 shell_command {\"kill_pid\": %d} 终止；会话结束自动清理",
			pid, logPath, pid)}, nil
	}

	// 前台模式。
	timeout := time.Duration(p.TimeoutMS) * time.Millisecond
	if p.TimeoutMS <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := newShellCmd(p.Command, p.Workdir)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	tree, err := startForeground(ctx, cmd)
	if err != nil {
		// Start 前 ctx 已取消的竞态兜底：区分中断/超时（Esc 与超时都返回
		// 明确语义，模型知道进程树状态）。
		if ctx.Err() == context.Canceled {
			return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "shell_command: 命令已被中断（Esc），进程树已终止"}
		}
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "shell_command: " + err.Error()}
	}
	defer closeProcessTree(tree)

	// Start 后 ctx 已取消的竞态兜底：AfterFunc 注册晚于取消瞬间时会漏杀，
	// 这里显式杀树（幂等）。
	if ctx.Err() != nil {
		killProcessTree(tree, cmd.Process.Pid)
	}

	err = cmd.Wait() // 杀树成功后树必死 → 管道写端全关 → Wait 必返回
	switch {
	case ctx.Err() == context.Canceled:
		// Esc 中断（ADR-038）：杀树已在 ctx 取消瞬间执行，这里幂等再杀一次
		// 防御；回填"命令已被中断"（模型可见、transcript 落盘）。
		killProcessTree(tree, cmd.Process.Pid)
		msg := "shell_command: 命令已被中断（Esc），进程树已终止"
		if out.Len() > 0 {
			msg += "\n" + EvictContent(rc, out.String())
		}
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: msg}
	case ctx.Err() == context.DeadlineExceeded:
		// 超时：杀树由 AfterFunc 承担（ctx 取消瞬间，不等 Wait）；已收集输出
		// 落盘（错误带路径，模型可用 read_file 读进度，不盲目重试）。
		msg := fmt.Sprintf("shell_command: 命令超时（%v），进程树已终止", timeout)
		if out.Len() > 0 {
			msg += "\n" + EvictContent(rc, out.String())
		}
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: msg}
	case err != nil:
		msg := "shell_command: " + err.Error()
		if out.Len() > 0 {
			msg += "\n" + EvictContent(rc, out.String())
		}
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: msg}
	default:
		return messages.ToolResult{Success: true, Content: out.String()}, nil
	}
}

// newShellCmd 按平台构造 shell 命令（**无 ctx**：前台经 startForeground 绑定
// 取消杀树，后台进程寿命独立于回合 ctx）。
//
// Windows 用 PowerShell（与 codex 一致）：规避 cmd 的引号转义坑——Go exec 的
// 参数转义用 `\"`，cmd.exe 的引号转义是 `""`（两者不兼容，含引号路径的命令
// 经 exec.Command 传给 cmd 会破坏路径）；PowerShell 走标准命令行解析，无此
// 问题。另加 UTF-8 输出前缀处理中文编码。
func newShellCmd(command, workdir string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", powershellUTF8Prefix+command)
		if workdir != "" {
			cmd.Dir = workdir
		}
		return cmd
	}
	cmd := exec.Command("sh", "-c", command)
	if workdir != "" {
		cmd.Dir = workdir
	}
	applyProcessGroup(cmd) // 独立进程组，Esc/超时/退出可杀整组（Bug06(b)）
	return cmd
}
