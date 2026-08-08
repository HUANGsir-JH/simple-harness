package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/agent-project/harness/internal/messages"
)

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
	th, err := LoadThread(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(th.Messages) != 2 {
		t.Fatalf("thread 消息数=%d", len(th.Messages))
	}
	if th.Messages[0].Content != "摘要" {
		t.Errorf("seed 摘要缺失: %q", th.Messages[0].Content)
	}
	if th.Messages[1].Content != "after" {
		t.Errorf("新段内容错误: %q", th.Messages[1].Content)
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

	th, err := LoadThread(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(th.Messages) != 3 {
		t.Fatalf("消息数=%d, want 3 (user, assistant, tool)", len(th.Messages))
	}
	u := th.Messages[0]
	if u.Role != messages.RoleUser || u.Content != "hi" {
		t.Errorf("user 重建: %+v", u)
	}
	a := th.Messages[1]
	if a.Role != messages.RoleAssistant || a.Thinking != "think" || a.Content != "answer" {
		t.Errorf("assistant 重建: %+v", a)
	}
	if len(a.ToolCalls) != 1 || a.ToolCalls[0].ID != "c1" || string(a.ToolCalls[0].Args) != `{"path":"a"}` {
		t.Errorf("tool_calls 重建: %+v", a.ToolCalls)
	}
	tr := th.Messages[2]
	if tr.Role != messages.RoleTool || len(tr.ToolResults) != 1 || tr.ToolResults[0].ToolCallID != "c1" || tr.ToolResults[0].Content != "file" {
		t.Errorf("tool 消息重建: %+v", tr.ToolResults)
	}
}
