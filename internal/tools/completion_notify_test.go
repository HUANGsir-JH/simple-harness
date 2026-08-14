package tools

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-project/harness/internal/completion"
	"github.com/agent-project/harness/internal/middleware"
)

// testCompletionRC 构造带完成事件队列的测试 rc（生产端 notifyCompletion 用）。
func testCompletionRC(t *testing.T) (*middleware.RuntimeContext, *completion.Queue) {
	t.Helper()
	rc := middleware.NewRuntimeContext()
	rc.SessionID = "sess-notify"
	rc.StatePath = filepath.Join(t.TempDir(), "sess", "agentstate.json")
	q := completion.New(filepath.Join(filepath.Dir(rc.StatePath), "completions.json"))
	rc.Completions = q
	return rc, q
}

// exitErr 构造真实非零退出错误（exec.ExitError，ExitCode 可用）。
func exitErr(code int) error {
	if isWindows() {
		return exec.Command("cmd", "/c", fmt.Sprintf("exit %d", code)).Run()
	}
	return exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
}

// waitForCompletion 轮询队列直至累计收到 want 条事件（超时 Fatal）。
func waitForCompletion(t *testing.T, q *completion.Queue, want int, timeout time.Duration) []completion.Event {
	t.Helper()
	var events []completion.Event
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		events = append(events, q.Drain()...)
		if len(events) >= want {
			return events
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("等待 %d 条完成事件超时（已收到 %d 条）", want, len(events))
	return nil
}

// TestNotifyCompletionAppendsEvent 验证 notifyCompletion：注销条目 + Append
// 完成事件（exit 0、通知全文、字段捕获）。
func TestNotifyCompletionAppendsEvent(t *testing.T) {
	rc, q := testCompletionRC(t)
	registerBackground(&bgProcess{PID: 4242, queue: q, sessionID: rc.SessionID, logPath: "/tmp/x.log"})
	notifyCompletion(4242, nil)
	if _, ok := backgroundProcesses.Load(4242); ok {
		t.Error("notify 后注册表条目应注销")
	}
	events := q.Drain()
	if len(events) != 1 {
		t.Fatalf("应 1 条事件，got %d", len(events))
	}
	ev := events[0]
	if ev.ToolName != "shell_command" || ev.SessionID != rc.SessionID {
		t.Errorf("字段捕获不符: %+v", ev)
	}
	if ev.ExitCode == nil || *ev.ExitCode != 0 {
		t.Errorf("exit 应为 0: %+v", ev)
	}
	for _, want := range []string{"4242", "exit 0", "/tmp/x.log", "read_file"} {
		if !strings.Contains(ev.Result, want) {
			t.Errorf("Result 应含 %q: %q", want, ev.Result)
		}
	}
}

// TestNotifyCompletionNonZeroExit 验证非零退出码（exec.ExitError.ExitCode）。
func TestNotifyCompletionNonZeroExit(t *testing.T) {
	rc, q := testCompletionRC(t)
	registerBackground(&bgProcess{PID: 43, queue: q, sessionID: rc.SessionID, logPath: "x"})
	notifyCompletion(43, exitErr(3))
	events := q.Drain()
	if len(events) != 1 {
		t.Fatalf("应 1 条事件，got %d", len(events))
	}
	if events[0].ExitCode == nil || *events[0].ExitCode != 3 {
		t.Errorf("exit 应为 3: %+v", events[0])
	}
	if !strings.Contains(events[0].Result, "exit 3") {
		t.Errorf("Result: %q", events[0].Result)
	}
}

// TestNotifyCompletionNoop 验证 no-op 场景：未注册 pid、条目无队列（非会话）。
func TestNotifyCompletionNoop(t *testing.T) {
	_, q := testCompletionRC(t)
	notifyCompletion(9999, nil)             // 未注册 → no-op
	registerBackground(&bgProcess{PID: 44}) // queue nil（非会话启动）
	notifyCompletion(44, nil)
	if _, ok := backgroundProcesses.Load(44); ok {
		t.Error("queue nil 时也应注销条目（自然退出注销语义不变）")
	}
	if got := q.Drain(); got != nil {
		t.Errorf("不应有事件: %+v", got)
	}
}

// TestCompensateTransferNotify 验证超时转后台竞态窗口补偿（2026-08-13）：
// done 已有结果 → 补注销+通知；done 空 → 不动（goroutine 之后自会 notify）。
func TestCompensateTransferNotify(t *testing.T) {
	rc, q := testCompletionRC(t)
	// 分支 1：进程死在超时瞬间、goroutine 的 notify 已先于注册执行（no-op）。
	registerBackground(&bgProcess{PID: 51, queue: q, sessionID: rc.SessionID, logPath: "a"})
	done := make(chan error, 1)
	done <- errors.New("sentinel")
	compensateTransferNotify(done, 51)
	if got := q.Drain(); len(got) != 1 || !strings.Contains(got[0].Result, "51") {
		t.Fatalf("补偿应补发通知: %+v", got)
	}
	// 分支 2：done 空 = goroutine 仍在等 → 不通知、条目保留。
	registerBackground(&bgProcess{PID: 52, queue: q, sessionID: rc.SessionID, logPath: "b"})
	compensateTransferNotify(make(chan error, 1), 52)
	if got := q.Drain(); got != nil {
		t.Errorf("done 空不应通知: %+v", got)
	}
	if _, ok := backgroundProcesses.Load(52); !ok {
		t.Error("done 空时条目应保留（进程死后 goroutine 自会 notify）")
	}
	notifyCompletion(52, nil) // 清理条目
}

// TestShellCommandBackgroundNaturalExitNotifies 验证 background 进程自然退出 →
// 完成事件入队（Wait goroutine 生产端，2026-08-13）。
func TestShellCommandBackgroundNaturalExitNotifies(t *testing.T) {
	rc, q := testCompletionRC(t)
	t.Cleanup(CleanupBackground)
	r, err := callWithRC(ShellCommandTool{}, rc, map[string]any{
		"command": "echo done", "background": true,
	})
	if err != nil || !r.Success {
		t.Fatalf("background: %v %v", r, err)
	}
	events := waitForCompletion(t, q, 1, 10*time.Second)
	if len(events) != 1 {
		t.Fatalf("应恰好 1 条完成事件，got %d", len(events))
	}
	if !strings.Contains(events[0].Result, "exit 0") {
		t.Errorf("自然退出 exit 应为 0: %q", events[0].Result)
	}
}

// TestShellCommandKillBackgroundNoNotify 验证 kill_pid 杀进程 → 不发通知
// （模型已知，kill 结果就是反馈）。
func TestShellCommandKillBackgroundNoNotify(t *testing.T) {
	rc, q := testCompletionRC(t)
	t.Cleanup(CleanupBackground)
	r, err := callWithRC(ShellCommandTool{}, rc, map[string]any{
		"command": bgSleep(60), "background": true,
	})
	if err != nil || !r.Success {
		t.Fatalf("background: %v %v", r, err)
	}
	var pid int
	if _, err := fmt.Sscanf(r.Content, "已后台启动 PID %d", &pid); err != nil || pid <= 0 {
		t.Fatalf("提取 PID: %q", r.Content)
	}
	if _, err := callWithRC(ShellCommandTool{}, rc, map[string]any{"kill_pid": pid}); err != nil {
		t.Fatalf("kill_pid: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	if got := q.Drain(); got != nil {
		t.Errorf("kill_pid 杀进程不应发通知: %+v", got)
	}
}

// TestShellCommandForegroundNoNotify 验证前台正常完成 → 不发通知（pid 从未
// 进注册表，no-op 天然正确）。
func TestShellCommandForegroundNoNotify(t *testing.T) {
	rc, q := testCompletionRC(t)
	t.Cleanup(CleanupBackground)
	if _, err := callWithRC(ShellCommandTool{}, rc, map[string]any{"command": "echo hi"}); err != nil {
		t.Fatalf("前台: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if got := q.Drain(); got != nil {
		t.Errorf("前台完成不应发通知: %+v", got)
	}
}

// TestShellCommandTimeoutTransferNotifies 验证超时转后台 → 进程自然死 →
// 完成事件入队。
func TestShellCommandTimeoutTransferNotifies(t *testing.T) {
	rc, q := testCompletionRC(t)
	t.Cleanup(CleanupBackground)
	_, err := callWithRC(ShellCommandTool{}, rc, map[string]any{
		"command": bgSleep(1), "timeout_ms": 300,
	})
	wantRespondToModel(t, err, "timeout transfer")
	msg := err.Error()
	if !strings.Contains(msg, "已自动转入后台") {
		t.Fatalf("应转后台: %s", msg)
	}
	events := waitForCompletion(t, q, 1, 5*time.Second)
	if len(events) != 1 {
		t.Fatalf("应恰好 1 条完成事件，got %d", len(events))
	}
	if !strings.Contains(events[0].Result, "exit 0") {
		t.Errorf("转后台进程自然退出 exit 应为 0: %q", events[0].Result)
	}
}

// TestShellCommandTimeoutRaceCompensation 回归锚点（2026-08-13）：进程恰在
// 超时瞬间退出（timeout 1ms + exit 0）——无论 Wait goroutine 的 notify 与
// transferToBackground 谁先执行，都必须恰好收到 1 条完成事件（补偿路径
// compensateTransferNotify 兜住"通知永久丢失"的竞态窗口）。
func TestShellCommandTimeoutRaceCompensation(t *testing.T) {
	rc, q := testCompletionRC(t)
	t.Cleanup(CleanupBackground)
	_, err := callWithRC(ShellCommandTool{}, rc, map[string]any{
		"command": "exit 0", "timeout_ms": 1,
	})
	if err == nil {
		t.Skip("进程在超时前完成（前台路径，不承诺通知）")
	}
	wantRespondToModel(t, err, "timeout")
	events := waitForCompletion(t, q, 1, 5*time.Second)
	if len(events) != 1 {
		t.Fatalf("应恰好 1 条完成事件，got %d", len(events))
	}
	if events[0].ExitCode == nil || *events[0].ExitCode != 0 {
		t.Errorf("exit 应为 0: %+v", events[0])
	}
	// 不双通知：再等 500ms 应无第二条。
	time.Sleep(500 * time.Millisecond)
	if extra := q.Drain(); len(extra) != 0 {
		t.Errorf("不应有第二条通知: %+v", extra)
	}
}
