package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-project/harness/internal/session"
)

// TestParseCommand 验证 REPL 命令解析。
func TestParseCommand(t *testing.T) {
	cases := []struct {
		line string
		ok   bool
		name string
		arg  string
	}{
		{"hello", false, "", ""},
		{"/switch abc", true, "switch", "abc"},
		{"/switch --last", true, "switch", "--last"},
		{"/model deepseek-v4-pro", true, "model", "deepseek-v4-pro"},
		{"/effort low", true, "effort", "low"},
	}
	for _, c := range cases {
		cmd, ok := parseCommand(c.line)
		if ok != c.ok || cmd.name != c.name || cmd.arg != c.arg {
			t.Errorf("parseCommand(%q): got ok=%v %+v want ok=%v name=%q arg=%q", c.line, ok, cmd, c.ok, c.name, c.arg)
		}
	}
}

// newTestReplCtx 构建带临时会话的测试 replCtx（HARNESS_HOME 隔离 + 显式配置）。
func newTestReplCtx(t *testing.T) *replCtx {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HARNESS_HOME", filepath.Join(dir, ".harness"))
	t.Chdir(dir)

	rt, err := loadRuntime(testConfig(t, basicConfig))
	if err != nil {
		t.Fatalf("loadRuntime: %v", err)
	}
	proj, err := findProject()
	if err != nil {
		t.Fatalf("findProject: %v", err)
	}
	sess, err := session.CreateInCWD(rt.Resolved.Model)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	c := &replCtx{
		rt:     rt,
		proj:   proj,
		open:   map[string]*session.Session{sess.ID: sess},
		active: sess,
	}
	t.Cleanup(c.closeAll) // 关闭所有 open 会话的 writer（含 /switch 后 resume 的）
	return c
}

// TestHandleCommandEffort 验证 /effort 切换档位并落盘到会话 state。
func TestHandleCommandEffort(t *testing.T) {
	c := newTestReplCtx(t)
	if err := c.handleCommand(replCommand{name: "effort", arg: "low"}); err != nil {
		t.Fatalf("/effort low: %v", err)
	}
	if got := c.active.State().ThinkingEffort; got != "low" {
		t.Errorf("state effort: got %q want low", got)
	}
}

// TestHandleCommandEffortInvalid 验证 /effort 不在模型 efforts 内报错。
func TestHandleCommandEffortInvalid(t *testing.T) {
	c := newTestReplCtx(t)
	err := c.handleCommand(replCommand{name: "effort", arg: "bogus"})
	if err == nil || !strings.Contains(err.Error(), "不支持") {
		t.Fatalf("expected not-supported error, got %v", err)
	}
}

// TestHandleCommandModel 验证 /model 切换模型并重置档位为模型默认。
func TestHandleCommandModel(t *testing.T) {
	c := newTestReplCtx(t)
	if err := c.handleCommand(replCommand{name: "model", arg: "m"}); err != nil {
		t.Fatalf("/model m: %v", err)
	}
	if got := c.active.Model(); got != "m" {
		t.Errorf("active model: got %q", got)
	}
	// 新模型重置 effort 为模型默认（high）。
	if got := c.active.State().ThinkingEffort; got != "high" {
		t.Errorf("state effort after /model: got %q want high", got)
	}
}

// TestHandleCommandModelInvalid 验证 /model 不存在的模型报错。
func TestHandleCommandModelInvalid(t *testing.T) {
	c := newTestReplCtx(t)
	err := c.handleCommand(replCommand{name: "model", arg: "nonexistent"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error, got %v", err)
	}
}

// TestReplCtxSwitch 验证进程内会话切换：未开会话 → resume；已开会话 → 复用。
func TestReplCtxSwitch(t *testing.T) {
	c := newTestReplCtx(t)
	firstID := c.active.ID

	// 用项目桶另建一个会话（/switch 目标），关闭其 writer 释放文件（否则与
	// resume 打开的 writer 同时持有同一文件，Windows 上会锁）。
	other, err := c.proj.Create("m")
	if err != nil {
		t.Fatalf("create other: %v", err)
	}
	if err := other.Close(); err != nil {
		t.Fatalf("close other: %v", err)
	}

	// 未开会话 → 从磁盘 resume（新实例，ID 相同），并切换。
	if err := c.switchTo(other.ID); err != nil {
		t.Fatalf("switchTo: %v", err)
	}
	if c.active.ID != other.ID {
		t.Fatal("active 未切换到新会话")
	}
	if c.active == other {
		t.Error("switchTo 应 resume 新实例而非复用已关闭对象")
	}

	// 已开会话 → 复用 open map 实例（无需重新打开），切回初始。
	if err := c.switchTo(firstID); err != nil {
		t.Fatalf("switchTo back: %v", err)
	}
	if c.active != c.open[firstID] {
		t.Fatal("active 未切回初始会话实例")
	}
}

// TestReplCtxSwitchUnknown 验证切换不存在的会话报错。
func TestReplCtxSwitchUnknown(t *testing.T) {
	c := newTestReplCtx(t)
	err := c.switchTo("nonexistent")
	if err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("expected not-exist error, got %v", err)
	}
}
