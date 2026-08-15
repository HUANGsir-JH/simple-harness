package config

import (
	"fmt"
	"os"
)

// templateConfig 是 `harness init` 写入的全局配置模板（2026-08-14）：全注释、
// 无激活值——不误连任何端点；run 在用户填好之前报 "providers: no providers
// configured"（比"文件不存在"更明确地指向"去填配置"）。完整可运行示例见仓库
// 根 config.example.yaml（两者用途不同：模板 = 骨架引导，示例 = 完整参考）。
const templateConfig = `# harness 全局配置（由 ` + "`harness init`" + ` 生成的全注释模板）
# 填入你的 provider 与 API key 后即可 ` + "`harness run \"你好\"`" + `。
# 完整可运行示例见仓库根目录 config.example.yaml。
#
# 结构：default_provider 指定默认供应商，providers 按名称分组。
# 每个 provider 含 base_url / api_key(或 env_key) / models。
# 仅支持 anthropic wire（ADR-022）——多 provider = 多 anthropic 兼容端点
# （Anthropic 官方、DeepSeek anthropic 兼容等），base_url 覆盖即可。
#
# 示例：
# default_provider: my-anthropic
# providers:
#   my-anthropic:
#     base_url: https://api.anthropic.com/
#     env_key: ANTHROPIC_API_KEY        # 或 api_key: sk-xxx
#     models:
#       claude-sonnet-5:
#         context_window: 1000000
#         thinking:
#           efforts: [low, high, max]
#
# API key 两种提供方式：
#   1. api_key 直接写在本文件（本地配置推荐）
#   2. env_key 指定环境变量名，运行时从环境读取（默认 ANTHROPIC_API_KEY）
#
# 模型选择：harness run --model <名>；不指定用 default_provider 的 models 第一个。
# thinking 默认开启；会话级切换用 TUI /thinking 或 run --thinking/--no-thinking。
#
# 工具审批（ADR-029）：approval.mode 三档——readonly（写操作/命令询问）/
# acceptedit（默认，只读+编辑放行，shell 询问）/ bypass（全部放行）。
# 运行时 /permission 做会话级切换；不配置 = acceptedit。
# approval:
#   mode: acceptedit
`

// EnsureConfig 确保 path 上存在配置文件：不存在时写入注释模板并返回
// created=true；已存在则完全不动（不覆盖用户编辑），created=false。
// 写盘原子性：先写临时名再 rename（与 completion.Queue.persist 同款精神）。
func EnsureConfig(path string) (created bool, err error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("config: stat %s: %w", path, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(templateConfig), 0o644); err != nil {
		return false, fmt.Errorf("config: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return false, fmt.Errorf("config: rename %s: %w", tmp, err)
	}
	return true, nil
}
