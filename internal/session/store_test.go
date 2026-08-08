package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureDirs(t *testing.T) {
	root := t.TempDir()
	s := NewAt(root)
	if err := s.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	for _, d := range []string{DirWorkspaces, DirSubagents, DirMemory, DirLogs} {
		if fi, err := os.Stat(filepath.Join(root, d)); err != nil || !fi.IsDir() {
			t.Errorf("%s 目录未创建", d)
		}
	}
	if _, err := os.Stat(filepath.Join(root, FileAgentsMD)); err != nil {
		t.Errorf("agents.md 占位未创建: %v", err)
	}
	// 再次调用不报错（占位已存在跳过）。
	if err := s.EnsureDirs(); err != nil {
		t.Errorf("重复 EnsureDirs: %v", err)
	}
}

func TestProjectDir(t *testing.T) {
	root := t.TempDir()
	s := NewAt(root)
	got := s.ProjectDir(`D:\agent-project\harness`)
	want := filepath.Join(root, DirWorkspaces, "D__agent-project_harness")
	if got != want {
		t.Errorf("ProjectDir = %q, want %q", got, want)
	}
}

func TestFindProject(t *testing.T) {
	root := t.TempDir()
	s := NewAt(root)

	// 预先建桶（模拟已存在项目）。
	projPath := filepath.Join(root, "proj")
	bucket := s.ProjectDir(projPath)
	if err := os.MkdirAll(bucket, 0o755); err != nil {
		t.Fatal(err)
	}
	// cwd 在项目子目录 → 逐级向上命中项目根。
	p, err := s.FindProject(filepath.Join(projPath, "sub", "dir"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Path != projPath || p.Dir != bucket {
		t.Errorf("命中错误: Path=%q Dir=%q, want Path=%q Dir=%q", p.Path, p.Dir, projPath, bucket)
	}

	// 无已存在桶 → 用 cwd 自身（惰性）。
	other := filepath.Join(root, "other")
	p2, err := s.FindProject(other)
	if err != nil {
		t.Fatal(err)
	}
	if p2.Path != other {
		t.Errorf("无命中应使用 cwd: %+v", p2)
	}
}

func TestSessionsLast(t *testing.T) {
	root := t.TempDir()
	s := NewAt(root)
	projPath := filepath.Join(root, "proj")
	proj := &Project{Path: projPath, Dir: s.ProjectDir(projPath)}
	for _, id := range []string{"20260808T100001-a", "20260808T100002-b", "20260808T100000-c"} {
		if err := os.MkdirAll(filepath.Join(proj.Dir, id), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sess, err := proj.Sessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sess) != 3 {
		t.Fatalf("Sessions len=%d, want 3", len(sess))
	}
	// 目录名排序：100000-c < 100001-a < 100002-b。
	if sess[0].ID != "20260808T100000-c" || sess[2].ID != "20260808T100002-b" {
		t.Errorf("排序错误: %v", sess)
	}
	last, ok := proj.Last()
	if !ok || last.ID != "20260808T100002-b" {
		t.Errorf("Last: %+v ok=%v", last, ok)
	}
}
