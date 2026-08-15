package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
)

// testRCWithStatePath 构造带会话 StatePath 的测试 rc（background 日志目录推导）。
func testRCWithStatePath(t *testing.T) *middleware.RuntimeContext {
	t.Helper()
	rc := middleware.NewRuntimeContext()
	rc.StatePath = filepath.Join(t.TempDir(), "sess", "agentstate.json")
	return rc
}

// callWithCtx 用指定 ctx 执行一次 shell 调用（Esc 中断测试用）。
func callWithCtx(ctx context.Context, args map[string]any) (messages.ToolResult, error) {
	b, err := json.Marshal(args)
	if err != nil {
		panic(err)
	}
	return ShellCommandTool{}.Handle(ctx, middleware.NewRuntimeContext(), "c1", b)
}

// isProcessAlive 平台判定进程存活（实现分派到 build-tag 测试文件：
// processAliveWindows / processAliveUnix）。

// bgSleep 返回一个后台睡眠命令（跨平台）。
func bgSleep(seconds int) string {
	if isWindows() {
		return fmt.Sprintf("Start-Sleep -Seconds %d", seconds)
	}
	return fmt.Sprintf("sleep %d", seconds)
}

// TestShellCommandEmptyArgsError 验证 command 与 kill_pid 都空时报参数错误
// （ADR-038：command 从 required 改为可选后必须显式校验）。
func TestShellCommandEmptyArgsError(t *testing.T) {
	_, err := call(ShellCommandTool{}, map[string]any{"command": ""})
	wantRespondToModel(t, err, "empty args")
	if !strings.Contains(err.Error(), "至少提供一个") {
		t.Errorf("错误应提示 command 与 kill_pid 至少一个: %s", err)
	}
}

// TestShellCommandKillUnknownPID 验证 kill 未注册 PID 报错（仅注册表内 PID，
// 防误杀系统进程，ADR-038 安全边界）。
func TestShellCommandKillUnknownPID(t *testing.T) {
	_, err := call(ShellCommandTool{}, map[string]any{"kill_pid": 999999})
	wantRespondToModel(t, err, "kill unknown")
	if !strings.Contains(err.Error(), "未找到后台进程") {
		t.Errorf("错误应提示未找到: %s", err)
	}
}

// logPathFrom 从 background 结果文本提取日志路径（"日志：" 在行中间，
// 用 Index 定位而非 TrimPrefix）。
func logPathFrom(t *testing.T, content string) string {
	t.Helper()
	for _, line := range strings.Split(content, "\n") {
		if i := strings.Index(line, "日志："); i >= 0 {
			return strings.TrimSpace(line[i+len("日志："):])
		}
	}
	t.Fatalf("Content 无日志路径: %q", content)
	return ""
}

// TestShellCommandBackgroundReturnsPIDAndLog 验证 background 立即返回（不等命令
// 结束）+ PID + 日志路径，且日志文件在会话 background/ 目录下。
func TestShellCommandBackgroundReturnsPIDAndLog(t *testing.T) {
	rc := testRCWithStatePath(t)
	t.Cleanup(CleanupBackground)
	start := time.Now()
	r, err := callWithRC(ShellCommandTool{}, rc, map[string]any{
		"command": bgSleep(5), "background": true,
	})
	if err != nil || !r.Success {
		t.Fatalf("background: %v %v", r, err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("background 应立即返回（不等待命令结束）")
	}
	if !strings.Contains(r.Content, "已后台启动 PID") {
		t.Errorf("Content 应含 PID: %q", r.Content)
	}
	logPath := logPathFrom(t, r.Content)
	if !strings.Contains(logPath, filepath.Join("sess", "background")) {
		t.Errorf("日志应在会话 background/ 目录: %q", logPath)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Errorf("日志文件应存在: %v", err)
	}
}

// TestShellCommandBackgroundWritesLog 验证 background 输出写入日志文件
// （模型用 read_file/grep 轮询的契约）。
func TestShellCommandBackgroundWritesLog(t *testing.T) {
	rc := testRCWithStatePath(t)
	t.Cleanup(CleanupBackground)
	r, err := callWithRC(ShellCommandTool{}, rc, map[string]any{
		"command": "echo bg-marker-42", "background": true,
	})
	if err != nil || !r.Success {
		t.Fatalf("background: %v %v", r, err)
	}
	logPath := logPathFrom(t, r.Content)
	// 轮询日志（PowerShell 冷启动 ~1s，deadline 5s 足够）。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(logPath); err == nil && strings.Contains(string(data), "bg-marker-42") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("日志应含 bg-marker-42")
}

// TestShellCommandKillBackground 验证 kill_pid 终止后台进程（平台断言进程死），
// 二次 kill 报"未找到"（注册表已注销）。
func TestShellCommandKillBackground(t *testing.T) {
	rc := testRCWithStatePath(t)
	t.Cleanup(CleanupBackground)
	r, err := callWithRC(ShellCommandTool{}, rc, map[string]any{
		"command": bgSleep(60), "background": true,
	})
	if err != nil || !r.Success {
		t.Fatalf("background: %v %v", r, err)
	}
	var pid int
	if _, err := fmt.Sscanf(r.Content, "已后台启动 PID %d", &pid); err != nil || pid <= 0 {
		t.Fatalf("无法从 Content 提取 PID: %q err=%v", r.Content, err)
	}

	r2, err := callWithRC(ShellCommandTool{}, rc, map[string]any{"kill_pid": pid})
	if err != nil || !r2.Success {
		t.Fatalf("kill_pid: %v %v", r2, err)
	}
	if !strings.Contains(r2.Content, "已终止后台进程") {
		t.Errorf("kill 结果: %q", r2.Content)
	}
	// 杀树是同步内核操作（job terminate / kill 信号），进程应已死。
	// 用 waitForProcessDead 轮询：瞬时 kill -0 会撞上"僵尸未回收"窗口
	// （审查报告 03，POSIX 假阳性）。
	if !waitForProcessDead(pid, 2*time.Second) {
		t.Errorf("kill_pid 后进程 %d 仍存活", pid)
	}
	// 二次 kill：注册表已注销。
	_, err = callWithRC(ShellCommandTool{}, rc, map[string]any{"kill_pid": pid})
	wantRespondToModel(t, err, "kill twice")
	if !strings.Contains(err.Error(), "未找到后台进程") {
		t.Errorf("二次 kill 应报未找到: %s", err)
	}
}

// TestCleanupBackgroundKillsAll 验证退出清理杀光全部后台进程（用户补充需求：
// harness 退出 pre-kill 的清理函数本体）。
func TestCleanupBackgroundKillsAll(t *testing.T) {
	rc := testRCWithStatePath(t)
	var pids []int
	for i := 0; i < 2; i++ {
		r, err := callWithRC(ShellCommandTool{}, rc, map[string]any{
			"command": bgSleep(60), "background": true,
		})
		if err != nil || !r.Success {
			t.Fatalf("background[%d]: %v %v", i, r, err)
		}
		var pid int
		if _, err := fmt.Sscanf(r.Content, "已后台启动 PID %d", &pid); err != nil || pid <= 0 {
			t.Fatalf("提取 PID 失败[%d]: %q", i, r.Content)
		}
		pids = append(pids, pid)
	}
	CleanupBackground()
	for _, pid := range pids {
		if !waitForProcessDead(pid, 2*time.Second) {
			t.Errorf("CleanupBackground 后进程 %d 仍存活", pid)
		}
	}
}

// TestShellCommandTimeoutKeepsProcessAlive 验证超时转后台后进程**仍存活**
// （ADR-038 扩展：超时不杀树），且 kill_pid 可终止（移交注册表生效）。
func TestShellCommandTimeoutKeepsProcessAlive(t *testing.T) {
	rc := testRCWithStatePath(t)
	t.Cleanup(CleanupBackground)
	_, err := callWithRC(ShellCommandTool{}, rc, map[string]any{
		"command": bgSleep(30), "timeout_ms": 300,
	})
	wantRespondToModel(t, err, "timeout keep alive")
	msg := err.Error()
	if !strings.Contains(msg, "已自动转入后台") {
		t.Fatalf("应提示转后台: %s", msg)
	}
	var pid int
	if _, scanErr := fmt.Sscanf(msg, "shell_command: 命令运行超过 300ms，已自动转入后台：PID %d", &pid); scanErr != nil || pid <= 0 {
		t.Fatalf("无法从消息提取 PID: %q", msg)
	}
	// 转后台后进程应存活（超时不杀树）。
	if !isProcessAlive(pid) {
		t.Errorf("超时转后台后进程 %d 应存活", pid)
	}
	// kill_pid 走注册表可杀（句柄已移交）。
	r, err := callWithRC(ShellCommandTool{}, rc, map[string]any{"kill_pid": pid})
	if err != nil || !r.Success {
		t.Fatalf("kill_pid: %v %v", r, err)
	}
	if !waitForProcessDead(pid, 2*time.Second) {
		t.Errorf("kill_pid 后进程 %d 应死亡", pid)
	}
}

// TestShellCommandEscInterrupt 验证 Esc（ctx 取消）返回"命令已被中断"提示
// （ADR-038：Esc 杀树 + 回填语义，模型知道进程树已终止）。
func TestShellCommandEscInterrupt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	_, err := callWithCtx(ctx, map[string]any{"command": bgSleep(30)})
	wantRespondToModel(t, err, "esc interrupt")
	if !strings.Contains(err.Error(), "命令已被中断") {
		t.Errorf("Esc 应回填中断提示: %s", err)
	}
}

// TestShellCommandFgSpawnedChildSurvives 回归锚点（审查报告 01）：前台命令
// 派生后台进程后正常退出——派生进程必须存活（终端式语义）。修复前两条路径
// 都会误杀：POSIX = defer cancel() 触发 AfterFunc 杀进程组；Windows = job
// 句柄关闭 KILL_ON_JOB_CLOSE 杀全树。等待 500ms 再断言：杀树回调是异步的，
// 瞬时检查会放过竞态窗口（误判通过）。
func TestShellCommandFgSpawnedChildSurvives(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	r, err := call(ShellCommandTool{}, map[string]any{
		"command": fgSpawnChildCommand(pidFile),
	})
	if err != nil || !r.Success {
		t.Fatalf("前台命令: %v %v", r, err)
	}
	child := readSpawnedPID(t, pidFile)
	time.Sleep(500 * time.Millisecond)
	if !isProcessAlive(child) {
		t.Errorf("命令派生的后台进程 %d 被杀（正常退出误杀回归）", child)
	}
	// 清理：派生进程不被注册表追踪（01 语义，与终端一致），直接杀。
	killPidDirect(child)
	if !waitForProcessDead(child, 3*time.Second) {
		t.Errorf("清理失败：派生进程 %d 未死", child)
	}
}

// TestShellCommandBackgroundConcurrentUniqueLogs 回归锚点（后台日志分配竞态，
// 2026-08-14）：并发 background 启动的临时日志名曾用 time.Now().UnixNano()
// 合成——本机实测同刻并发调用 8 个里 7~8 个返回相同值（墙上时钟 tick 粒度粗，
// 连续调用 93% 相同），两个子进程 os.Create 共享同一 inode：输出在相同偏移
// 互相覆盖（等长行整体丢失），先 rename 者胜、后 rename 者 ENOENT 降级保留已
// 不存在的 .bg 路径（通知路径不可读）。修复 = os.CreateTemp（随机名 + O_EXCL，
// 撞名必然换新文件）。本测试直接并发调 startBackground 多轮（屏障最大化碰撞
// 窗口，旧实现多轮必败），断言：路径全部唯一且为 <pid>.log、文件存在、内容
// 仅含本任务输出、目录无 .bg 残留。
func TestShellCommandBackgroundConcurrentUniqueLogs(t *testing.T) {
	rc := testRCWithStatePath(t)
	t.Cleanup(CleanupBackground)
	logDir := filepath.Join(filepath.Dir(rc.StatePath), "background")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}

	const rounds, n = 12, 8
	type entry struct {
		pid     int
		logPath string
		err     error
	}
	all := make([][]entry, rounds)
	for round := 0; round < rounds; round++ {
		results := make([]entry, n)
		var wg sync.WaitGroup
		start := make(chan struct{}) // 屏障：同刻放行全部 goroutine
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				marker := fmt.Sprintf("BG-CONC-%02d-%02d", round, i)
				pid, logPath, err := startBackground(rc,
					fmt.Sprintf("echo %s-START; %s; echo %s-END", marker, bgSleep(1), marker), "")
				results[i].pid, results[i].logPath, results[i].err = pid, logPath, err
			}(i)
		}
		close(start)
		wg.Wait()

		seen := map[string]int{}
		for i, res := range results {
			if res.err != nil {
				t.Fatalf("round %d task %d: %v", round, i, res.err)
			}
			if filepath.Dir(res.logPath) != logDir {
				t.Errorf("round %d task %d: 日志路径不在 background 目录: %q", round, i, res.logPath)
			}
			if seen[res.logPath]++; seen[res.logPath] > 1 {
				t.Errorf("round %d: 同一路径被多任务共用: %q", round, res.logPath)
			}
		}
		all[round] = results
	}

	// 等全部进程自然退出（Wait goroutine 注销 + 输出写盘完成）。
	for _, results := range all {
		for _, res := range results {
			if !waitForProcessDead(res.pid, 15*time.Second) {
				t.Fatalf("pid %d 未在期限内退出", res.pid)
			}
		}
	}
	// 内容隔离断言：每份日志必须完整含本任务 START/END、不得含他人标记。
	for round, results := range all {
		for i, res := range results {
			data, err := os.ReadFile(res.logPath)
			if err != nil {
				t.Errorf("round %d task %d: 日志不可读（通知路径失效）: %v", round, i, err)
				continue
			}
			for r2 := 0; r2 < rounds; r2++ {
				for j := 0; j < n; j++ {
					marker := fmt.Sprintf("BG-CONC-%02d-%02d", r2, j)
					if r2 == round && j == i {
						if !strings.Contains(string(data), marker+"-START") || !strings.Contains(string(data), marker+"-END") {
							t.Errorf("round %d task %d: 日志缺本任务输出 %q: %q", round, i, marker, string(data))
						}
					} else if strings.Contains(string(data), marker) {
						t.Errorf("round %d task %d: 日志混入 %q 输出（串扰）: %q", round, i, marker, string(data))
					}
				}
			}
		}
	}
	// Rename 在 Windows 上可能因子进程仍持有 stdout 句柄而降级保留 .bg 临时名。
	// 降级路径是工具结果中明确返回的有效日志，而不是残留；只报未被任务返回的孤儿文件。
	known := make(map[string]struct{}, rounds*n)
	for _, results := range all {
		for _, res := range results {
			known[filepath.Clean(res.logPath)] = struct{}{}
		}
	}
	entries, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		path := filepath.Join(logDir, e.Name())
		if strings.HasPrefix(e.Name(), ".bg_") {
			if _, ok := known[filepath.Clean(path)]; !ok {
				t.Errorf("目录残留孤儿 .bg 临时文件: %s", e.Name())
			}
		}
	}
}

// TestShellCommandBackgroundAutoUnregister 回归锚点（审查报告 02）：background
// 进程自然退出后注册表条目自动注销——kill_pid 应报"未找到"。残留条目在
// POSIX 上会因 PID 复用让 kill_pid 通过"仅注册表内 PID"检查后误杀无关进程组
// （安全边界失效）。
func TestShellCommandBackgroundAutoUnregister(t *testing.T) {
	rc := testRCWithStatePath(t)
	t.Cleanup(CleanupBackground)
	r, err := callWithRC(ShellCommandTool{}, rc, map[string]any{
		"command": "echo done", "background": true,
	})
	if err != nil || !r.Success {
		t.Fatalf("background: %v %v", r, err)
	}
	var pid int
	if _, err := fmt.Sscanf(r.Content, "已后台启动 PID %d", &pid); err != nil || pid <= 0 {
		t.Fatalf("提取 PID 失败: %q", r.Content)
	}
	if !waitForProcessDead(pid, 10*time.Second) {
		t.Fatalf("进程 %d 未在期限内退出", pid)
	}
	// Wait goroutine 注销晚于进程死亡：轮询注册表直至条目消失。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := backgroundProcesses.Load(pid); !ok {
			_, err = callWithRC(ShellCommandTool{}, rc, map[string]any{"kill_pid": pid})
			wantRespondToModel(t, err, "auto unregister")
			if !strings.Contains(err.Error(), "未找到后台进程") {
				t.Errorf("自然退出后 kill_pid 应报未找到: %s", err)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("进程自然退出后注册表条目未自动注销（残留）")
}
