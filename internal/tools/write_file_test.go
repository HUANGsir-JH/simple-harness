package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileCreate(t *testing.T) {
	t.Chdir(t.TempDir())
	r, err := call(WriteFileTool{}, map[string]any{"path": "new.txt", "content": "hello\nworld"})
	if err != nil || !r.Success {
		t.Fatalf("write: %v %v", r, err)
	}
	data, err := os.ReadFile("new.txt")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "hello\nworld" {
		t.Errorf("content: got %q", string(data))
	}
}

// TestWriteFileOverwrite 验证覆盖已有文件。
func TestWriteFileOverwrite(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("a.txt", []byte("old content"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := call(WriteFileTool{}, map[string]any{"path": "a.txt", "content": "new content"})
	if err != nil || !r.Success {
		t.Fatalf("write: %v %v", r, err)
	}
	data, _ := os.ReadFile("a.txt")
	if string(data) != "new content" {
		t.Errorf("content: got %q", string(data))
	}
}

// TestWriteFileCreateParentDir 验证自动创建父目录。
func TestWriteFileCreateParentDir(t *testing.T) {
	t.Chdir(t.TempDir())
	r, err := call(WriteFileTool{}, map[string]any{"path": filepath.Join("sub", "nested", "f.txt"), "content": "x"})
	if err != nil || !r.Success {
		t.Fatalf("write: %v %v", r, err)
	}
	if _, err := os.Stat(filepath.Join("sub", "nested", "f.txt")); err != nil {
		t.Errorf("文件未创建到父目录: %v", err)
	}
}

// TestWriteFileMissingArgs 验证缺 path/content 报错（RespondToModel）。
func TestWriteFileMissingArgs(t *testing.T) {
	_, err := call(WriteFileTool{}, map[string]any{})
	wantRespondToModel(t, err, "missing args")
	_, err = call(WriteFileTool{}, map[string]any{"content": "x"})
	wantRespondToModel(t, err, "missing path")
}
