package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
)

// 进程树生命周期（ADR-038）：前台命令的 Esc/超时杀树 + background/kill_pid
// 后台进程管理 + harness 退出 pre-kill 清理。平台分派见 background_windows.go
// （Job Object）/ background_unix.go（进程组）。

// bgProcess 是一个后台 shell 进程的注册表条目。
// Handle 是平台进程树资源：Windows = job 句柄（KILL_ON_JOB_CLOSE），
// POSIX = 零值（杀树走 kill(-pid) 进程组语义）。
type bgProcess struct {
	PID    int
	Handle processTreeHandle
}

// backgroundProcesses 是进程级后台进程注册表（PID → 条目）。
// 与无状态 agent（ADR-026）兼容：ShellCommandTool 是无状态值类型（每次
// Build 重建实例），会话/回合切换不影响进程生命周期；注册表挂在工具包的
// 进程级全局，sync.Map 并发安全（并行工具调用可同时注册/注销）。
var backgroundProcesses sync.Map // int → *bgProcess

// registerBackground 登记后台进程（Start 成功后调用）。
func registerBackground(e *bgProcess) { backgroundProcesses.Store(e.PID, e) }

// unregisterBackground 注销并返回条目（kill_pid 用；不存在返回 nil）。
func unregisterBackground(pid int) *bgProcess {
	v, ok := backgroundProcesses.LoadAndDelete(pid)
	if !ok {
		return nil
	}
	return v.(*bgProcess)
}

// startForeground 启动前台命令并挂接 ctx 取消杀树。
//
// 杀树时机在 ctx 取消**瞬间**（context.AfterFunc），不依赖 Wait 返回——旧实现
// 的杀树在 cmd.Run() 返回后的超时分支里，而 Run 可能因孙进程继承管道句柄
// 永不返回（写端不关、读端无 EOF），形成"卡住 → 杀不到"的死锁。杀树成功后
// 树内全部进程消亡、管道写端随进程终止被内核关闭 → Wait 必返回。
//
// 返回进程树句柄，调用方须全路径 defer closeProcessTree(tree)：
// 成功路径关闭空 job 无害（进程已自然退出）；ctx 取消路径杀树后关闭。
// attach 失败降级（tree 置 0）：杀树走 taskkill 尽力兜底（Windows 嵌套 job
// 限制场景，ADR-038 已知边界）。
func startForeground(ctx context.Context, cmd *exec.Cmd) (processTreeHandle, error) {
	// createProcessTree 失败（如 Windows 嵌套 job 限制）→ 零值句柄：
	// attach 对零值 no-op，杀树走 taskkill 兜底（ADR-038 已知边界）。
	tree, _ := createProcessTree()
	if err := cmd.Start(); err != nil {
		closeProcessTree(tree)
		return tree, err
	}
	_ = attachProcessTree(tree, cmd.Process)
	// AfterFunc 在 Start 之后注册：回调触发时 cmd.Process.Pid 必有值。
	// Start 前 ctx 已取消的竞态由 Handle 的 ctx.Err() 检查兜底（见 shell.go）。
	pid := cmd.Process.Pid
	context.AfterFunc(ctx, func() { killProcessTree(tree, pid) })
	return tree, nil
}

// startBackground 后台启动 shell 命令：输出重定向到日志文件，立即返回 PID。
// 用 Go 直接启动（exec.Command + 文件重定向），不用 & / nohup / Start-Process
// 等 shell 语法——跨平台统一且日志重定向全控（模型手写后台命令易出语法错，
// ADR-038）。
//
// 生命周期（ADR-038）：后台进程**不绑定回合 ctx**——Esc 中断回合不杀它
// （它已不是"正在执行的工具调用"，是会话级资源），仅由 kill_pid（模型显式
// 杀，conversation 里有 PID+日志路径）与 harness 退出 CleanupBackground 终止。
func startBackground(rc *middleware.RuntimeContext, command, workdir string) (pid int, logPath string, err error) {
	dir := backgroundDir(rc)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, "", err
	}
	// 临时名先开文件：Start 前必须打开文件才能重定向，而 PID 要 Start 后才有。
	tmp := filepath.Join(dir, fmt.Sprintf(".bg_%d.log", time.Now().UnixNano()))
	f, err := os.Create(tmp)
	if err != nil {
		return 0, "", err
	}
	cmd := newShellCmd(command, workdir)
	cmd.Stdout = f
	cmd.Stderr = f

	// createProcessTree 失败 → 零值句柄（attach no-op，杀树走 taskkill 兜底）。
	tree, _ := createProcessTree()
	if err := cmd.Start(); err != nil {
		f.Close()
		closeProcessTree(tree)
		os.Remove(tmp)
		return 0, "", err
	}
	_ = attachProcessTree(tree, cmd.Process)

	pid = cmd.Process.Pid
	logPath = filepath.Join(dir, fmt.Sprintf("%d.log", pid))
	if err := os.Rename(tmp, logPath); err != nil {
		logPath = tmp // 平台差异降级保留临时名；结果显式返回实际路径，模型不猜文件名
	}
	f.Close()
	// 回收子进程资源：Wait 在进程死后等管道 EOF 即返回（goroutine 不阻塞主流程）。
	go func() { _ = cmd.Wait() }()

	registerBackground(&bgProcess{PID: pid, Handle: tree})
	return pid, logPath, nil
}

// backgroundDir 计算后台日志目录：<StatePath 所在目录>/background（仿
// EvictContent 的 evictions 模式，工具惰性建目录）。StatePath 空（非会话/
// 测试）退化 os.TempDir()。
func backgroundDir(rc *middleware.RuntimeContext) string {
	if rc != nil && rc.StatePath != "" {
		return filepath.Join(filepath.Dir(rc.StatePath), "background")
	}
	return os.TempDir()
}

// handleKill 执行 kill_pid：仅允许杀**注册表内**的后台进程——不向模型开放
// 任意 PID 击杀（防误杀系统进程，ADR-038 安全边界）。杀树 → 关句柄 → 注销。
func handleKill(pid int) (messages.ToolResult, error) {
	entry := unregisterBackground(pid)
	if entry == nil {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: fmt.Sprintf(
			"shell_command: 未找到后台进程 %d（可能已退出或未经本会话 background 启动）", pid)}
	}
	killProcessTree(entry.Handle, entry.PID)
	closeProcessTree(entry.Handle)
	return messages.ToolResult{Success: true, Content: fmt.Sprintf("已终止后台进程 %d", pid)}, nil
}

// CleanupBackground 退出前清理：杀掉全部后台 shell 进程树并释放平台资源。
// 由 cmd/harness 的 run() defer 调用（覆盖 run/resume/TUI 全部子命令）——
// background 进程生命周期 ≤ harness 进程寿命（用户拍板：不提供"退出后存活"
// 语义）。Windows 上即使本函数未执行到（SIGKILL/crash），KILL_ON_JOB_CLOSE
// 也会在句柄随进程消亡被内核关闭时兜底杀树。
func CleanupBackground() {
	backgroundProcesses.Range(func(k, _ any) bool {
		v, ok := backgroundProcesses.LoadAndDelete(k)
		if !ok || v == nil {
			return true
		}
		entry := v.(*bgProcess)
		killProcessTree(entry.Handle, entry.PID)
		closeProcessTree(entry.Handle)
		return true
	})
}
