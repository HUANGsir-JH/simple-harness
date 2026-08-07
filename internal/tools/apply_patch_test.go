package tools

import (
	"os"
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

// TestParseHunks 验证 @@ 分段与空行分段。
func TestParseHunks(t *testing.T) {
	lines := []string{"@@ a", "-one", "+ONE", "", "-two", "+TWO"}
	hunks := parseHunks(lines)
	if len(hunks) != 2 {
		t.Fatalf("hunks: got %d want 2", len(hunks))
	}
	if len(hunks[0]) != 2 || len(hunks[1]) != 2 {
		t.Errorf("hunk sizes: %d %d", len(hunks[0]), len(hunks[1]))
	}
	if hunks[0][0].kind != '-' || hunks[0][0].text != "one" {
		t.Errorf("hunk0 line0: %+v", hunks[0][0])
	}
}
