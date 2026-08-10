package tools

import (
	"os"
	"strings"
	"testing"
)

func TestApplyPatchAdd(t *testing.T) {
	t.Chdir(t.TempDir())
	patch := `*** Begin Patch
*** Add File: hello.txt
+Hello world
*** End Patch`
	r, err := call(ApplyPatchTool{}, map[string]any{"patch": patch})
	if err != nil || !r.Success {
		t.Fatalf("add: %v %v", r, err)
	}
	data, err := os.ReadFile("hello.txt")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "Hello world\n" {
		t.Errorf("content: got %q", string(data))
	}
}

func TestApplyPatchUpdate(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("a.txt", []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := `*** Begin Patch
*** Update File: a.txt
@@
-one
+ONE
*** End Patch`
	r, err := call(ApplyPatchTool{}, map[string]any{"patch": patch})
	if err != nil || !r.Success {
		t.Fatalf("update: %v %v", r, err)
	}
	data, _ := os.ReadFile("a.txt")
	if string(data) != "ONE\ntwo\nthree\n" {
		t.Errorf("content: got %q", string(data))
	}
}

func TestApplyPatchUpdateMultipleHunks(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("a.txt", []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := `*** Begin Patch
*** Update File: a.txt
@@
-one
+ONE
@@
-three
+THREE
*** End Patch`
	r, err := call(ApplyPatchTool{}, map[string]any{"patch": patch})
	if err != nil || !r.Success {
		t.Fatalf("update: %v %v", r, err)
	}
	data, _ := os.ReadFile("a.txt")
	if string(data) != "ONE\ntwo\nTHREE\n" {
		t.Errorf("content: got %q", string(data))
	}
}

func TestApplyPatchDelete(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("d.txt", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := `*** Begin Patch
*** Delete File: d.txt
*** End Patch`
	r, err := call(ApplyPatchTool{}, map[string]any{"patch": patch})
	if err != nil || !r.Success {
		t.Fatalf("delete: %v %v", r, err)
	}
	if _, err := os.Stat("d.txt"); !os.IsNotExist(err) {
		t.Errorf("d.txt should be deleted, stat err=%v", err)
	}
}

func TestApplyPatchCombined(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("a.txt", []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("gone.txt", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := `*** Begin Patch
*** Update File: a.txt
-old
+new
*** Add File: b.txt
+B file
*** Delete File: gone.txt
*** End Patch`
	r, err := call(ApplyPatchTool{}, map[string]any{"patch": patch})
	if err != nil || !r.Success {
		t.Fatalf("combined: %v %v", r, err)
	}
	if data, _ := os.ReadFile("a.txt"); string(data) != "new\n" {
		t.Errorf("a.txt: got %q", string(data))
	}
	if data, _ := os.ReadFile("b.txt"); string(data) != "B file\n" {
		t.Errorf("b.txt: got %q", string(data))
	}
}

// TestApplyPatchParseError 验证缺 Begin/End 信封报错（RespondToModel）。
func TestApplyPatchParseError(t *testing.T) {
	_, err := call(ApplyPatchTool{}, map[string]any{"patch": "no envelope here"})
	wantRespondToModel(t, err, "parse")
}

// TestApplyPatchHunkMismatch 验证 hunk 未匹配时报错（RespondToModel）。
func TestApplyPatchHunkMismatch(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("a.txt", []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := `*** Begin Patch
*** Update File: a.txt
-nope
+new
*** End Patch`
	_, err := call(ApplyPatchTool{}, map[string]any{"patch": patch})
	wantRespondToModel(t, err, "hunk mismatch")
}

// TestParseChunks 验证 @@ 分段、定位锚点与空行分段。
func TestParseChunks(t *testing.T) {
	lines := []string{"@@ func a", "-one", "+ONE", "", "-two", "+TWO"}
	chunks := parseChunks(lines)
	if len(chunks) != 2 {
		t.Fatalf("chunks: got %d want 2", len(chunks))
	}
	if chunks[0].anchor != "func a" {
		t.Errorf("chunk0 anchor: got %q, want %q", chunks[0].anchor, "func a")
	}
	if len(chunks[0].oldLines) != 1 || chunks[0].oldLines[0] != "one" {
		t.Errorf("chunk0 oldLines: %v", chunks[0].oldLines)
	}
	if len(chunks[1].oldLines) != 1 || chunks[1].oldLines[0] != "two" {
		t.Errorf("chunk1 oldLines: %v", chunks[1].oldLines)
	}
}

// TestApplyPatchAmbiguous 验证无 @@ 定位且多处匹配时拒绝并回填候选行号
// （Bug07(a)：不静默错改——两个同构函数，模型想改哪处工具不知道）。
func TestApplyPatchAmbiguous(t *testing.T) {
	t.Chdir(t.TempDir())
	content := "func first() error {\n    return err\n}\n\nfunc second() error {\n    return err\n}\n"
	if err := os.WriteFile("a.go", []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := `*** Begin Patch
*** Update File: a.go
-    return err
+    return fmt.Errorf("wrapped: %w", err)
*** End Patch`
	_, err := call(ApplyPatchTool{}, map[string]any{"patch": patch})
	wantRespondToModel(t, err, "ambiguous")
	if !strings.Contains(err.Error(), "多处出现") {
		t.Errorf("错误应说明多处匹配，got: %v", err)
	}
}

// TestApplyPatchAnchor 验证 @@ 定位文本消歧：改第二个同构函数（Bug07(a)，
// 对齐 codex context 定位）。
func TestApplyPatchAnchor(t *testing.T) {
	t.Chdir(t.TempDir())
	content := "func first() error {\n    return err\n}\n\nfunc second() error {\n    return err\n}\n"
	if err := os.WriteFile("a.go", []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := `*** Begin Patch
*** Update File: a.go
@@ func second
-    return err
+    return fmt.Errorf("second: %w", err)
*** End Patch`
	r, err := call(ApplyPatchTool{}, map[string]any{"patch": patch})
	if err != nil || !r.Success {
		t.Fatalf("anchor: %v %v", r, err)
	}
	data, _ := os.ReadFile("a.go")
	want := "func first() error {\n    return err\n}\n\nfunc second() error {\n    return fmt.Errorf(\"second: %w\", err)\n}\n"
	if string(data) != want {
		t.Errorf("content:\n%s", data)
	}
}

// TestApplyPatchFuzzyMatch 验证模糊匹配（行尾空白差异不阻塞补丁，对齐 codex
// seek_sequence 4 级严格度）。
func TestApplyPatchFuzzyMatch(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("a.txt", []byte("foo   \nbar\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := `*** Begin Patch
*** Update File: a.txt
-foo
+FOO
*** End Patch`
	r, err := call(ApplyPatchTool{}, map[string]any{"patch": patch})
	if err != nil || !r.Success {
		t.Fatalf("fuzzy: %v %v", r, err)
	}
	data, _ := os.ReadFile("a.txt")
	if string(data) != "FOO\nbar\n" {
		t.Errorf("content: %q", data)
	}
}

// TestApplyPatchAtomic 验证两阶段事务：补丁中任一操作失败 → 整体不落盘
// （Bug07(b)：Add 不残留，模型重试不会撞"文件已存在"）。
func TestApplyPatchAtomic(t *testing.T) {
	t.Chdir(t.TempDir())
	patch := `*** Begin Patch
*** Add File: new.txt
+created
*** Update File: does-not-exist.txt
-old
+new
*** End Patch`
	_, err := call(ApplyPatchTool{}, map[string]any{"patch": patch})
	wantRespondToModel(t, err, "atomic")
	if _, serr := os.Stat("new.txt"); !os.IsNotExist(serr) {
		t.Errorf("两阶段失败时 Add 不应落盘，new.txt 存在")
	}
}

// TestApplyPatchEndOfFile 验证 *** End of File 标记：多匹配时取文件尾（对齐
// codex eof 优先）。
func TestApplyPatchEndOfFile(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("a.txt", []byte("x\nx\ny\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := `*** Begin Patch
*** Update File: a.txt
*** End of File
-x
+X
*** End Patch`
	r, err := call(ApplyPatchTool{}, map[string]any{"patch": patch})
	if err != nil || !r.Success {
		t.Fatalf("eof: %v %v", r, err)
	}
	data, _ := os.ReadFile("a.txt")
	if string(data) != "x\nX\ny\n" {
		t.Errorf("content: %q", data)
	}
}
