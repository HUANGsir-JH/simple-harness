package middleware

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/agent-project/harness/internal/provider"
)

// recordingMiddleware 记录洋葱调用顺序（before → next → after）。
type recordingMiddleware struct {
	Base
	name string
	log  *[]string
}

func (m recordingMiddleware) OnAgent(ctx context.Context, rc *RuntimeContext, in AgentInput, next func(context.Context, *RuntimeContext, AgentInput) error) error {
	*m.log = append(*m.log, m.name+":before")
	err := next(ctx, rc, in)
	*m.log = append(*m.log, m.name+":after")
	return err
}

func (m recordingMiddleware) OnActing(ctx context.Context, rc *RuntimeContext, in ActingInput, next func(context.Context, *RuntimeContext, ActingInput) error) error {
	*m.log = append(*m.log, m.name+":acting-before")
	err := next(ctx, rc, in)
	*m.log = append(*m.log, m.name+":acting-after")
	return err
}

// TestChainOnionOrder 验证洋葱包裹顺序：注册顺序外层先 before、后 after。
func TestChainOnionOrder(t *testing.T) {
	var log []string
	c := NewChain(
		recordingMiddleware{name: "m1", log: &log},
		recordingMiddleware{name: "m2", log: &log},
	)
	wrapped := c.WrapAgent(func(context.Context, *RuntimeContext, AgentInput) error {
		log = append(log, "core")
		return nil
	})
	if err := wrapped(context.Background(), NewRuntimeContext(), AgentInput{}); err != nil {
		t.Fatalf("wrapped: %v", err)
	}
	want := []string{"m1:before", "m2:before", "core", "m2:after", "m1:after"}
	if !reflect.DeepEqual(log, want) {
		t.Errorf("order: got %v want %v", log, want)
	}
}

// TestChainWrapActing 验证 acting hook 同样洋葱（before/after 两段）。
func TestChainWrapActing(t *testing.T) {
	var log []string
	c := NewChain(recordingMiddleware{name: "m1", log: &log})
	wrapped := c.WrapActing(func(context.Context, *RuntimeContext, ActingInput) error {
		log = append(log, "run-tool")
		return nil
	})
	if err := wrapped(context.Background(), NewRuntimeContext(), ActingInput{}); err != nil {
		t.Fatalf("wrapped: %v", err)
	}
	want := []string{"m1:acting-before", "run-tool", "m1:acting-after"}
	if !reflect.DeepEqual(log, want) {
		t.Errorf("order: got %v want %v", log, want)
	}
}

// appendMiddleware 是 transformer middleware（onSystemPrompt 追加文本）。
type appendMiddleware struct {
	Base
	text string
}

func (m appendMiddleware) OnSystemPrompt(_ context.Context, _ *RuntimeContext, current string) (string, error) {
	return current + m.text, nil
}

// TestChainComposeSystemPrompt 验证 transformer pipeline 从左到右。
func TestChainComposeSystemPrompt(t *testing.T) {
	c := NewChain(appendMiddleware{text: "A"}, appendMiddleware{text: "B"})
	got, err := c.ComposeSystemPrompt(context.Background(), NewRuntimeContext(), "base")
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if got != "baseAB" {
		t.Errorf("prompt: got %q want baseAB", got)
	}
}

// TestToolInstructions 验证工具说明注入（工具列表 + apply_patch 语法）。
func TestToolInstructions(t *testing.T) {
	m := ToolInstructionsMiddleware{Tools: []provider.ToolSpec{
		{Name: "read_file", Description: "读文件"},
		{Name: "apply_patch", Description: "应用补丁"},
	}}
	got, err := m.OnSystemPrompt(context.Background(), NewRuntimeContext(), "You are a helpful coding agent.")
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if !strings.Contains(got, "You are a helpful coding agent.") {
		t.Error("应保留基础指令")
	}
	if !strings.Contains(got, "- read_file: 读文件") {
		t.Error("应包含工具列表")
	}
	if !strings.Contains(got, "*** Begin Patch") || !strings.Contains(got, "*** End Patch") {
		t.Error("应包含 apply_patch 语法")
	}
}

// TestRuntimeContext 验证 per-call 上下文自由键值。
func TestRuntimeContext(t *testing.T) {
	rc := NewRuntimeContext()
	rc.SessionID = "s1"
	rc.Set("k", "v")
	if rc.Get("k") != "v" {
		t.Errorf("get: got %v want v", rc.Get("k"))
	}
	if rc.Get("missing") != nil {
		t.Error("missing key should be nil")
	}
}

// TestBasePassthrough 验证 Base 空实现透传 next 且不改提示词。
func TestBasePassthrough(t *testing.T) {
	b := Base{}
	called := false
	if err := b.OnAgent(context.Background(), nil, AgentInput{}, func(context.Context, *RuntimeContext, AgentInput) error {
		called = true
		return nil
	}); err != nil || !called {
		t.Errorf("OnAgent passthrough: called=%v err=%v", called, err)
	}
	if got, _ := b.OnSystemPrompt(context.Background(), nil, "x"); got != "x" {
		t.Errorf("OnSystemPrompt: got %q", got)
	}
}
