package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/provider"
)

// textStream 构造一个事件流：先发出给定文本增量，再 done。
func textStream(parts ...string) provider.EventStream {
	var evs []provider.Event
	for _, p := range parts {
		evs = append(evs, provider.Event{Type: provider.EventTextDelta, Text: p})
	}
	evs = append(evs, provider.Event{Type: provider.EventDone})
	return provider.NewFakeStream(evs)
}

// TestRunOnceText 验证 RunOnce 将增量拼装为单条消息。
func TestRunOnceText(t *testing.T) {
	fc := &provider.FakeClient{
		StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
			return textStream("Hel", "lo ", "world"), nil
		},
	}
	a := New(fc, "test-model")

	th := messages.NewThread()
	th.Add(messages.NewUserMessage("hi"))

	var deltas []string
	msg, err := a.RunOnce(context.Background(), th, func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if msg.Role != messages.RoleAssistant {
		t.Errorf("role: got %q", msg.Role)
	}
	if msg.Content != "Hello world" {
		t.Errorf("content: got %q", msg.Content)
	}
	if len(deltas) != 3 || strings.Join(deltas, "") != "Hello world" {
		t.Errorf("deltas: got %v", deltas)
	}
	// 请求必须携带了 thread 消息。
	if fc.LastReq == nil || len(fc.LastReq.Messages) != 1 {
		t.Fatalf("request messages: %+v", fc.LastReq)
	}
}

// TestRunOnceEmptyStream 验证空流仍产生一条助手消息（内容为空）。
func TestRunOnceEmptyStream(t *testing.T) {
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return provider.NewFakeStream(nil), nil
	}}
	a := New(fc, "m")
	th := messages.NewThread()
	th.Add(messages.NewUserMessage("hi"))

	msg, err := a.RunOnce(context.Background(), th, nil)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if msg.Content != "" {
		t.Errorf("content: got %q want empty", msg.Content)
	}
}

// TestRunOnceStreamError 验证流中错误会中止并传播。
func TestRunOnceStreamError(t *testing.T) {
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return provider.NewFakeStream([]provider.Event{
			{Type: provider.EventTextDelta, Text: "partial"},
			{Type: provider.EventError, Error: errors.New("boom")},
		}), nil
	}}
	a := New(fc, "m")
	th := messages.NewThread()
	th.Add(messages.NewUserMessage("hi"))

	_, err := a.RunOnce(context.Background(), th, nil)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("expected boom error, got %v", err)
	}
}

// TestRunOnceStartError 验证启动失败会传播。
func TestRunOnceStartError(t *testing.T) {
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return nil, errors.New("start failed")
	}}
	a := New(fc, "m")
	th := messages.NewThread()
	th.Add(messages.NewUserMessage("hi"))

	if _, err := a.RunOnce(context.Background(), th, nil); err == nil {
		t.Fatal("expected error")
	}
}

// TestRunOnceToolCallIgnored 验证阶段一行为：tool call 事件被容忍（忽略），
// 回合正常结束。
func TestRunOnceToolCallIgnored(t *testing.T) {
	fc := &provider.FakeClient{StreamFn: func(ctx context.Context, req provider.Request) (provider.EventStream, error) {
		return provider.NewFakeStream([]provider.Event{
			{Type: provider.EventToolCall, ToolCall: &messages.ToolCall{ID: "c1", Name: "read_file"}},
			{Type: provider.EventTextDelta, Text: "still works"},
			{Type: provider.EventDone},
		}), nil
	}}
	a := New(fc, "m")
	th := messages.NewThread()
	th.Add(messages.NewUserMessage("hi"))

	msg, err := a.RunOnce(context.Background(), th, nil)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if msg.Content != "still works" {
		t.Errorf("content: got %q", msg.Content)
	}
}
