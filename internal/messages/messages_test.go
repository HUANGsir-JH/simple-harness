package messages

import (
	"bytes"
	"encoding/json"
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
