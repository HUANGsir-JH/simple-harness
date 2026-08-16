package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agent-project/harness/internal/middleware"
)

// 注册一个假后台进程（直接操作包级注册表；done/exitCode 手工设置）。
func registerFakeBG(t *testing.T, pid int, logPath string) *bgProcess {
	t.Helper()
	e := &bgProcess{PID: pid, logPath: logPath, done: make(chan struct{})}
	registerBackground(e)
	t.Cleanup(func() { unregisterBackground(pid) })
	return e
}

func waitTaskHandle(t *testing.T, args waitTaskArgs) (resultMsg string, toolErr *ToolError) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	res, err := (WaitTaskTool{}).Handle(context.Background(), middleware.NewRuntimeContext(), "c1", raw)
	if err != nil {
		te, ok := err.(*ToolError)
		if !ok {
			t.Fatalf("非 ToolError: %v", err)
		}
		return res.Content, te
	}
	return res.Content, nil
}

// TestWaitTaskExitedZero 验证进程已退出（exit 0）：返回退出码 + 日志尾部。
func TestWaitTaskExitedZero(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "out.log")
	if err := os.WriteFile(logPath, []byte("line1\nline2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pid := 4242
	e := registerFakeBG(t, pid, logPath)
	markDone(e, 0)

	content, te := waitTaskHandle(t, waitTaskArgs{PID: pid, TimeoutMS: 1000})
	if te != nil {
		t.Fatalf("exit 0 不应为错误: %v", te)
	}
	if content != "后台进程 4242 已退出（exit 0）\n日志尾部：\nline1\nline2\n" {
		t.Errorf("内容: %q", content)
	}
}

// TestWaitTaskExitedNonZero 验证非零退出走 ToolError（回填语义对齐 shell）。
func TestWaitTaskExitedNonZero(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "out.log")
	if err := os.WriteFile(logPath, []byte("error: boom"), 0o644); err != nil {
		t.Fatal(err)
	}
	pid := 4243
	e := registerFakeBG(t, pid, logPath)
	markDone(e, 1)

	_, te := waitTaskHandle(t, waitTaskArgs{PID: pid, TimeoutMS: 1000})
	if te == nil || !te.RespondToModel {
		t.Fatalf("非零退出应回填错误: %v", te)
	}
	if te.Message != "wait_task: 后台进程 4243 已退出（exit 1）\n日志尾部：\nerror: boom" {
		t.Errorf("错误文本: %q", te.Message)
	}
}

// TestWaitTaskTimeout 验证超时返回"仍在运行"（阻塞 timeout_ms 内不退出）。
func TestWaitTaskTimeout(t *testing.T) {
	pid := 4244
	e := registerFakeBG(t, pid, "")
	start := time.Now()
	content, te := waitTaskHandle(t, waitTaskArgs{PID: pid, TimeoutMS: 150})
	if te != nil {
		t.Fatalf("超时不应为错误: %v", te)
	}
	if time.Since(start) < 100*time.Millisecond {
		t.Errorf("未真正等待: %v", time.Since(start))
	}
	if content != "wait_task: 后台进程 4244 在 150ms 内仍在运行；可继续 wait_task 等待，或 read_file 查看日志：" {
		t.Errorf("内容: %q", content)
	}
	// 进程后退出 → 再次调用能拿到结果（done 信号与注册表注销无关，entry 引用有效）。
	markDone(e, 0)
	content, _ = waitTaskHandle(t, waitTaskArgs{PID: pid, TimeoutMS: 1000})
	if content != "后台进程 4244 已退出（exit 0）" {
		t.Errorf("退出后内容: %q", content)
	}
}

// TestWaitTaskUnknownPID 验证未知 PID 报错（已退出/非本会话启动）。
func TestWaitTaskUnknownPID(t *testing.T) {
	_, te := waitTaskHandle(t, waitTaskArgs{PID: 9999, TimeoutMS: 10})
	if te == nil || !te.RespondToModel {
		t.Fatalf("未知 PID 应报错: %v", te)
	}
}
