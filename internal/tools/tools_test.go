package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/provider"
)

// fakeTool 是测试用工具：可指定 name/spec/错误行为。
type fakeTool struct {
	name string
	spec provider.ToolSpec
	err  error
}

func (f *fakeTool) Name() string { return f.name }
func (f *fakeTool) Spec() provider.ToolSpec {
	if f.spec.Name != "" {
		return f.spec
	}
	return provider.ToolSpec{Name: f.name, Description: "test tool", Parameters: json.RawMessage(`{"type":"object"}`)}
}
func (f *fakeTool) Handle(_ context.Context, _ *middleware.RuntimeContext, _ string, _ json.RawMessage) (messages.ToolResult, error) {
	if f.err != nil {
		return messages.ToolResult{}, f.err
	}
	return messages.ToolResult{Success: true, Content: "ok"}, nil
}

func newFake(name string) *fakeTool { return &fakeTool{name: name} }

// TestRegistrySpecsOrder 验证 Specs 按注册顺序返回（模型可见顺序稳定）。
func TestRegistrySpecsOrder(t *testing.T) {
	r := NewRegistry()
	for _, n := range []string{"b", "a", "c"} {
		if err := r.Register(newFake(n)); err != nil {
			t.Fatalf("register %s: %v", n, err)
		}
	}
	specs := r.Specs()
	if len(specs) != 3 {
		t.Fatalf("specs: got %d want 3", len(specs))
	}
	var names []string
	for _, s := range specs {
		names = append(names, s.Name)
	}
	if got := strings.Join(names, ","); got != "b,a,c" {
		t.Errorf("order: got %s want b,a,c", got)
	}
}

// TestRegistryRegisterDuplicate 验证重名注册报错。
func TestRegistryRegisterDuplicate(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(newFake("x")); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := r.Register(newFake("x")); err == nil {
		t.Fatal("expected duplicate registration error")
	}
}

// TestRegistryRegisterInvalid 验证 nil / 空名注册报错。
func TestRegistryRegisterInvalid(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(nil); err == nil {
		t.Fatal("expected nil tool error")
	}
	if err := r.Register(newFake("")); err == nil {
		t.Fatal("expected empty name error")
	}
}

// TestRegistryGet 验证按名查找。
func TestRegistryGet(t *testing.T) {
	r := NewRegistry()
	r.Register(newFake("read_file"))
	if _, ok := r.Get("read_file"); !ok {
		t.Error("expected read_file to be found")
	}
	if _, ok := r.Get("nope"); ok {
		t.Error("expected nope not to be found")
	}
	if !r.Has("read_file") || r.Has("nope") {
		t.Error("Has mismatch")
	}
}

// TestToolErrorSemantics 验证错误二分类字段。
func TestToolErrorSemantics(t *testing.T) {
	respond := &ToolError{RespondToModel: true, Message: "recoverable"}
	if !respond.RespondToModel {
		t.Error("expected RespondToModel=true")
	}
	if respond.Error() != "recoverable" {
		t.Errorf("error message: got %q", respond.Error())
	}
	fatal := &ToolError{RespondToModel: false, Message: "fatal"}
	if fatal.RespondToModel {
		t.Error("expected RespondToModel=false")
	}
	var te *ToolError
	if !errors.As(fatal, &te) {
		t.Error("ToolError should be usable with errors.As")
	}
}

// TestHandleErrorPath 验证工具 Handle 返回错误时由注册表透传（agent 层处理二分类）。
func TestHandleErrorPath(t *testing.T) {
	tool := newFake("boom")
	tool.err = &ToolError{RespondToModel: true, Message: "cmd failed"}
	r := NewRegistry()
	r.Register(tool)

	got, ok := r.Get("boom")
	if !ok {
		t.Fatal("expected boom tool")
	}
	_, err := got.Handle(context.Background(), nil, "c1", json.RawMessage(`{}`))
	var te *ToolError
	if !errors.As(err, &te) || !te.RespondToModel {
		t.Errorf("expected RespondToModel ToolError, got %v", err)
	}
}
