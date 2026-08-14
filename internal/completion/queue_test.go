package completion

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func testQueue(t *testing.T) *Queue {
	t.Helper()
	return New(filepath.Join(t.TempDir(), "completions.json"))
}

func testEvent(i int) Event {
	code := i
	return Event{
		ToolName:  "shell_command",
		Result:    fmt.Sprintf("后台进程 %d 已退出", i),
		ExitCode:  &code,
		DoneAt:    "2026-08-13T00:00:00Z",
		SessionID: "sess",
	}
}

// TestQueueAppendDrain 验证 Append/Drain/PendingCount 基本语义与顺序保持。
func TestQueueAppendDrain(t *testing.T) {
	q := testQueue(t)
	if q.PendingCount() != 0 {
		t.Fatalf("新队列 pending 应为 0，got %d", q.PendingCount())
	}
	if got := q.Drain(); got != nil {
		t.Fatalf("空队列 Drain 应为 nil，got %v", got)
	}
	for i := 0; i < 3; i++ {
		q.Append(testEvent(i))
	}
	if q.PendingCount() != 3 {
		t.Fatalf("pending 应为 3，got %d", q.PendingCount())
	}
	drained := q.Drain()
	if len(drained) != 3 {
		t.Fatalf("Drain 应返回 3 条，got %d", len(drained))
	}
	for i, ev := range drained {
		if ev.Result != testEvent(i).Result {
			t.Errorf("Drain[%d] 顺序破坏: %q", i, ev.Result)
		}
	}
	if q.PendingCount() != 0 {
		t.Errorf("Drain 后 pending 应为 0，got %d", q.PendingCount())
	}
	if got := q.Drain(); got != nil {
		t.Errorf("二次 Drain 应为 nil，got %v", got)
	}
}

// TestQueuePersistRoundtrip 验证 Append 落盘 + 重新打开恢复（resume 场景）。
func TestQueuePersistRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "completions.json")
	q := New(path)
	q.Append(testEvent(1))
	q.Append(testEvent(2))

	q2 := New(path) // 模拟 resume：重新打开同一文件
	if q2.PendingCount() != 2 {
		t.Fatalf("重新打开后 pending 应为 2，got %d", q2.PendingCount())
	}
	drained := q2.Drain()
	if len(drained) != 2 || drained[0].Result != testEvent(1).Result || drained[1].Result != testEvent(2).Result {
		t.Errorf("恢复事件内容不符: %+v", drained)
	}
	if *drained[0].ExitCode != 1 {
		t.Errorf("ExitCode 恢复不符: %d", *drained[0].ExitCode)
	}
	// Drain 落盘清空：再次打开应为空。
	q3 := New(path)
	if q3.PendingCount() != 0 {
		t.Errorf("Drain 落盘后重新打开 pending 应为 0，got %d", q3.PendingCount())
	}
}

// TestQueueOnAppend 验证 OnAppend 每次 Append 触发一次、Drain 不触发。
func TestQueueOnAppend(t *testing.T) {
	q := testQueue(t)
	var mu sync.Mutex
	var calls int
	q.SetOnAppend(func() {
		mu.Lock()
		calls++
		mu.Unlock()
	})
	q.Append(testEvent(1))
	q.Append(testEvent(2))
	_ = q.Drain()
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Errorf("OnAppend 应触发 2 次（仅 Append），got %d", calls)
	}
}

// TestQueueSetOnAppendNil 验证 nil 取消订阅后 Append 不 panic。
func TestQueueSetOnAppendNil(t *testing.T) {
	q := testQueue(t)
	q.SetOnAppend(func() {})
	q.SetOnAppend(nil)
	q.Append(testEvent(1)) // 不应 panic
	if q.PendingCount() != 1 {
		t.Errorf("pending 应为 1，got %d", q.PendingCount())
	}
}

// TestQueueNewMissingFile 验证文件不存在 = 空队列。
func TestQueueNewMissingFile(t *testing.T) {
	q := New(filepath.Join(t.TempDir(), "nope", "completions.json"))
	if q.PendingCount() != 0 {
		t.Errorf("无文件应空队列，got %d", q.PendingCount())
	}
}

// TestQueueNewCorruptFile 验证损坏文件容错（空队列起步，不锁死 resume）。
func TestQueueNewCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "completions.json")
	if err := os.WriteFile(path, []byte("{not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	q := New(path)
	if q.PendingCount() != 0 {
		t.Errorf("损坏文件应空队列起步，got %d", q.PendingCount())
	}
	q.Append(testEvent(1)) // 覆盖损坏文件后应正常工作
	q2 := New(path)
	if q2.PendingCount() != 1 {
		t.Errorf("覆盖后重新打开 pending 应为 1，got %d", q2.PendingCount())
	}
}

// TestQueueConcurrent 并发 Append + Drain/PendingCount（配合 -race 跑）。
func TestQueueConcurrent(t *testing.T) {
	q := testQueue(t)
	var wg sync.WaitGroup
	var drained atomic.Int64 // drain goroutine 取走的事件计数（守恒核对用）
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				q.Append(testEvent(g*100 + i))
				_ = q.PendingCount()
			}
		}(g)
	}
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				drained.Add(int64(len(q.Drain())))
				_ = q.PendingCount()
			}
		}()
	}
	wg.Wait()
	// 最终守恒检查：全部 400 条 = 并发期被 Drain 的 + 最终 Drain 的 + 残留 pending。
	const total = 400
	seen := len(q.Drain())
	if got := drained.Load() + int64(seen) + int64(q.PendingCount()); got != total {
		t.Errorf("事件守恒破坏: total=%d drained=%d seen=%d pending=%d", total, drained.Load(), seen, q.PendingCount())
	}
}
