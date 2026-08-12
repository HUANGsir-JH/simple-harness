package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/agent-project/harness/internal/events"
)

// segMarker 是切分指令行（内部哨兵）：writer 收到后关闭当前文件、开新文件。
// 走 channel 保证与后续事件串行（保序，见 plan）。
const segMarker = "__segment__"

// flushMarker 是同步指令行（内部哨兵）：writer 收到后确认已写完此前所有行
// （AddCommand 落盘同步点；命令低频可接受）。
const flushMarker = "__flush__"

// transcript 行类型（C2，2026-08-10 统一为常量，替代三处裸字符串）：
// 写侧 OnAgentEvent / 读侧 load.go / TUI 渲染 run.go 共用。新增类型时同步
// 三处，load 的 switch 对未知类型走 default 跳过（读侧容错，Bug08），
// 不再静默吞。
const (
	LineTypeMeta       = "meta"
	LineTypeUser       = "user"
	LineTypeCommand    = "command"
	LineTypeThinking   = "thinking"
	LineTypeText       = "text"
	LineTypeToolUse    = "tool_use"
	LineTypeToolResult = "tool_result"
	LineTypeTurnStart  = "turn_start"
	LineTypeTurnEnd    = "turn_end"
)

// Line 是 transcript 的一行（块级事件，ADR-025）。resume 按 ordinal 排序加载。
type Line struct {
	Ordinal   int64           `json:"ordinal"`
	Type      string          `json:"type"` // LineType* 常量
	SessionID string          `json:"session_id,omitempty"`
	CWD       string          `json:"cwd,omitempty"`
	Model     string          `json:"model,omitempty"`
	CreatedAt string          `json:"created_at,omitempty"`
	Turn      int             `json:"turn,omitempty"`
	MsgID     string          `json:"msg_id,omitempty"` // thinking/text/tool_use 归属的 assistant 消息
	CallID    string          `json:"call_id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Args      json.RawMessage `json:"args,omitempty"`
	Success   *bool           `json:"success,omitempty"`
	Content   string          `json:"content,omitempty"`
	Text      string          `json:"text,omitempty"`
	// Signature 是 thinking 行的数字签名（ADR-025 修订完整回传）：块级落盘，
	// resume 恢复回 Message.ThinkingSignature（重放 thinking 块的凭据）。
	Signature string        `json:"signature,omitempty"`
	Sync      chan struct{} `json:"-"` // 内部 flush 确认（不序列化）
}

// TranscriptWriter 是块级 transcript 的异步 writer（ADR-025）。
//
// 顺序与并发安全（用户点名）：
//   - 单后台 goroutine 消费 channel，写序 = 入队序（FIFO）
//   - ordinal 在 writer goroutine 内自增（单线程无锁），resume 按序加载兜底
//   - channel 并发安全；文件写仅 writer goroutine（独占无锁）；切分走 channel
//     指令在 goroutine 内完成
//   - 进程崩溃丢缓冲尾部几块（可接受，远好于回合结束才写）
type TranscriptWriter struct {
	ch      chan Line
	dir     string
	file    *os.File
	segment int   // 当前文件序号（writer goroutine 内）
	ordinal int64 // 行序号（writer goroutine 内）
	turn    int   // 当前回合（OnAgentEvent 所在 goroutine，串行）
	done    chan struct{}
	// mu 保护 "closed 检查 + 发送" 的原子性（Bug06(a)）：Write/Flush/NewSegment
	// 持锁发送，Close 持锁设 closed + close(ch)，杜绝"发送到已关闭 channel"
	// 的 panic（写后关）。closeOnce 只保证 Close 自身幂等。
	mu     sync.Mutex
	closed bool

	closeOnce sync.Once // Close 幂等（REPL/RunTUI 双路径可能重复关闭）
}

// historyBuf 是 channel 缓冲：写入方（agent 事件回调）在缓冲满时阻塞，
// 保证不丢事件。
const historyBuf = 256

// NewTranscriptWriter 打开 historys 目录下最新文件（无则创建 history-1.jsonl）
// 并启动后台写 goroutine。
func NewTranscriptWriter(historyDir string) (*TranscriptWriter, error) {
	if err := os.MkdirAll(historyDir, 0o755); err != nil {
		return nil, fmt.Errorf("session: mkdir %s: %w", historyDir, err)
	}
	seg := currentSegment(historyDir)
	hasExisting := seg > 0 // 已有段 = resume 复用（续接 ordinal/turn）
	if seg == 0 {
		seg = 1
	}
	w := &TranscriptWriter{
		ch:      make(chan Line, historyBuf),
		dir:     historyDir,
		segment: seg,
		done:    make(chan struct{}),
	}
	if err := w.openSegment(); err != nil {
		return nil, err
	}
	// resume 续接（C3）：复用现有段时续接 ordinal/turn，避免同段内重复
	//（否则 Line.Ordinal 字段与注释承诺的"resume 按序加载兜底"不符）。
	if hasExisting {
		w.ordinal, w.turn = lastOrdinalTurn(historyPath(historyDir, seg))
	}
	go w.run()
	return w, nil
}

// lastOrdinalTurn 读取现有段的最大 ordinal 与 turn（resume 续接基准，C3）。
// 坏行跳过（Bug08 读侧容错）；无文件/全坏行返回 0。
func lastOrdinalTurn(path string) (ordinal int64, turn int) {
	_, _ = forEachLine(path, func(raw []byte) error {
		var line Line
		if err := json.Unmarshal(raw, &line); err != nil {
			return err
		}
		if line.Ordinal > ordinal {
			ordinal = line.Ordinal
		}
		if line.Turn > turn {
			turn = line.Turn
		}
		return nil
	})
	return ordinal, turn
}

// Write 入队一行（FIFO）。缓冲满时阻塞，保证不丢。
// 关闭后静默丢弃（Bug06(a)：写后关不再 panic，ADR-025 尽力而为写侧）。
func (w *TranscriptWriter) Write(line Line) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.ch <- line
}

// OnAgentEvent 把 agent 回合级事件转为 transcript 行（delta/error 不落盘）。
// 须在事件回调所在 goroutine 调用（单线程串行）。
func (w *TranscriptWriter) OnAgentEvent(ev events.Event) {
	var line Line
	switch ev.Type {
	case events.EventTurnStart:
		w.turn++
		line = Line{Type: LineTypeTurnStart, Turn: w.turn}
	case events.EventThinkingDone:
		line = Line{Type: LineTypeThinking, MsgID: ev.MsgID, Text: ev.Text, Signature: ev.Signature, Turn: w.turn}
	case events.EventTextDone:
		line = Line{Type: LineTypeText, MsgID: ev.MsgID, Text: ev.Text, Turn: w.turn}
	case events.EventToolCall:
		line = Line{Type: LineTypeToolUse, MsgID: ev.MsgID, CallID: ev.ToolCall.ID, Name: ev.ToolCall.Name, Args: ev.ToolCall.Args, Turn: w.turn}
	case events.EventToolResult:
		succ := ev.ToolResult.Success
		line = Line{Type: LineTypeToolResult, CallID: ev.ToolCall.ID, Success: &succ, Content: ev.ToolResult.Content, Turn: w.turn}
	case events.EventTurnDone:
		line = Line{Type: LineTypeTurnEnd, Turn: w.turn}
	default:
		return // thinking_delta/text_delta（流式）/error 不落盘
	}
	w.Write(line)
}

// NewSegment 切一个新 transcript 文件（压缩点：新文件以摘要+保留开头）。
// 调用方随后写入 seed 消息。经 channel 保序完成。关闭后 no-op。
func (w *TranscriptWriter) NewSegment() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.ch <- Line{Type: segMarker}
}

// Close 关闭 channel 并等后台 goroutine flush 后关闭文件（幂等）。
// 关闭后 Write/Flush/NewSegment 静默丢弃（Bug06(a)）。
func (w *TranscriptWriter) Close() error {
	var err error
	w.closeOnce.Do(func() {
		w.mu.Lock()
		w.closed = true
		close(w.ch)
		w.mu.Unlock()
		<-w.done
		err = w.file.Close()
	})
	return err
}

// Flush 等待此前所有入队行写入（同步点；AddCommand 落盘确认，命令低频可接受）。
// 走 w.ch（FIFO）保证顺序：flush 确认在它之前的行都写完。关闭后 no-op。
func (w *TranscriptWriter) Flush() {
	ack := make(chan struct{})
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.ch <- Line{Type: flushMarker, Sync: ack}
	w.mu.Unlock()
	<-ack
}

func (w *TranscriptWriter) run() {
	defer close(w.done)
	for line := range w.ch {
		switch {
		case line.Type == segMarker:
			w.segment++
			if err := w.openSegment(); err != nil {
				// v1：切分失败不阻塞后续（文件写错误进 file.Err？简化忽略）。
				_ = err
			}
			continue
		case line.Type == flushMarker:
			if line.Sync != nil {
				close(line.Sync)
			}
			continue
		}
		w.ordinal++
		line.Ordinal = w.ordinal
		data, err := json.Marshal(line)
		if err != nil {
			continue
		}
		if _, err := w.file.Write(append(data, '\n')); err != nil {
			// 写失败：忽略（v1 不引入错误通道，避免阻塞事件源）。
			_ = err
		}
	}
}

func (w *TranscriptWriter) openSegment() error {
	if w.file != nil {
		_ = w.file.Close()
	}
	f, err := os.OpenFile(historyPath(w.dir, w.segment), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	w.file = f
	return nil
}

func historyPath(historyDir string, n int) string {
	return filepath.Join(historyDir, fmt.Sprintf("history-%d.jsonl", n))
}

// currentSegment 返回 historys 目录下最大的文件序号；无文件返回 0。
func currentSegment(historyDir string) int {
	entries, err := os.ReadDir(historyDir)
	if err != nil {
		return 0
	}
	max := 0
	for _, e := range entries {
		if n, ok := parseHistoryName(e.Name()); ok && n > max {
			max = n
		}
	}
	return max
}

func parseHistoryName(name string) (int, bool) {
	const p = "history-"
	if !strings.HasPrefix(name, p) || !strings.HasSuffix(name, ".jsonl") {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, p), ".jsonl"))
	return n, err == nil
}
