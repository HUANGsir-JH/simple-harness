package ui

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/agent-project/harness/internal/events"
	"github.com/agent-project/harness/internal/messages"
)

// TestJSONRendererEmitsUsage 验证 JSON 渲染器透出 EventUsage（评测轨迹
// 成本统计依赖 usage 进 --json 流；2026-08-20 补）。
func TestJSONRendererEmitsUsage(t *testing.T) {
	ev := events.Event{Type: events.EventUsage, Usage: &messages.Usage{
		InputTokens: 100, OutputTokens: 50,
		CacheReadInputTokens: 10, CacheCreationInputTokens: 5,
	}}
	out := captureStdout(t, func() {
		JSONRenderer{}.Event(ev)
	})
	if !strings.Contains(out, `"type":"usage"`) || !strings.Contains(out, `"input_tokens":100`) {
		t.Errorf("JSONRenderer 应输出 usage 事件，got %q", out)
	}

	// 全零 usage 不发（与 agent 层 IsZero 过滤一致）。
	zero := events.Event{Type: events.EventUsage, Usage: &messages.Usage{}}
	out = captureStdout(t, func() {
		JSONRenderer{}.Event(zero)
	})
	if out != "" {
		t.Errorf("全零 usage 不应输出，got %q", out)
	}
}

// TestRenderersIgnoreEventNotice 验证审查 06（2026-08-14）：run 单轮模式的
// 路径 A（BackgroundCompletionMiddleware 经 rc.Emit 推 EventNotice）会经
// onEvent 双转发到达渲染器——text/json 渲染器均未注册该类型，静默忽略：
// 无输出、不 panic（run 模式通知可见性仅靠 transcript user 行，ADR-040 已
// 知局限；本测试锚定"忽略"而非"渲染"是刻意行为）。
func TestRenderersIgnoreEventNotice(t *testing.T) {
	ev := events.Event{Type: events.EventNotice, Text: "（系统通知：后台进程 42 已退出）"}

	// 文本渲染器：无输出。
	out := captureStdout(t, func() {
		NewTextRenderer(true).Event(ev)
	})
	if out != "" {
		t.Errorf("TextRenderer 应忽略 EventNotice（无输出），got %q", out)
	}

	// JSON 渲染器：无输出。
	out = captureStdout(t, func() {
		JSONRenderer{}.Event(ev)
	})
	if out != "" {
		t.Errorf("JSONRenderer 应忽略 EventNotice（无输出），got %q", out)
	}
}

// captureStdout 捕获 fn 期间写到 stdout 的全部内容（串行测试用）。
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	w.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}
