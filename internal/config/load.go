package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// EnvHome 是 harness 工作根目录覆盖环境变量（对标 codex CODEX_HOME）。
// 2026-08-14 起与 session 包对齐：config 查找的全局配置路径也尊重它
// （此前写死 ~/.harness/config.yaml，HARNESS_HOME 环境下的 init/测试隔离
// 不一致）。规范定义在本包（config 最底层），session.EnvHome 别名引用。
const EnvHome = "HARNESS_HOME"

// LoadConfig 从第一个存在的配置文件读取并校验（配置加载统一收敛到 config 包，
// CLI 各命令经 App 复用一份，不重复读盘）。
//
// 查找顺序：显式路径（若指定）→ 项目级 config.local.yaml → 全局 config.yaml
// （$HARNESS_HOME 优先，否则 ~/.harness）。API key 可放在配置文件（api_key）
// 或环境变量（env_key / 默认变量名）中。
func LoadConfig(path string) (Config, error) {
	for _, p := range configCandidates(path) {
		data, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return Config{}, fmt.Errorf("read config %s: %w", p, err)
		}
		var cfg Config
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("config %s: %w", p, err)
		}
		if err := cfg.Validate(); err != nil {
			return Config{}, fmt.Errorf("config %s: %w", p, err)
		}
		return cfg, nil
	}
	return Config{}, fmt.Errorf("no config found: create config.local.yaml in this project or ~/.harness/config.yaml (see `harness help`)")
}

// configCandidates 返回配置文件查找顺序（见 LoadConfig）。
func configCandidates(path string) []string {
	if path != "" {
		return []string{path}
	}
	var out []string
	if cwd, err := os.Getwd(); err == nil {
		local := filepath.Join(cwd, "config.local.yaml")
		if _, err := os.Stat(local); err == nil {
			out = append(out, local)
		}
	}
	if global := globalConfigPath(); global != "" {
		out = append(out, global)
	}
	return out
}

// globalConfigPath 返回全局配置路径：$HARNESS_HOME/config.yaml 优先（与
// session 的 workspace 根对齐，2026-08-14 `harness init` 写入同位置），否则
// ~/.harness/config.yaml；两者都不可得时返回空。
func globalConfigPath() string {
	if env := os.Getenv(EnvHome); env != "" {
		return filepath.Join(env, "config.yaml")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".harness", "config.yaml")
	}
	return ""
}
