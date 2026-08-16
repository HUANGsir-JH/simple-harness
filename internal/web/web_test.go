package web

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-project/harness/internal/messages"
)

// --- Hub 测试 ---------------------------------------------------------------

// TestHubSubscribeBroadcast 验证订阅后能收到广播事件、退订后不再收到。
func TestHubSubscribeBroadcast(t *testing.T) {
	h := NewHub()
	ch, unsub := h.Subscribe()
	defer unsub()

	h.Broadcast("agent", map[string]any{"text": "hello"})
	select {
	case msg := <-ch:
		if !strings.Contains(string(msg), "event: agent") || !strings.Contains(string(msg), `"text":"hello"`) {
			t.Errorf("广播内容: %s", msg)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("未收到广播")
	}

	unsub()
	h.Broadcast("agent", map[string]any{"text": "after-unsub"})
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("退订后仍收到")
		}
		// ok=false = 通道已关闭（退订语义），正确
	case <-time.After(100 * time.Millisecond):
		t.Error("退订后通道未关闭")
	}
}

// TestHubLen 验证订阅者计数（approver 无订阅者自释放判定用）。
func TestHubLen(t *testing.T) {
	h := NewHub()
	if h.Len() != 0 {
		t.Fatalf("初始 Len = %d", h.Len())
	}
	ch, unsub := h.Subscribe()
	_ = ch
	if h.Len() != 1 {
		t.Fatalf("订阅后 Len = %d", h.Len())
	}
	unsub()
	if h.Len() != 0 {
		t.Fatalf("退订后 Len = %d", h.Len())
	}
}

// TestHubClose 验证 Close 后全部订阅通道关闭（SSE 长连接断开；Server 收尾）。
func TestHubClose(t *testing.T) {
	h := NewHub()
	ch, unsub := h.Subscribe()
	defer unsub()
	h.Close()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("Close 后通道应关闭")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Close 后通道未关闭")
	}
}

// TestHubConcurrentBroadcast 验证并发广播不 panic、事件不丢失（-race 守护）。
func TestHubConcurrentBroadcast(t *testing.T) {
	h := NewHub()
	const subs = 4
	var wg sync.WaitGroup
	chs := make([]<-chan []byte, subs)
	unsubs := make([]func(), subs)
	for i := 0; i < subs; i++ {
		ch, unsub := h.Subscribe()
		chs[i], unsubs[i] = ch, unsub
		defer unsub()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ { // < 通道缓冲 64，全量收到
			h.Broadcast("t", map[string]any{"n": i})
		}
	}()
	wg.Wait()
	for _, ch := range chs {
		count := 0
		for {
			select {
			case _, ok := <-ch:
				if !ok {
					t.Fatal("通道意外关闭")
				}
				count++
				if count == 50 {
					goto done
				}
			case <-time.After(200 * time.Millisecond):
				t.Fatalf("只收到 %d/50", count)
			}
		}
	done:
	}
}

// --- toolview 测试 ------------------------------------------------------------

func mustArgs(t *testing.T, s string) []byte {
	t.Helper()
	return []byte(s)
}

// TestToolCallSummary 验证按工具提取摘要。
func TestToolCallSummary(t *testing.T) {
	cases := []struct {
		name string
		args string
		want string
	}{
		{"read_file", `{"path":"a.go"}`, "read_file a.go"},
		{"write_file", `{"path":"b.txt","content":"x"}`, "write_file b.txt"},
		{"glob", `{"pattern":"*.go"}`, "glob *.go"},
		{"shell_command", `{"command":"ls -la"}`, "shell_command: ls -la"},
		{"shell_command", `{"command":"","kill_pid":42}`, "shell_command: kill 42"},
		{"shell_command", `{"command":"serve &","background":true}`, "shell_command: serve & &"},
		{"update_todo", `{}`, "update_todo"},
		{"apply_patch", `{}`, "apply_patch"},
		{"skill", `{"name":"frontend"}`, "skill frontend"},
	}
	for _, c := range cases {
		got := toolCallSummary(c.name, mustArgs(t, c.args))
		if got != c.want {
			t.Errorf("%s %s: got %q want %q", c.name, c.args, got, c.want)
		}
	}
}

// TestApplyToolResultDispatch 验证各工具分派（read_file 统计 / shell head /
// list_dir 计数 / 失败态）。
func TestApplyToolResultDispatch(t *testing.T) {
	// read_file：行数 + 大小
	v := applyToolResult(&toolCallInfo{name: "read_file", args: mustArgs(t, `{"path":"f.go"}`)},
		&messages.ToolResult{Success: true, Content: "line1\nline2\n"})
	if !strings.Contains(v.Content, "2 lines") {
		t.Errorf("read_file content: %q", v.Content)
	}

	// shell_command：exit 0 + head 5
	long := "a\nb\nc\nd\ne\nf\ng\n"
	v = applyToolResult(&toolCallInfo{name: "shell_command", args: mustArgs(t, `{"command":"x"}`)},
		&messages.ToolResult{Success: true, Content: long})
	if !strings.Contains(v.Content, "exit 0") || !strings.Contains(v.Content, "+2 lines") {
		t.Errorf("shell content: %q", v.Content)
	}

	// 失败态
	v = applyToolResult(&toolCallInfo{name: "shell_command", args: mustArgs(t, `{}`)},
		&messages.ToolResult{Success: false, Content: "command not found"})
	if !v.Failed || !strings.Contains(v.Content, "command not found") {
		t.Errorf("failed content: %+v", v)
	}

	// background：原文展示不拼 exit 0
	v = applyToolResult(&toolCallInfo{name: "shell_command", args: mustArgs(t, `{"command":"s","background":true}`)},
		&messages.ToolResult{Success: true, Content: "已后台启动 PID 123"})
	if strings.Contains(v.Content, "exit 0") || !strings.Contains(v.Content, "PID 123") {
		t.Errorf("background content: %q", v.Content)
	}
}

// TestApplyToolResultWriteFile 验证 write_file 新建/覆盖 diff。
func TestApplyToolResultWriteFile(t *testing.T) {
	// 新建（无旧文件）
	v := applyToolResult(&toolCallInfo{name: "write_file", args: mustArgs(t, `{"path":"n.txt","content":"x\ny\n"}`)},
		&messages.ToolResult{Success: true, Content: ""})
	if !strings.Contains(v.Content, "created n.txt") {
		t.Errorf("created content: %q", v.Content)
	}
	// 覆盖（有旧内容 → diff）
	v = applyToolResult(&toolCallInfo{
		name: "write_file", args: mustArgs(t, `{"path":"n.txt","content":"y\n"}`),
		oldContent: "x\n", oldExists: true,
	}, &messages.ToolResult{Success: true, Content: ""})
	if !strings.Contains(v.Content, "updated n.txt") || v.Diff == "" {
		t.Errorf("updated content: %q diff: %q", v.Content, v.Diff)
	}
	if !strings.Contains(v.Diff, "diff-add") || !strings.Contains(v.Diff, "diff-del") {
		t.Errorf("diff html 缺 +/- 高亮: %q", v.Diff)
	}
}

// TestTruncateFull 验证展开态截断（100KB）。
func TestTruncateFull(t *testing.T) {
	big := strings.Repeat("x", 200*1024)
	got := truncateFull(big)
	if len(got) >= len(big) || !strings.Contains(got, "内容过长已截断") {
		t.Errorf("truncateFull 未截断: %d -> %d", len(big), len(got))
	}
	small := "hi"
	if truncateFull(small) != small {
		t.Error("小内容不应截断")
	}
}

// --- md 测试 -------------------------------------------------------------------

// TestRenderHTML 验证 markdown → HTML（段落/代码块/高亮类）。
func TestRenderHTML(t *testing.T) {
	html := renderHTML("# 标题\n\n正文 **加粗**\n\n```go\nfunc main() {}\n```")
	if !strings.Contains(html, "<h1") {
		t.Errorf("缺标题: %s", html)
	}
	if !strings.Contains(html, "<strong>") {
		t.Errorf("缺加粗: %s", html)
	}
	if !strings.Contains(html, "code-block") {
		t.Errorf("缺代码块: %s", html)
	}
	// 转义：原始 HTML 不渲染（安全）
	safe := renderHTML("<script>alert(1)</script>")
	if strings.Contains(safe, "<script>") {
		t.Errorf("原始 HTML 未转义: %s", safe)
	}
}

// TestRenderHTMLEscape 验证内联代码转义。
func TestRenderHTMLEscape(t *testing.T) {
	html := renderHTML("`a<b`")
	if strings.Contains(html, "a<b") && !strings.Contains(html, "&lt;") {
		t.Errorf("内联代码未转义: %s", html)
	}
}

// --- state 辅助测试 -------------------------------------------------------------

// TestMsgID 验证元素 id 规则（msg-<序号>，不依赖 msg_id）。
func TestMsgID(t *testing.T) {
	if msgID(0) != "msg-0" || msgID(5) != "msg-5" {
		t.Error("msgID 格式错误")
	}
}

// TestFormatToolArgs 验证参数格式化（JSON 缩进）。
func TestFormatToolArgs(t *testing.T) {
	got := formatToolArgs(mustArgs(t, `{"a":1,"b":[1,2]}`))
	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("格式化后不是合法 JSON: %v", err)
	}
	if parsed["a"].(float64) != 1 {
		t.Errorf("parsed: %v", parsed)
	}
}
