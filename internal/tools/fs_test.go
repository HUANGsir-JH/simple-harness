package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-project/harness/internal/messages"
)

// call 用给定参数执行一次工具调用（测试辅助）。
func call(t Tool, args map[string]any) (messages.ToolResult, error) {
	b, err := json.Marshal(args)
	if err != nil {
		panic(err)
	}
	return t.Handle(context.Background(), nil, "c1", b)
}

// wantRespondToModel 断言错误是 RespondToModel 的 ToolError。
func wantRespondToModel(t *testing.T, err error, prefix string) {
	t.Helper()
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("%s: expected ToolError, got %v", prefix, err)
	}
	if !te.RespondToModel {
		t.Errorf("%s: expected RespondToModel, got %v", prefix, err)
	}
}

func TestReadFileTool(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(p, []byte("l1\nl2\nl3\nl4\nl5\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 全量读取
	r, err := call(ReadFileTool{}, map[string]any{"path": p})
	if err != nil || !r.Success {
		t.Fatalf("read: %v %v", r, err)
	}
	if r.Content != "l1\nl2\nl3\nl4\nl5\n" {
		t.Errorf("content: got %q", r.Content)
	}

	// 行范围（2-3）
	r, err = call(ReadFileTool{}, map[string]any{"path": p, "start_line": 2, "end_line": 3})
	if err != nil || r.Content != "l2\nl3" {
		t.Errorf("range: got %q err=%v", r.Content, err)
	}

	// 文件不存在 → RespondToModel
	_, err = call(ReadFileTool{}, map[string]any{"path": filepath.Join(dir, "nope.txt")})
	wantRespondToModel(t, err, "missing file")

	// 参数错误
	_, err = call(ReadFileTool{}, map[string]any{})
	wantRespondToModel(t, err, "no path")
}

// TestReadFileLargeFile 验证超大文件未指定行范围时提示分段（防一次读爆
// 上下文）；指定范围则正常读取（read_file 豁免 evict，ADR-028）。
func TestReadFileLargeFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.txt")
	big := strings.Repeat("x", MaxReadFileBytes+100) + "\n"
	if err := os.WriteFile(p, []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}

	// 未指定范围 → 提示分段（RespondToModel，模型可修正重试）。
	_, err := call(ReadFileTool{}, map[string]any{"path": p})
	wantRespondToModel(t, err, "too large")
	if !strings.Contains(err.Error(), "start_line/end_line") {
		t.Errorf("error should hint line ranges: %v", err)
	}

	// 指定行范围 → 正常读取（超大文件的指定行）。
	r, err := call(ReadFileTool{}, map[string]any{"path": p, "start_line": 1, "end_line": 1})
	if err != nil || !r.Success {
		t.Fatalf("range read on big file: %v %v", r, err)
	}
	if len(r.Content) <= MaxReadFileBytes {
		t.Errorf("range read content length: got %d", len(r.Content))
	}
}

func TestListDirTool(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644)
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)

	r, err := call(ListDirTool{}, map[string]any{"path": dir})
	if err != nil || !r.Success {
		t.Fatalf("list: %v %v", r, err)
	}
	if !strings.Contains(r.Content, "file\ta.txt") {
		t.Errorf("content missing file entry: %q", r.Content)
	}
	if !strings.Contains(r.Content, "dir\tsub") {
		t.Errorf("content missing dir entry: %q", r.Content)
	}
}

func TestGlobTool(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "x.go"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "x_test.go"), []byte("x"), 0o644)

	r, err := call(GlobTool{}, map[string]any{"pattern": filepath.Join(dir, "*.go")})
	if err != nil || !r.Success {
		t.Fatalf("glob: %v %v", r, err)
	}
	if !strings.Contains(r.Content, "x.go") || !strings.Contains(r.Content, "x_test.go") {
		t.Errorf("content: got %q", r.Content)
	}
}
