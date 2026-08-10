package tools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/middleware"
)

// TestResolvePath 验证路径解析：相对以 ws 为基、绝对原样、空回退进程 cwd。
func TestResolvePath(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "ws")
	// 相对 → join(ws)。
	if got, want := ResolvePath(ws, "a.txt"), filepath.Join(ws, "a.txt"); got != want {
		t.Errorf("rel: got %q want %q", got, want)
	}
	if got, want := ResolvePath(ws, "sub/dir/x"), filepath.Join(ws, "sub", "dir", "x"); got != want {
		t.Errorf("nested: got %q want %q", got, want)
	}
	// 绝对 → 原样 Clean。
	abs := filepath.Join(t.TempDir(), "outside.txt")
	if got := ResolvePath(ws, abs); got != abs {
		t.Errorf("abs: got %q want %q", got, abs)
	}
	// 空路径 → ws 本身（list_dir 默认当前目录）。
	if got := ResolvePath(ws, ""); got != ws {
		t.Errorf("empty: got %q want %q", got, ws)
	}
	// ws 空 → 进程 cwd（rc 传 nil 兼容）。
	wd, _ := os.Getwd()
	if got := ResolvePath("", "a.txt"); got != filepath.Join(wd, "a.txt") {
		t.Errorf("empty ws: got %q want %q", got, filepath.Join(wd, "a.txt"))
	}
	// `..` 穿越 → 词法上移（范围判定由 InWorkspace，不在 ResolvePath 拦）。
	if got := ResolvePath(ws, "../x.txt"); filepath.Dir(got) != filepath.Dir(ws) {
		t.Errorf("dotdot: got %q, want parent of %q", got, ws)
	}
}

// TestInWorkspace 验证边界判定：ws 内（含本身）true、外 false。
func TestInWorkspace(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "a.txt")
	outside := filepath.Join(root, "..", "sibling.txt")
	rootParent := filepath.Dir(root)

	cases := []struct {
		name string
		abs  string
		want bool
	}{
		{"inside", inside, true},
		{"root-itself", root, true},
		{"parent-of-root", rootParent, false},
		{"outside", outside, false},
	}
	for _, tc := range cases {
		if got := InWorkspace(root, tc.abs); got != tc.want {
			t.Errorf("%s: InWorkspace(%q, %q) = %v, want %v", tc.name, root, tc.abs, got, tc.want)
		}
	}
	// 空 ws → 进程 cwd。
	wd, _ := os.Getwd()
	if !InWorkspace("", filepath.Join(wd, "x")) {
		t.Error("empty ws should resolve to process cwd")
	}
}

// TestResolveInWorkspace 验证工具入口：rc.State.CWD 为基；nil rc 回退进程 cwd。
func TestResolveInWorkspace(t *testing.T) {
	ws := t.TempDir()
	rc := middleware.NewRuntimeContext()
	rc.State = agentstate.New("s1", "m", ws)
	if got, want := ResolveInWorkspace(rc, "a.txt"), filepath.Join(ws, "a.txt"); got != want {
		t.Errorf("with CWD: got %q want %q", got, want)
	}

	// rc 为 nil → 进程 cwd（既有测试传 nil rc 的行为不变）。
	wd, _ := os.Getwd()
	if got, want := ResolveInWorkspace(nil, "a.txt"), filepath.Join(wd, "a.txt"); got != want {
		t.Errorf("nil rc: got %q want %q", got, want)
	}
}

// TestToolWorkspaceResolution 验证文件工具相对路径以 state.CWD 为基解析
// （state.CWD 死字段复活，Bug03）：read_file 传相对路径读到会话目录下的文件，
// write_file 相对路径写入会话目录（而非进程 cwd）。
func TestToolWorkspaceResolution(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	rc := middleware.NewRuntimeContext()
	rc.State = agentstate.New("s1", "m", ws)

	// read_file 相对路径 → 命中 ws/a.txt。
	r, err := callWithRC(ReadFileTool{}, rc, map[string]any{"path": "a.txt"})
	if err != nil || !r.Success || r.Content != "hello" {
		t.Fatalf("read rel: %v %v", r, err)
	}

	// write_file 相对路径 → 写入 ws/out.txt（内容返回绝对路径）。
	r, err = callWithRC(WriteFileTool{}, rc, map[string]any{"path": "out.txt", "content": "x"})
	if err != nil || !r.Success {
		t.Fatalf("write rel: %v %v", r, err)
	}
	if want := filepath.Join(ws, "out.txt"); r.Content != "Write File: "+want {
		t.Errorf("write content: got %q want %q", r.Content, "Write File: "+want)
	}
	if _, err := os.Stat(filepath.Join(ws, "out.txt")); err != nil {
		t.Errorf("write should land in workspace: %v", err)
	}
}
