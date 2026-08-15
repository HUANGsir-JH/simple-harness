package agentsmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile 在 dir 下写 name 文件（自动建目录）。
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

// initGit 在 dir 下建 .git 目录（模拟项目根标记）。
func initGit(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, dir, ".git", "ref: refs/heads/main")
}

// TestDiscover 验证项目级指令文件发现：项目根判定、根→cwd 顺序、文件名回退。
func TestDiscover(t *testing.T) {
	t.Run("无 git 仅看 cwd", func(t *testing.T) {
		root := t.TempDir()
		sub := filepath.Join(root, "a", "b")
		writeFile(t, root, "AGENTS.md", "root doc")
		writeFile(t, sub, "AGENTS.md", "sub doc")
		got := Discover(sub)
		if len(got) != 1 || got[0] != filepath.Join(sub, "AGENTS.md") {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("git 在祖先取根到 cwd 顺序", func(t *testing.T) {
		root := t.TempDir()
		initGit(t, root)
		sub := filepath.Join(root, "a", "b")
		writeFile(t, root, "AGENTS.md", "root doc")
		writeFile(t, filepath.Join(root, "a"), "AGENTS.md", "mid doc")
		writeFile(t, sub, "AGENTS.md", "sub doc")
		got := Discover(sub)
		want := []string{
			filepath.Join(root, "AGENTS.md"),
			filepath.Join(root, "a", "AGENTS.md"),
			filepath.Join(sub, "AGENTS.md"),
		}
		if len(got) != len(want) {
			t.Fatalf("got %d files %v, want %d", len(got), got, len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("[%d] got %q want %q", i, got[i], want[i])
			}
		}
	})

	t.Run("嵌套 git 取最近", func(t *testing.T) {
		root := t.TempDir()
		initGit(t, root)
		inner := filepath.Join(root, "inner")
		initGit(t, inner)
		writeFile(t, root, "AGENTS.md", "outer doc")
		writeFile(t, inner, "AGENTS.md", "inner doc")
		got := Discover(inner)
		if len(got) != 1 || got[0] != filepath.Join(inner, "AGENTS.md") {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("AGENTS.md 优先 CLAUDE.md", func(t *testing.T) {
		root := t.TempDir()
		initGit(t, root)
		writeFile(t, root, "AGENTS.md", "agents")
		writeFile(t, root, "CLAUDE.md", "claude")
		got := Discover(root)
		if len(got) != 1 || got[0] != filepath.Join(root, "AGENTS.md") {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("仅 CLAUDE.md 回退", func(t *testing.T) {
		root := t.TempDir()
		initGit(t, root)
		writeFile(t, root, "CLAUDE.md", "claude")
		got := Discover(root)
		if len(got) != 1 || got[0] != filepath.Join(root, "CLAUDE.md") {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("无文件返回空", func(t *testing.T) {
		root := t.TempDir()
		initGit(t, root)
		if got := Discover(root); got != nil {
			t.Fatalf("got %v want nil", got)
		}
	})
}

// TestCompose 验证拼接：全局+项目顺序、空白跳过、预算截断、读失败跳过。
func TestCompose(t *testing.T) {
	t.Run("全局在前项目在后", func(t *testing.T) {
		root := t.TempDir()
		initGit(t, root)
		global := writeFile(t, t.TempDir(), "agents.md", "全局 persona")
		writeFile(t, root, "AGENTS.md", "项目 doc")
		got := Compose(Options{GlobalPath: global}, root)
		if !strings.HasPrefix(got, "全局 persona") {
			t.Fatalf("全局应在前：%q", got)
		}
		if !strings.Contains(got, "来自 AGENTS.md：\n项目 doc") {
			t.Fatalf("应含项目标注：%q", got)
		}
	})

	t.Run("仅全局", func(t *testing.T) {
		global := writeFile(t, t.TempDir(), "agents.md", "全局 persona")
		got := Compose(Options{GlobalPath: global}, t.TempDir())
		if got != "全局 persona" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("空白文件跳过", func(t *testing.T) {
		root := t.TempDir()
		initGit(t, root)
		writeFile(t, root, "AGENTS.md", "  \n\t\n")
		if got := Compose(Options{}, root); got != "" {
			t.Fatalf("got %q want 空", got)
		}
	})

	t.Run("预算截断", func(t *testing.T) {
		root := t.TempDir()
		initGit(t, root)
		writeFile(t, root, "AGENTS.md", strings.Repeat("a", 100))
		sub := filepath.Join(root, "sub")
		writeFile(t, sub, "AGENTS.md", strings.Repeat("b", 100))
		got := Compose(Options{MaxBytes: 60}, sub)
		// 预算只作用于文件内容（来源标注不计），根文件应截断为 60 字节。
		if !strings.Contains(got, strings.Repeat("a", 60)) {
			t.Fatalf("根文件应截断为 60 字节：%q", got)
		}
		if strings.Contains(got, "b") {
			t.Fatalf("预算耗尽后不应含子文件：%q", got)
		}
	})

	t.Run("读失败跳过", func(t *testing.T) {
		root := t.TempDir()
		initGit(t, root)
		// AGENTS.md 是目录（Stat 非普通文件）→ 跳过；CLAUDE.md 不存在 → 空。
		if err := os.MkdirAll(filepath.Join(root, "AGENTS.md"), 0o755); err != nil {
			t.Fatal(err)
		}
		if got := Compose(Options{}, root); got != "" {
			t.Fatalf("got %q want 空", got)
		}
	})

	t.Run("全局读失败不影响项目", func(t *testing.T) {
		root := t.TempDir()
		initGit(t, root)
		writeFile(t, root, "AGENTS.md", "项目 doc")
		got := Compose(Options{GlobalPath: filepath.Join(t.TempDir(), "missing.md")}, root)
		if !strings.Contains(got, "项目 doc") {
			t.Fatalf("got %q", got)
		}
	})
}

// TestTruncateUTF8 验证多字节 rune 不被切断。
func TestTruncateUTF8(t *testing.T) {
	s := strings.Repeat("中", 10) // 每个 3 字节，共 30 字节
	// 7 字节落在第 3 个「中」的续字节上，应回退到 6 字节（两个完整「中」）。
	if got := truncateUTF8(s, 7); got != "中中" {
		t.Fatalf("truncateUTF8(7) = %q, want 中中", got)
	}
	if got := truncateUTF8(s, 9); got != "中中中" {
		t.Fatalf("truncateUTF8(9) = %q, want 中中中", got)
	}
	if got := truncateUTF8(s, 30); got != s {
		t.Fatalf("truncateUTF8(30) 不应截断，got %q", got)
	}
}
