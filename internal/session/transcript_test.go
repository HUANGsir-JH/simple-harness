package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-project/harness/internal/messages"
)

// TestLoadToleratesHugeLine 验证读侧无行长限制（Bug08）：超大 tool_result 行
// 不再锁死 resume（旧 bufio.Scanner 4MB 上限 → ErrTooLong），后续正常行也读到。
func TestLoadToleratesHugeLine(t *testing.T) {
	dir := t.TempDir()
	w, err := NewTranscriptWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	succ := true
	huge := strings.Repeat("x", 5*1024*1024) // 5MB，超过旧 Scanner 的 4MB 上限
	w.Write(Line{Type: "tool_result", CallID: "c1", Success: &succ, Content: huge})
	w.Write(Line{Type: "user", MsgID: "m1", Content: "正常消息"})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	lines, skipped, err := LoadLines(dir)
	if err != nil {
		t.Fatalf("LoadLines 不应失败（旧实现 token too long 锁死）：%v", err)
	}
	if skipped != 0 {
		t.Errorf("超大行是合法 JSON，不应跳过：skipped=%d", skipped)
	}
	if len(lines) != 2 || lines[1].Content != "正常消息" {
		t.Fatalf("lines=%d, want 2（含后续正常行）", len(lines))
	}
	conv, err := LoadConversation(dir)
	if err != nil {
		t.Fatalf("LoadConversation 不应失败：%v", err)
	}
	if n := len(conv.Messages); n != 2 || conv.Messages[n-1].Content != "正常消息" {
		t.Fatalf("conversation 最后一条应含正常 user 行，got %d 条", n)
	}
}

// TestLoadSkipsCorruptLine 验证坏行跳过 + 计数上报（Bug08）：崩溃截断的半
// JSON 行不再让整会话无法打开，前面完好的行保留，skipped 计数可见。
func TestLoadSkipsCorruptLine(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	good, _ := json.Marshal(Line{Type: "user", MsgID: "m1", Content: "完好"})
	path := filepath.Join(dir, "history-1.jsonl")
	// 完好行 + 崩溃截断的半 JSON + 完好行（后者无换行结尾）。
	content := string(good) + "\n{\"type\":\"tool_result\",\"call_id\":\"c1\",\"content\":\"trun" + "\n" + string(good)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	lines, skipped, err := LoadLines(dir)
	if err != nil {
		t.Fatalf("LoadLines 不应失败（旧实现 bad line 锁死）：%v", err)
	}
	if skipped != 1 {
		t.Errorf("skipped=%d, want 1（半 JSON 行）", skipped)
	}
	if len(lines) != 2 || lines[0].Content != "完好" || lines[1].Content != "完好" {
		t.Fatalf("lines=%d, want 2（坏行跳过、前后完好行保留）", len(lines))
	}
	conv, err := LoadConversation(dir)
	if err != nil {
		t.Fatalf("LoadConversation 不应失败：%v", err)
	}
	if n := len(conv.Messages); n != 2 {
		t.Fatalf("conversation 应保留完好 user 行，got %d 条", n)
	}
}

// TestWriteAfterClose 验证 Close 后 Write/Flush/NewSegment 静默丢弃不 panic
// （Bug06(a)：写后关 send on closed channel 崩溃整个进程）。
func TestWriteAfterClose(t *testing.T) {
	dir := t.TempDir()
	w, err := NewTranscriptWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// 写后关不应 panic（静默丢弃）。
	w.Write(Line{Type: "user", MsgID: "m1", Content: "x"})
	w.Write(Line{Type: "text", Text: "y"})
	w.NewSegment()
	w.Flush()
	// Close 幂等（可重复调）。
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// 文件为空（写后关的行被丢弃）。
	lines := readLines(t, filepath.Join(dir, "history-1.jsonl"))
	if len(lines) != 0 {
		t.Errorf("写后关应被丢弃，got %d 行", len(lines))
	}
}

// TestConcurrentCloseWrite 验证 Close 与并发 Write 竞态不 panic（Bug06(a)
// 的第二条触发路径：缓冲满阻塞的发送者 + close(ch) 竞态）。
func TestConcurrentCloseWrite(t *testing.T) {
	dir := t.TempDir()
	w, err := NewTranscriptWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				w.Write(Line{Type: "text", Text: "t"})
			}
		}()
	}
	time.Sleep(2 * time.Millisecond) // 让部分 Write 在途时 Close
	_ = w.Close()
	wg.Wait()
}

// readLines 读回 history 文件并解析为 Line 列表。
func readLines(t *testing.T, path string) []Line {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []Line
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if l == "" {
			continue
		}
		var ln Line
		if err := json.Unmarshal([]byte(l), &ln); err != nil {
			t.Fatalf("bad line %q: %v", l, err)
		}
		out = append(out, ln)
	}
	return out
}

// TestWriterOrder 验证 FIFO 保序 + ordinal 单调递增。
func TestWriterOrder(t *testing.T) {
	dir := t.TempDir()
	w, err := NewTranscriptWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 10; i++ {
		w.Write(Line{Type: "text", Text: fmt.Sprintf("t%d", i)})
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	lines := readLines(t, filepath.Join(dir, "history-1.jsonl"))
	if len(lines) != 10 {
		t.Fatalf("lines=%d", len(lines))
	}
	for i, ln := range lines {
		if ln.Ordinal != int64(i+1) || ln.Text != fmt.Sprintf("t%d", i+1) {
			t.Errorf("line %d: %+v", i, ln)
		}
	}
}

// TestConcurrentWrite 验证并发入队安全：所有行都写入且 ordinal 唯一覆盖。
func TestConcurrentWrite(t *testing.T) {
	dir := t.TempDir()
	w, err := NewTranscriptWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	const g, n = 8, 50
	var wg sync.WaitGroup
	for i := 0; i < g; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < n; j++ {
				w.Write(Line{Type: "text", Text: fmt.Sprintf("g%d-j%d", i, j)})
			}
		}(i)
	}
	wg.Wait()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	lines := readLines(t, filepath.Join(dir, "history-1.jsonl"))
	if len(lines) != g*n {
		t.Fatalf("lines=%d, want %d", len(lines), g*n)
	}
	seen := map[int64]bool{}
	for _, ln := range lines {
		if seen[ln.Ordinal] {
			t.Fatalf("ordinal %d 重复", ln.Ordinal)
		}
		seen[ln.Ordinal] = true
	}
	for i := 1; i <= g*n; i++ {
		if !seen[int64(i)] {
			t.Fatalf("缺 ordinal %d", i)
		}
	}
}

// TestNewSegment 验证压缩切分：新文件 history-2 从 seed 开始，Load 只读最新。
func TestNewSegment(t *testing.T) {
	dir := t.TempDir()
	w, err := NewTranscriptWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(Line{Type: "user", MsgID: "m0", Content: "old"})
	w.Write(Line{Type: "text", MsgID: "m1", Text: "before"})
	w.NewSegment()
	// 压缩点：seed 摘要 + 保留消息写入新文件。
	w.Write(Line{Type: "user", MsgID: "m2", Content: "摘要"})
	w.Write(Line{Type: "text", MsgID: "m3", Text: "after"})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("应有两个文件, got %d", len(entries))
	}
	// Load 只读最新（history-2）：user(摘要) + assistant(after)。
	conv, err := LoadConversation(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(conv.Messages) != 2 {
		t.Fatalf("conversation 消息数=%d", len(conv.Messages))
	}
	if conv.Messages[0].Content != "摘要" {
		t.Errorf("seed 摘要缺失: %q", conv.Messages[0].Content)
	}
	if conv.Messages[1].Content != "after" {
		t.Errorf("新段内容错误: %q", conv.Messages[1].Content)
	}
}

// TestLoadReconstruct 验证块序列 → Message 重建（thinking 入字段、tool 合并）。
func TestLoadReconstruct(t *testing.T) {
	dir := t.TempDir()
	w, err := NewTranscriptWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	succ := true
	w.Write(Line{Type: "user", MsgID: "m1", Content: "hi"})
	w.Write(Line{Type: "thinking", MsgID: "m2", Text: "think", Turn: 1})
	w.Write(Line{Type: "text", MsgID: "m2", Text: "answer", Turn: 1})
	w.Write(Line{Type: "tool_use", MsgID: "m2", CallID: "c1", Name: "read_file", Args: json.RawMessage(`{"path":"a"}`), Turn: 1})
	w.Write(Line{Type: "tool_result", CallID: "c1", Success: &succ, Content: "file", Turn: 1})
	w.Write(Line{Type: "turn_end", Turn: 1})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	conv, err := LoadConversation(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(conv.Messages) != 3 {
		t.Fatalf("消息数=%d, want 3 (user, assistant, tool)", len(conv.Messages))
	}
	u := conv.Messages[0]
	if u.Role != messages.RoleUser || u.Content != "hi" {
		t.Errorf("user 重建: %+v", u)
	}
	a := conv.Messages[1]
	if a.Role != messages.RoleAssistant || a.Thinking != "think" || a.Content != "answer" {
		t.Errorf("assistant 重建: %+v", a)
	}
	if len(a.ToolCalls) != 1 || a.ToolCalls[0].ID != "c1" || string(a.ToolCalls[0].Args) != `{"path":"a"}` {
		t.Errorf("tool_calls 重建: %+v", a.ToolCalls)
	}
	tr := conv.Messages[2]
	if tr.Role != messages.RoleTool || len(tr.ToolResults) != 1 || tr.ToolResults[0].ToolCallID != "c1" || tr.ToolResults[0].Content != "file" {
		t.Errorf("tool 消息重建: %+v", tr.ToolResults)
	}
}
