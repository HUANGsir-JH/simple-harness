package messages

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMessageJSONLRoundTrip(t *testing.T) {
	m := &Message{
		ID:      "msg_1",
		Role:    RoleAssistant,
		Content: "I'll look at that.",
		ToolCalls: []ToolCall{
			{
				ID:   "call_1",
				Name: "read_file",
				Args: json.RawMessage(`{"path":"a.txt"}`),
				Result: &ToolResult{
					Success: true,
					Content: "file contents",
				},
			},
		},
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(m); err != nil {
		t.Fatalf("encode: %v", err)
	}

	var got Message
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != m.ID || got.Role != m.Role || got.Content != m.Content {
		t.Errorf("scalar mismatch: got %+v want %+v", got, m)
	}
	if len(got.ToolCalls) != 1 {
		t.Fatalf("tool calls: got %d want 1", len(got.ToolCalls))
	}
	tc := got.ToolCalls[0]
	if tc.ID != "call_1" || tc.Name != "read_file" || string(tc.Args) != `{"path":"a.txt"}` {
		t.Errorf("tool call mismatch: %+v", tc)
	}
	if tc.Result == nil || !tc.Result.Success || tc.Result.Content != "file contents" {
		t.Errorf("tool result mismatch: %+v", tc.Result)
	}
}

func TestMessageToolResultOnly(t *testing.T) {
	m := NewToolResultMessage("call_9", false, "command failed")
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(m); err != nil {
		t.Fatalf("encode: %v", err)
	}
	s := buf.String()
	if !strings.Contains(s, `"tool_call_id":"call_9"`) {
		t.Errorf("missing tool_call_id in %s", s)
	}
	if !strings.Contains(s, `"role":"tool"`) {
		t.Errorf("missing role in %s", s)
	}
}

func TestThreadJSONLFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	th := NewThread()
	th.Add(NewUserMessage("hello"))
	th.Add(NewAssistantMessage("hi there"))
	if err := th.SaveJSONL(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := LoadThreadJSONL(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// Note: thread ID/CreatedAt are session metadata (from the filename), not
	// persisted in the JSONL message lines; verify the message sequence.
	if len(got.Messages) != 2 {
		t.Fatalf("messages: got %d want 2", len(got.Messages))
	}
	if len(got.Messages) != 2 {
		t.Fatalf("messages: got %d want 2", len(got.Messages))
	}
	if got.Messages[0].Role != RoleUser || got.Messages[0].Content != "hello" {
		t.Errorf("msg0 mismatch: %+v", got.Messages[0])
	}
	if got.Messages[1].Role != RoleAssistant || got.Messages[1].Content != "hi there" {
		t.Errorf("msg1 mismatch: %+v", got.Messages[1])
	}
}

func TestLoadThreadJSONLMissingID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	// A file with no id fields must still load.
	if err := os.WriteFile(path, []byte(`{"role":"user","content":"a"}
{"role":"assistant","content":"b"}
`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	th, err := LoadThreadJSONL(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(th.Messages) != 2 {
		t.Fatalf("messages: got %d want 2", len(th.Messages))
	}
	if th.Messages[0].ID == "" || th.Messages[1].ID == "" {
		t.Error("expected generated ids for messages without id")
	}
}

func TestLoadThreadJSONLBadLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.jsonl")
	if err := os.WriteFile(path, []byte("not json\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadThreadJSONL(path); err == nil {
		t.Fatal("expected error on invalid json line")
	}
}

func TestReadWriteThreadJSONL(t *testing.T) {
	th := NewThread()
	th.Add(NewUserMessage("q"))
	th.Add(NewAssistantMessage("a"))

	var buf bytes.Buffer
	if err := WriteThreadJSONL(&buf, th); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := ReadThreadJSONL(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("messages: got %d want 2", len(got.Messages))
	}
}

func TestAppendToolResult(t *testing.T) {
	m := NewAssistantMessage("")
	m.ToolCalls = []ToolCall{{ID: "c1", Name: "shell_command"}}

	if !m.AppendToolResult("c1", &ToolResult{Success: true, Content: "ok"}) {
		t.Fatal("expected match on c1")
	}
	if m.ToolCalls[0].Result == nil || m.ToolCalls[0].Result.Content != "ok" {
		t.Errorf("result not recorded: %+v", m.ToolCalls[0])
	}
	if m.AppendToolResult("nope", &ToolResult{}) {
		t.Error("expected no match for unknown call id")
	}
}
