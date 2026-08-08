package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/agent-project/harness/internal/agent"
	"github.com/agent-project/harness/internal/messages"
)

// segMarker 是切分指令行（内部哨兵）：writer 收到后关闭当前文件、开新文件。
// 走 channel 保证与后续事件串行（保序，见 plan）。
const segMarker = "__segment__"

// Line 是 transcript 的一行（块级事件，ADR-025）。resume 按 ordinal 排序加载。
type Line struct {
	Ordinal   int64           `json:"ordinal"`
	Type      string          `json:"type"` // meta|user|thinking|text|tool_use|tool_result|turn_start|turn_end
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
	dir     string // historys 目录
	file    *os.File
	segment int   // 当前文件序号（writer goroutine 内）
	ordinal int64 // 行序号（writer goroutine 内）
	turn    int   // 当前回合（OnAgentEvent 所在 goroutine，串行）
	done    chan struct{}
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
	go w.run()
	return w, nil
}

// Write 入队一行（FIFO）。缓冲满时阻塞，保证不丢。
func (w *TranscriptWriter) Write(line Line) {
	w.ch <- line
}

// OnAgentEvent 把 agent 回合级事件转为 transcript 行（delta/error 不落盘）。
// 须在事件回调所在 goroutine 调用（单线程串行）。
func (w *TranscriptWriter) OnAgentEvent(ev agent.Event) {
	var line Line
	switch ev.Type {
	case agent.EventTurnStart:
		w.turn++
		line = Line{Type: "turn_start", Turn: w.turn}
	case agent.EventThinkingDone:
		line = Line{Type: "thinking", MsgID: ev.MsgID, Text: ev.Text, Turn: w.turn}
	case agent.EventTextDone:
		line = Line{Type: "text", MsgID: ev.MsgID, Text: ev.Text, Turn: w.turn}
	case agent.EventToolCall:
		line = Line{Type: "tool_use", MsgID: ev.MsgID, CallID: ev.ToolCall.ID, Name: ev.ToolCall.Name, Args: ev.ToolCall.Args, Turn: w.turn}
	case agent.EventToolResult:
		succ := ev.ToolResult.Success
		line = Line{Type: "tool_result", CallID: ev.ToolCall.ID, Success: &succ, Content: ev.ToolResult.Content, Turn: w.turn}
	case agent.EventTurnDone:
		line = Line{Type: "turn_end", Turn: w.turn}
	default:
		return // thinking_delta/text_delta（流式）/error 不落盘
	}
	w.Write(line)
}

// NewSegment 切一个新 transcript 文件（压缩点：新文件以摘要+保留开头）。
// 调用方随后写入 seed 消息。经 channel 保序完成。
func (w *TranscriptWriter) NewSegment() {
	w.ch <- Line{Type: segMarker}
}

// Close 关闭 channel 并等后台 goroutine flush 后关闭文件。
func (w *TranscriptWriter) Close() error {
	close(w.ch)
	<-w.done
	return w.file.Close()
}

func (w *TranscriptWriter) run() {
	defer close(w.done)
	for line := range w.ch {
		if line.Type == segMarker {
			w.segment++
			if err := w.openSegment(); err != nil {
				// v1：切分失败不阻塞后续（文件写错误进 file.Err？简化忽略）。
				_ = err
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

// LoadThread 读取 historys 目录下最大序号文件，按 ordinal 逐行重建 thread。
// 最新文件即有效历史（压缩切分后新文件以摘要+保留开头；旧文件纯审计）。
func LoadThread(historyDir string) (*messages.Thread, error) {
	seg := currentSegment(historyDir)
	if seg == 0 {
		return nil, fmt.Errorf("session: no transcript found in %s", historyDir)
	}
	return loadHistoryFile(historyPath(historyDir, seg))
}

func loadHistoryFile(path string) (*messages.Thread, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("session: open %s: %w", path, err)
	}
	defer f.Close()

	th := messages.NewThread()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var cur *messages.Message // 当前 assistant（同 msg_id 累积 thinking/text/tool_use）
	for sc.Scan() {
		var line Line
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			return nil, fmt.Errorf("session: bad line: %w", err)
		}
		switch line.Type {
		case "user":
			cur = nil
			th.Add(&messages.Message{ID: line.MsgID, Role: messages.RoleUser, Content: line.Content})
		case "thinking":
			cur = ensureAssistant(th, cur, line.MsgID)
			cur.Thinking += line.Text
		case "text":
			cur = ensureAssistant(th, cur, line.MsgID)
			cur.Content += line.Text
		case "tool_use":
			cur = ensureAssistant(th, cur, line.MsgID)
			cur.ToolCalls = append(cur.ToolCalls, messages.ToolCall{ID: line.CallID, Name: line.Name, Args: line.Args})
		case "tool_result":
			cur = nil
			appendToolResult(th, line)
		case "meta", "turn_start", "turn_end", segMarker:
			// 无消息语义
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return th, nil
}

// ensureAssistant 返回消息 id 对应的 assistant 消息；若无则新建追加。
// thinking/text/tool_use 同属一个 assistant（同一次采样轮）。
func ensureAssistant(th *messages.Thread, cur *messages.Message, msgID string) *messages.Message {
	if cur != nil && cur.ID == msgID {
		return cur
	}
	cur = &messages.Message{ID: msgID, Role: messages.RoleAssistant}
	th.Add(cur)
	return cur
}

// appendToolResult 追加 tool_result 块：与上一条 tool 消息合并（多块，满足
// anthropic 紧邻要求），否则新建 tool 消息。
func appendToolResult(th *messages.Thread, line Line) {
	succ := line.Success != nil && *line.Success
	block := messages.ToolResultBlock{ToolCallID: line.CallID, Success: succ, Content: line.Content}
	if n := len(th.Messages); n > 0 && th.Messages[n-1].Role == messages.RoleTool {
		last := th.Messages[n-1]
		last.ToolResults = append(last.ToolResults, block)
		return
	}
	th.Add(&messages.Message{Role: messages.RoleTool, ToolResults: []messages.ToolResultBlock{block}})
}
