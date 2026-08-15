package completion

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// Queue 是会话级的完成事件队列：内存队列 + completions.json 落盘 + OnAppend
// 运行时唤起信号。
//
// 并发模型：Append（生产端 Wait goroutine）与 Drain/PendingCount（注入端
// agent run goroutine / TUI Update）可能并发——全部状态在 mu 内读写；落盘在
// 锁内（串行化，文件写独占）；OnAppend 在锁外调用（回调内再进 Queue 不会
// 重入死锁，且回调只发程序内信号）。
type Queue struct {
	mu     sync.Mutex
	events []Event
	path   string
	// onAppend 是追加回调（锁内快照、锁外调用）。nil = 未订阅（非 TUI）。
	onAppend func()
}

// New 打开 path 上的队列；文件不存在 = 空队列。已有事件（上次会话未注入的
// 完成通知）加载进内存，resume 后由注入端在下一次采样前补注入。读侧容错
// （Bug08 同款精神）：文件损坏/不可读时从空队列开始，不阻塞会话。
func New(path string) *Queue {
	q := &Queue{path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		return q // 不存在/不可读 = 空队列
	}
	var events []Event
	if err := json.Unmarshal(data, &events); err != nil {
		return q // 损坏 = 空队列（容错，不锁死 resume）
	}
	q.events = events
	return q
}

// Append 追加一条完成事件：锁内 append + 全量原子落盘（pid 临时名 + fsync +
// rename，agentstate.SaveFile 同款），锁外调 onAppend。
// 落盘失败静默忽略（尽力而为：内存队列仍在，本进程内注入不受影响；
// resume 恢复场景的丢失窗口见 Drain 注释）。
func (q *Queue) Append(ev Event) {
	q.mu.Lock()
	q.events = append(q.events, ev)
	_ = q.persist()
	fn := q.onAppend
	q.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// Drain 取出全部待注入事件并清空（含落盘清空）。空队列返回 nil。
// 崩溃窗口（已记录）：Drain 落盘清空与调用方逐条注入之间存在极小窗口，
// 进程崩溃会丢这批事件（不重复注入优先——重复通知比丢失通知更糟）。
func (q *Queue) Drain() []Event {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.events) == 0 {
		return nil
	}
	drained := q.events
	q.events = nil
	_ = q.persist()
	return drained
}

// PendingCount 返回待注入事件数（唤醒器防空跑判断用）。
func (q *Queue) PendingCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.events)
}

// SetOnAppend 设置追加回调（nil 取消订阅）。与 Append 并发安全（锁内读写）。
func (q *Queue) SetOnAppend(fn func()) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.onAppend = fn
}

// persist 全量原子落盘（调用方须持锁）。空队列落盘为 []（而非 null），
// 保持文件语义稳定。
func (q *Queue) persist() error {
	events := q.events
	if events == nil {
		events = []Event{}
	}
	data, err := json.Marshal(events)
	if err != nil {
		return fmt.Errorf("completion: marshal: %w", err)
	}
	tmp := fmt.Sprintf("%s.%d.tmp", q.path, os.Getpid())
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("completion: create %s: %w", tmp, err)
	}
	cleanup := func() {
		f.Close()
		os.Remove(tmp)
	}
	if _, err := f.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("completion: write %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("completion: sync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("completion: close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, q.path); err != nil {
		return fmt.Errorf("completion: rename %s: %w", q.path, err)
	}
	return nil
}
