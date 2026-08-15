package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/agent-project/harness/internal/config"
	"github.com/agent-project/harness/internal/session"
)

// initCmd 初始化 workspace 根（$HARNESS_HOME 优先，否则 ~/.harness）：
// 目录骨架（workspaces/subagents/memory/logs/skills + agents.md 占位）+ 全局
// 配置模板（config.yaml 不存在时写入全注释模板，存在则不动）。幂等：重复
// 执行已存在项跳过不覆盖。
func initCmd(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("init: 不接受参数（用法: `harness init`）")
	}
	store, err := session.New()
	if err != nil {
		return err
	}
	root := store.Root()

	// 预检存在性（报告用；EnsureDirs/EnsureConfig 本身幂等）。
	type item struct {
		path string
		name string
	}
	items := []item{
		{filepath.Join(root, session.DirWorkspaces), "workspaces/"},
		{filepath.Join(root, session.DirSubagents), "subagents/"},
		{filepath.Join(root, session.DirMemory), "memory/"},
		{filepath.Join(root, session.DirLogs), "logs/"},
		{filepath.Join(root, session.DirSkills), "skills/"},
		{filepath.Join(root, session.FileAgentsMD), "agents.md"},
	}
	var created, skipped []string
	for _, it := range items {
		if _, err := os.Stat(it.path); err == nil {
			skipped = append(skipped, it.name)
		} else {
			created = append(created, it.name)
		}
	}
	cfgPath := filepath.Join(root, "config.yaml")
	if _, err := os.Stat(cfgPath); err == nil {
		skipped = append(skipped, "config.yaml")
	} else {
		created = append(created, "config.yaml")
	}

	if err := store.EnsureDirs(); err != nil {
		return err
	}
	cfgCreated, err := config.EnsureConfig(cfgPath)
	if err != nil {
		return err
	}

	// 报告（中文对齐 sessionsCmd 风格）。
	fmt.Printf("harness: workspace 初始化完成: %s\n", root)
	if len(created) > 0 {
		fmt.Printf("  [创建] %s\n", strings.Join(created, "  "))
	}
	if len(skipped) > 0 {
		fmt.Printf("  [跳过] %s（已存在，不覆盖）\n", strings.Join(skipped, "  "))
	}
	if cfgCreated {
		fmt.Println("  提示：config.yaml 为注释模板，填入 provider 与 API key 后即可 `harness run`")
	}
	return nil
}
