package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agent-project/harness/internal/completion"
	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
)

// 进程树生命周期（ADR-038）：前台命令的 Esc/超时杀树 + background/kill_pid
// 后台进程管理 + harness 退出 pre-kill 清理。平台分派见 background_windows.go
// （Job Object）/ background_unix.go（进程组）。

// bgProcess 是一个后台 shell 进程的注册表条目。
// Handle 是平台进程树资源：Windows = job 句柄（KILL_ON_JOB_CLOSE），
// POSIX = 零值（杀树走 kill(-pid) 进程组语义）。
// queue/sessionID/logPath 是完成通知字段（2026-08-13）：启动时从 rc 捕获，
// 进程自然退出时 Wait goroutine 拼通知全文 Append 进队列；queue nil =
// 非会话/测试（进程照常管理，只是完成不通知）。
type bgProcess struct {
	PID    int
	Handle processTreeHandle

	queue     *completion.Queue
	sessionID string
	logPath   string

	// 完成信号（wait_task 轮询用，2026-08-16）：进程自然退出（notifyCompletion）
	// 或 kill_pid/退出清理杀树（handleKill/CleanupBackground）时 close(done)；
	// exitCode 在 close 之前写入（channel close 的 happens-before 保证 wait_task
	// 读安全，无需原子）。
	done     chan struct{}
	exitCode int
}

// markDone 置完成信号（close(done) + 先写 exitCode）。done 为 nil（旧测试直接
// 构造条目）时跳过——wait_task 按"未找到/超时"处理，无崩溃。退出缓存同时
// 记录（logPath 非空时）——wait_task 对已注销的合法 PID 仍可取退出结果
// （2026-08-16 修复 P2）。
func markDone(e *bgProcess, code int) {
	e.exitCode = code
	if e.done != nil {
		close(e.done)
	}
	if e.logPath != "" {
		rememberBGExit(e.PID, code, e.logPath)
	}
}

// backgroundProcesses 是进程级后台进程注册表（PID → 条目）。
// 与无状态 agent（ADR-026）兼容：ShellCommandTool 是无状态值类型（每次
// Build 重建实例），会话/回合切换不影响进程生命周期；注册表挂在工具包的
// 进程级全局，sync.Map 并发安全（并行工具调用可同时注册/注销）。
var backgroundProcesses sync.Map // int → *bgProcess

// bgExitInfo 是已退出后台进程的完成记录（wait_task 兜底查询，2026-08-16 修复
// P2：进程快速退出后注册表条目已注销，wait_task 仍能查到合法 PID 的退出结果）。
type bgExitInfo struct {
	exitCode int
	logPath  string
}

// bgExitCache 是已退出后台进程缓存（pid → bgExitInfo）。进程级，生命周期 =
// harness 进程；markDone 统一写入（notifyCompletion 自然退出 / kill_pid /
// CleanupBackground 杀树）。wait_task 注册表未命中时查它——本会话启动过且已
// 退出的 PID 直接返回退出码 + 日志尾部；从未见过的 pid 才报"未找到"。
var bgExitCache sync.Map // int → bgExitInfo

// bgExitCount 是缓存条目计数（粗粒度上限控制：后台进程量小，超限整体清空，
// 最旧记录丢失可接受——wait_task 对极旧 pid 退化为"未找到"）。
var bgExitCount atomic.Int32

// bgExitCacheLimit 是退出缓存上限。
const bgExitCacheLimit = 128

// rememberBGExit 记录已退出后台进程（markDone 统一调用）。并发安全：
// 超限清空与 Store 竞态最多丢最近的写入，无正确性影响。
func rememberBGExit(pid, code int, logPath string) {
	if bgExitCount.Add(1) > bgExitCacheLimit {
		bgExitCache.Range(func(k, _ any) bool {
			bgExitCache.Delete(k)
			return true
		})
		bgExitCount.Store(1)
	}
	bgExitCache.Store(pid, bgExitInfo{exitCode: code, logPath: logPath})
}

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
// 返回进程树句柄 + AfterFunc 的 stop 函数，调用方须全路径 defer
// closeProcessTree(tree)：完成/超时分支须先 stop()（防 defer cancel() 触发
// 杀树——ADR-038 决策第 2 点"成功路径 stop() 防 PID 复用误杀"：回调若在进程
// 退出后才触发，pid 可能已被系统复用给无关进程），Esc 分支不 stop（杀树就是
// 目的）。Start 失败返回零值句柄（避免调用方错误分支二次 close——Windows 上
// 双重 CloseHandle 可能关闭被复用句柄值）。attach 失败降级（tree 置 0）：
// 杀树走 taskkill 尽力兜底（Windows 嵌套 job 限制场景，ADR-038 已知边界）。
func startForeground(ctx context.Context, cmd *exec.Cmd) (processTreeHandle, func() bool, error) {
	// createProcessTree 失败（如 Windows 嵌套 job 限制）→ 零值句柄：
	// attach 对零值 no-op，杀树走 taskkill 兜底（ADR-038 已知边界）。
	tree, _ := createProcessTree()
	if err := cmd.Start(); err != nil {
		closeProcessTree(tree)
		var zero processTreeHandle
		return zero, nil, err
	}
	if err := attachProcessTree(tree, cmd.Process); err != nil {
		// attach 失败降级：句柄已无效，置零让杀树走 taskkill 兜底
		// （ADR-038 决策第 1 点"Assign 失败 → 句柄记 0"）。
		closeProcessTree(tree)
		var zero processTreeHandle
		tree = zero
	}
	// AfterFunc 在 Start 之后注册：回调触发时 cmd.Process.Pid 必有值。
	// **只杀 Esc（Canceled）**：超时（DeadlineExceeded）不杀——超时语义是
	// 转后台托管（ADR-038 扩展），由 Handle 的 select 超时分支移交注册表；
	// Esc 是用户主动说"停"，杀树。Start 前 ctx 已取消的竞态由 Handle 的
	// ctx.Err() 检查兜底（见 shell.go）。
	pid := cmd.Process.Pid
	stop := context.AfterFunc(ctx, func() {
		if ctx.Err() == context.Canceled {
			killProcessTree(tree, pid)
		}
	})
	return tree, stop, nil
}

// transferToBackground 把前台命令转后台托管（超时转后台，ADR-038 扩展）：
// 日志临时文件 rename 为 <pid>.log（失败保留临时名——tmpLog 经 os.CreateTemp
// 唯一创建，降级路径必然存在；结果显式返回实际路径），
// 进程树句柄移交注册表——之后该进程树与前台回合彻底解耦，仅由 kill_pid /
// 退出清理 / KILL_ON_JOB_CLOSE 三条路径终止（Esc 不再影响它）。
// 调用方须负责 go cmd.Wait 回收进程资源（Wait goroutine 在进程死后关闭
// 日志文件），并避免再 closeProcessTree 该句柄（已移交）。
func transferToBackground(rc *middleware.RuntimeContext, tree processTreeHandle, pid int, tmpLog string) string {
	logPath := filepath.Join(backgroundDir(rc), fmt.Sprintf("%d.log", pid))
	if err := os.Rename(tmpLog, logPath); err != nil {
		logPath = tmpLog // 平台差异降级保留临时名
	}
	entry := &bgProcess{PID: pid, Handle: tree, done: make(chan struct{})}
	captureCompletion(entry, rc, logPath)
	registerBackground(entry)
	return logPath
}

// captureCompletion 从 rc 捕获完成通知字段存进条目（2026-08-13）。
// rc.Completions nil = 非会话/测试，跳过（进程照常管理，完成不通知）。
func captureCompletion(e *bgProcess, rc *middleware.RuntimeContext, logPath string) {
	if rc == nil || rc.Completions == nil {
		return
	}
	e.queue = rc.Completions
	e.sessionID = rc.SessionID
	e.logPath = logPath
}

// notifyCompletion 是后台进程自然退出时的完成通知（通用 async 通道，
// 2026-08-13）：注销注册表条目；条目存在且带队列 → 拼通知全文 Append 进完成
// 事件队列（只写队列、不碰 conversation，避开主循环 data race）。
// no-op 场景：kill_pid/CleanupBackground 已注销（模型已知/退出中）、前台
// 正常完成（pid 从未进注册表）。退出码：exec.ExitError.ExitCode()，signal
// 杀 = -1。
func notifyCompletion(pid int, exitErr error) {
	entry := unregisterBackground(pid)
	if entry == nil {
		return
	}
	code := 0
	if exitErr != nil {
		code = -1
		var ee *exec.ExitError
		if errors.As(exitErr, &ee) {
			code = ee.ExitCode()
		}
	}
	markDone(entry, code)
	if entry.queue == nil {
		return
	}
	result := fmt.Sprintf(
		"（系统通知：后台进程 %d 已退出（exit %d）。日志：%s，可用 read_file 查看输出）",
		pid, code, entry.logPath)
	entry.queue.Append(completion.Event{
		ToolName:  ShellCommandTool{}.Name(),
		Result:    result,
		ExitCode:  &code,
		DoneAt:    time.Now().UTC().Format(time.RFC3339),
		SessionID: entry.sessionID,
	})
}

// compensateTransferNotify 是超时转后台竞态窗口的补偿（2026-08-13）：进程恰
// 在超时瞬间已死时，前台 Wait goroutine 的 notify 先于 transferToBackground
// 注册执行（no-op）——此后无人再通知、且死条目残留注册表。对 done 做一次
// **非阻塞 receive**：已有结果 → 补注销 + 通知；无结果 → goroutine 仍在等、
// 进程死后自会通知。两路恰好一个拿到 entry（unregister 只返回一次非 nil），
// 天然不会双通知。
func compensateTransferNotify(done chan error, pid int) {
	select {
	case werr := <-done:
		notifyCompletion(pid, werr)
	default:
	}
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
	// 用 os.CreateTemp 生成唯一临时名（随机后缀 + O_EXCL，撞名必然换新文件）：
	// 曾用 time.Now().UnixNano() 合成，并发 tool call（agent 并行 goroutine）同刻
	// 调用会返回相同值（本机实测 8 并发 7~8 个相同，墙上时钟 tick 粒度粗），两个
	// 子进程共享同一 inode——输出同偏移互覆/整体丢失，且先 rename 者胜、后 rename
	// 者 ENOENT 降级到已消失的 .bg 路径（后台日志分配竞态，2026-08-14）。
	f, err := os.CreateTemp(dir, ".bg_*.log")
	if err != nil {
		return 0, "", err
	}
	tmp := f.Name()
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
	if err := attachProcessTree(tree, cmd.Process); err != nil {
		// attach 失败降级：句柄置零走 taskkill 兜底（同 startForeground）。
		closeProcessTree(tree)
		var zero processTreeHandle
		tree = zero
	}

	pid = cmd.Process.Pid
	logPath = filepath.Join(dir, fmt.Sprintf("%d.log", pid))
	if err := os.Rename(tmp, logPath); err != nil {
		// 平台差异降级保留临时名；结果显式返回实际路径，模型不猜文件名。
		// tmp 由 O_EXCL 唯一创建、他人不可见也不可改：降级后的通知路径必然
		// 存在（与旧实现的竞态降级不同——旧实现失败恰因对方已把共享文件
		// rename 走，.bg 路径当时已不存在）。
		logPath = tmp
	}
	f.Close()
	entry := &bgProcess{PID: pid, Handle: tree, done: make(chan struct{})}
	captureCompletion(entry, rc, logPath)
	// 先注册再起回收 goroutine（顺序铁定：条目存在期间进程在跑；先起 goroutine
	// 则进程极快退出时注销可能先于注册执行，死进程条目残留回旧行为）。
	registerBackground(entry)
	// 回收子进程资源：Wait 在进程死后等管道 EOF 即返回（goroutine 不阻塞主流程）。
	// 进程自然退出后经 notifyCompletion 注销注册表条目（残留条目在 POSIX 上会
	// 因 PID 复用让 kill_pid 通过"仅注册表内 PID"检查后误杀无关进程组——安全
	// 边界失效），并 Append 完成事件（通用 async 通道，2026-08-13）。
	go func() { err := cmd.Wait(); notifyCompletion(pid, err) }()
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
	markDone(entry, -1)
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
		markDone(entry, -1)
		return true
	})
}
