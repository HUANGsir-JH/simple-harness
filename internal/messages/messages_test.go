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

func TestConversationJSONLFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	conv := NewConversation()
	conv.Add(NewUserMessage("hello"))
	conv.Add(NewAssistantMessage("hi there"))
	if err := conv.SaveJSONL(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := LoadConversationJSONL(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// 注意：conversation 的 ID/CreatedAt 是会话元数据（来自文件名），
	// 不持久化在 JSONL 消息行中；只验证消息序列。
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

func TestLoadConversationJSONLMissingID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	// 没有 id 字段的文件也必须能加载。
	if err := os.WriteFile(path, []byte(`{"role":"user","content":"a"}
{"role":"assistant","content":"b"}
`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	conv, err := LoadConversationJSONL(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(conv.Messages) != 2 {
		t.Fatalf("messages: got %d want 2", len(conv.Messages))
	}
	if conv.Messages[0].ID == "" || conv.Messages[1].ID == "" {
		t.Error("expected generated ids for messages without id")
	}
}

func TestLoadConversationJSONLBadLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.jsonl")
	if err := os.WriteFile(path, []byte("not json\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadConversationJSONL(path); err == nil {
		t.Fatal("expected error on invalid json line")
	}
}

func TestReadWriteConversationJSONL(t *testing.T) {
	conv := NewConversation()
	conv.Add(NewUserMessage("q"))
	conv.Add(NewAssistantMessage("a"))

	var buf bytes.Buffer
	if err := WriteConversationJSONL(&buf, conv); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := ReadConversationJSONL(bytes.NewReader(buf.Bytes()))
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
