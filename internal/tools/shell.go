package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
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
//   - 前台（默认）：同步阻塞至命令退出/超时/Esc 中断。Esc 杀**整棵进程树**
//     （Windows Job Object / POSIX 进程组，含派生孙进程）并回填"已中断"；
//     **正常退出不杀派生进程**（`npm run dev &` 起的服务随命令返回继续运行，
//     终端式语义）；**超时不杀树，自动转入后台托管**（返回 PID+日志路径，模型轮询日志，
//     用 kill_pid 终止，不要重试——命令可能仍在运行）；输出超长时完整版由
//     ToolOutputMiddleware 统一落盘 evictions/（工具返回完整结果，ADR-028）。
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
	TimeoutMS  int    `json:"timeout_ms,omitempty" jsonschema:"description=超时毫秒（默认 30000；超时后命令自动转入后台继续运行并返回 PID 与日志路径，完成会自动通知；background/kill_pid 模式忽略）"`
	Background bool   `json:"background,omitempty" jsonschema:"description=true 时后台启动并立即返回 PID 与日志路径（长任务/服务启动用；输出写入日志文件，完成会自动通知，可等通知也可用 read_file/grep 轮询；配套 kill_pid 终止）"`
	KillPID    int    `json:"kill_pid,omitempty" jsonschema:"description=终止指定后台进程（background 启动返回的 PID；提供时忽略 command）"`
}

func (ShellCommandTool) Spec() provider.ToolSpec {
	return provider.ToolSpec{
		Name: "shell_command",
		Description: "在 shell 中执行命令并返回输出（stdout+stderr 合并）。Windows 用 PowerShell，POSIX 用 sh -c。" +
			"Esc 中断会终止整个进程树；超时自动转入后台（返回 PID+日志路径，完成会自动通知，也可轮询日志、kill_pid 终止，不要重试）。" +
			"长任务/服务启动用 background: true 后台运行（完成会自动通知）。命令非零退出返回错误文本（输出超长时完整版会保存到 evictions/ 目录并用 read_file 提示，错误信息含路径）。",
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
			"已后台启动 PID %d，日志：%s\n完成会自动通知；可继续其它任务等通知，也可用 read_file/grep 轮询日志观察进度；用 shell_command {\"kill_pid\": %d} 终止；会话结束自动清理",
			pid, logPath, pid)}, nil
	}

	// 前台模式：输出写临时日志文件（而非内存 buffer）——超时转后台时输出
	// 无缝续写到同一文件（ADR-038 扩展）；正常完成/Esc 时读回返回。
	timeout := time.Duration(p.TimeoutMS) * time.Millisecond
	if p.TimeoutMS <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	logDir := backgroundDir(rc)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "shell_command: " + err.Error()}
	}
	tmpLog := filepath.Join(logDir, fmt.Sprintf(".fg_%d.log", time.Now().UnixNano()))
	f, err := os.Create(tmpLog)
	if err != nil {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "shell_command: " + err.Error()}
	}

	cmd := newShellCmd(p.Command, p.Workdir)
	cmd.Stdout = f
	cmd.Stderr = f

	tree, stopTree, err := startForeground(ctx, cmd)
	if err != nil {
		f.Close()
		os.Remove(tmpLog)
		closeProcessTree(tree)
		if ctx.Err() == context.Canceled {
			return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "shell_command: 命令已被中断（Esc），进程树已终止"}
		}
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "shell_command: " + err.Error()}
	}
	// tree 由 defer 关闭；超时转后台分支会把 tree 置零跳过（句柄移交注册表）。
	defer func() { closeProcessTree(tree) }()

	// Start 后 ctx 已取消的竞态兜底：AfterFunc 注册晚于取消瞬间时会漏杀，
	// 这里显式杀树（只 Esc——超时交给 select 的转后台分支）。
	if ctx.Err() == context.Canceled {
		killProcessTree(tree, cmd.Process.Pid)
	}

	// Wait goroutine：Wait 返回（copy goroutine 完成 = 文件写完）后关闭
	// 日志文件再发 done——转后台后进程可能长活，f 的收尾由这里统一承担。
	// transferred 门控（审查 04，2026-08-14）：仅超时转后台分支置 true 后才
	// 按 pid 查全局注册表通知——纯前台完成路径 pid 从未进注册表，无条件查询
	// 在 pid 复用窗口可能命中"刚死未注销"的旧后台条目发错通知（理论、概率
	// 可忽略但可消除）；门控后纯前台彻底不查询，误报窗口消除。转后台场景两
	// 路仍恰好一个拿到 entry（goroutine 命中注册 / compensate 补偿），不双通知。
	var transferred atomic.Bool
	done := make(chan error, 1)
	go waitForeground(cmd, f, &transferred, done)

	select {
	case werr := <-done:
		// 进程完成（或被 Esc 杀树后 Wait 返回）：读回输出、删临时文件。
		out, _ := os.ReadFile(tmpLog)
		os.Remove(tmpLog)
		if ctx.Err() == context.Canceled {
			// 完成瞬间恰逢 Esc（杀树已执行或进程已死）：报中断语义。
			// 不调 stopTree：杀树就是 Esc 的目的。
			msg := "shell_command: 命令已被中断（Esc），进程树已终止"
			if len(out) > 0 {
				msg += "\n" + string(out)
			}
			return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: msg}
		}
		// 正常完成路径：stopTree 防 defer cancel() 触发杀树（ADR-038 决策
		// 第 2 点——否则每条前台命令返回瞬间，命令派生的后台进程
		// （`npm run dev &`）随进程组/job 一并被杀）；preserveProcessTree
		// 释放句柄关闭时的内核兜底杀树（Windows 清 KILL_ON_JOB_CLOSE，
		// codex preserve_descendants 同款；POSIX no-op）。
		stopTree()
		preserveProcessTree(tree)
		if werr != nil {
			msg := "shell_command: " + werr.Error()
			if len(out) > 0 {
				msg += "\n" + string(out)
			}
			return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: msg}
		}
		return messages.ToolResult{Success: true, Content: string(out)}, nil

	case <-ctx.Done():
		switch ctx.Err() {
		case context.Canceled:
			// Esc：AfterFunc 已杀树 → 等 Wait 返回（树死管道 EOF 必返回；
			// 5s 安全网防 job 降级 + taskkill 失败时残留）。
			select {
			case <-done:
			case <-time.After(5 * time.Second):
			}
			out, _ := os.ReadFile(tmpLog)
			os.Remove(tmpLog)
			msg := "shell_command: 命令已被中断（Esc），进程树已终止"
			if len(out) > 0 {
				msg += "\n" + string(out)
			}
			return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: msg}

		default: // DeadlineExceeded
			// 超时转后台托管（ADR-038 扩展）：不杀树——进程继续跑，日志持续
			// 写文件，句柄移交注册表；模型轮询日志、kill_pid 终止。竞态：进程
			// 恰好此时完成也注册无害（杀空 job 无副作用，二次 kill 报未找到）。
			transferred.Store(true) // 门控先置位：转后台后完成必须通知（审查 04）
			pid := cmd.Process.Pid
			logPath := transferToBackground(rc, tree, pid, tmpLog)
			stopTree() // 回调已在超时瞬间触发（DeadlineExceeded 不杀），此处 no-op 防御
			var zero processTreeHandle
			tree = zero // 句柄已移交注册表，defer 不再 close
			// 竞态窗口补偿（2026-08-13）：进程恰在超时瞬间已死时，Wait
			// goroutine 的 notify 先于上面的注册执行（no-op）——补一次
			// 注销+通知，保证"完成会自动通知"的承诺不落空。
			compensateTransferNotify(done, pid)
			return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: fmt.Sprintf(
				"shell_command: 命令运行超过 %v，已自动转入后台：PID %d，日志：%s\n完成会自动通知；可用 read_file/grep 轮询日志观察进度；用 shell_command {\"kill_pid\": %d} 终止；不要重试该命令——它可能仍在后台运行",
				timeout, pid, logPath, pid)}
		}
	}
}

// waitForeground 回收前台命令进程资源（Wait goroutine 体，审查 04 抽名，
// 2026-08-14）：Wait 返回（copy goroutine 完成 = 文件写完）→ 关日志文件 →
// 仅 transferred 时按 pid 发完成通知（纯前台路径不查注册表，pid 复用窗口
// 不误命中"刚死未注销"的旧后台条目）→ 回传退出错误。
func waitForeground(cmd *exec.Cmd, f *os.File, transferred *atomic.Bool, done chan<- error) {
	err := cmd.Wait()
	f.Close()
	if transferred.Load() {
		notifyCompletion(cmd.Process.Pid, err)
	}
	done <- err
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
