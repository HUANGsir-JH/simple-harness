package middleware

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/messages"
)

// longContent 构造超过 MaxOutputChars 的测试内容（前缀/后缀可识别）。
func longContent(prefix, suffix string) string {
	return prefix + strings.Repeat("x", MaxOutputChars*2) + suffix
}

// TestEvictContentShort 验证短文本原样返回（不截断、不落盘）。
func TestEvictContentShort(t *testing.T) {
	rc := NewRuntimeContext()
	rc.StatePath = filepath.Join(t.TempDir(), "sess", "agentstate.json")
	if got := EvictContent(rc, "hello"); got != "hello" {
		t.Errorf("short: got %q", got)
	}
}

// TestEvictContentLong 验证超长文本 head/tail 双端保留 + 落盘 + 路径提示。
func TestEvictContentLong(t *testing.T) {
	dir := t.TempDir()
	rc := NewRuntimeContext()
	rc.StatePath = filepath.Join(dir, "sess", "agentstate.json")

	prefix := "HEAD-START-"
	suffix := "TAIL-END-"
	got := EvictContent(rc, longContent(prefix, suffix))

	// 头部/尾部保留。
	if !strings.Contains(got, prefix) {
		t.Error("missing head prefix")
	}
	if !strings.Contains(got, suffix) {
		t.Error("missing tail suffix")
	}
	// 路径提示。
	if !strings.Contains(got, "完整内容已保存到") {
		t.Error("missing save hint")
	}
	if !strings.Contains(got, "read_file") {
		t.Error("missing read_file hint")
	}
	// 落盘文件存在且内容 = 全量。
	evictions := filepath.Join(dir, "sess", "evictions")
	entries, err := os.ReadDir(evictions)
	if err != nil || len(entries) != 1 {
		t.Fatalf("evictions dir: %v entries=%d", err, len(entries))
	}
	data, err := os.ReadFile(filepath.Join(evictions, entries[0].Name()))
	if err != nil {
		t.Fatalf("read eviction file: %v", err)
	}
	if string(data) != longContent(prefix, suffix) {
		t.Error("eviction file content != full content")
	}
}

// TestEvictContentNilRC 验证 rc 为 nil（测试/非会话）退化纯截断、不落盘。
func TestEvictContentNilRC(t *testing.T) {
	got := EvictContent(nil, longContent("a", "b"))
	if len(got) > MaxOutputChars+1000 {
		t.Errorf("nil rc: output too long (%d)", len(got))
	}
	if !strings.Contains(got, "[truncated]") {
		t.Error("nil rc: expected truncation marker")
	}
}

// TestEvictContentNoStatePath 验证 StatePath 为空时不落盘（退化截断）。
func TestEvictContentNoStatePath(t *testing.T) {
	rc := NewRuntimeContext()
	got := EvictContent(rc, longContent("a", "b"))
	if !strings.Contains(got, "[truncated]") {
		t.Error("no statepath: expected truncation marker")
	}
	if strings.Contains(got, "完整内容已保存到") {
		t.Error("no statepath: should not write file")
	}
}

// testChain 构造含 ToolOutputMiddleware 的链（onToolCall 只挂它）。
func testChain() *Chain {
	return NewChain(ToolOutputMiddleware{})
}

// TestToolOutputMiddlewareTruncates 验证 onToolCall after 改写本批 tool_result
// 消息、不碰历史。
func TestToolOutputMiddlewareTruncates(t *testing.T) {
	conv := messages.NewConversation()
	// 历史：一条长内容 tool_result（resume 场景，不应被改写）。
	conv.Add(messages.NewToolResultsMessage([]messages.ToolResultBlock{
		{ToolCallID: "old", Success: true, Content: longContent("HIST", "OLD")},
	}))

	rc := NewRuntimeContext()
	rc.Messages = conv
	rc.State = agentstate.New("s1", "m", t.TempDir())
	rc.StatePath = filepath.Join(t.TempDir(), "sess", "agentstate.json")

	chain := testChain()
	before := len(conv.Messages)

	// 模拟一批工具调用：core 在 conversation 追加一条长 tool_result。
	core := func(ctx context.Context, rc *RuntimeContext, in ToolCallInput) error {
		conv.Add(messages.NewToolResultsMessage([]messages.ToolResultBlock{
			{ToolCallID: "c1", Success: true, Content: longContent("NEW", "RESULT")},
			{ToolCallID: "c2", Success: false, Content: "短错误"},
		}))
		return nil
	}
	wrapped := chain.WrapToolCall(core)
	if err := wrapped(context.Background(), rc, ToolCallInput{Calls: nil}); err != nil {
		t.Fatalf("wrapped: %v", err)
	}

	// 只新增了一条消息。
	if len(conv.Messages) != before+1 {
		t.Fatalf("messages: got %d want %d", len(conv.Messages), before+1)
	}
	newMsg := conv.Messages[before]
	// 长内容被截断（含路径提示），短内容不变。
	if !strings.Contains(newMsg.ToolResults[0].Content, "完整内容已保存到") {
		t.Error("new long result not truncated")
	}
	if !strings.Contains(newMsg.ToolResults[0].Content, "NEW") || !strings.Contains(newMsg.ToolResults[0].Content, "RESULT") {
		t.Error("new result head/tail missing")
	}
	if newMsg.ToolResults[1].Content != "短错误" {
		t.Errorf("short result changed: %q", newMsg.ToolResults[1].Content)
	}
	// 历史未被改写。
	if !strings.Contains(conv.Messages[0].ToolResults[0].Content, "HIST") {
		t.Error("history was modified")
	}
}

// TestToolOutputMiddlewareNilMessages 验证 rc.Messages 为 nil 时透传不崩。
func TestToolOutputMiddlewareNilMessages(t *testing.T) {
	rc := NewRuntimeContext() // Messages nil
	chain := testChain()
	core := func(ctx context.Context, rc *RuntimeContext, in ToolCallInput) error { return nil }
	if err := chain.WrapToolCall(core)(context.Background(), rc, ToolCallInput{}); err != nil {
		t.Fatalf("nil messages: %v", err)
	}
}
