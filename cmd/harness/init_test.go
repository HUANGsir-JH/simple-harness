package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestInitCmd 验证 harness init：建骨架（5 目录 + agents.md 占位）+
// 注释版 config.yaml 模板；幂等且不覆盖用户编辑。
func TestInitCmd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HARNESS_HOME", home)
	if err := initCmd(nil); err != nil {
		t.Fatalf("init: %v", err)
	}
	for _, p := range []string{
		"workspaces", "subagents", "memory", "logs", "skills", "agents.md", "config.yaml",
	} {
		if _, err := os.Stat(filepath.Join(home, p)); err != nil {
			t.Errorf("缺 %s: %v", p, err)
		}
	}
	// skills/ 占位说明（ADR-044）。
	if _, err := os.Stat(filepath.Join(home, "skills", "README.md")); err != nil {
		t.Errorf("缺 skills/README.md: %v", err)
	}
	cfg := filepath.Join(home, "config.yaml")
	data, _ := os.ReadFile(cfg)
	if len(data) == 0 {
		t.Error("config.yaml 应为注释模板")
	}

	// 幂等：重跑不报错、不覆盖用户编辑。
	userEdit := "# 用户已编辑\n"
	if err := os.WriteFile(cfg, []byte(userEdit), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := initCmd(nil); err != nil {
		t.Fatalf("重复 init: %v", err)
	}
	data, _ = os.ReadFile(cfg)
	if string(data) != userEdit {
		t.Errorf("config.yaml 不应被覆盖: %q", data)
	}
}

// TestInitCmdRejectsArgs 验证 init 不接受参数。
func TestInitCmdRejectsArgs(t *testing.T) {
	if err := initCmd([]string{"extra"}); err == nil {
		t.Error("init 带参数应报错")
	}
}
