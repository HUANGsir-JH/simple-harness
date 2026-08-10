package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/agent-project/harness/internal/middleware"
)

// MaxOutputChars 是工具结果回填模型的最大长度（超出走 eviction：head/tail
// 双端保留 + 落盘 evictions/ + read_file 指针）。参照 codex HeadTailBuffer
// 与 opencode truncate（ADR-028）。
const MaxOutputChars = 20000

// EvictContent 截断工具结果：超 MaxOutputChars 时保留 head 前 50% + tail 后
// 50%（各 MaxOutputChars/2），中间省略并计数；全量落盘到会话目录下
// evictions/（rc.StatePath 所在目录），返回 preview + 绝对路径 + 读取提示。
// 供 tools 包（shell 超时错误分支）与 impl 的 ToolOutputMiddleware 复用
// （ADR-028）。定义在 tools 包：工具域逻辑，避免 tools → impl 反向依赖
// （Bug03 断环，2026-08-10）。
//
// rc 为 nil 或 StatePath 为空（非会话场景/测试）→ 退化纯截断（不落盘），
// 保持工具测试无需会话环境。短文本原样返回。
func EvictContent(rc *middleware.RuntimeContext, s string) string {
	if len(s) <= MaxOutputChars {
		return s
	}
	half := MaxOutputChars / 2
	head := s[:half]
	tail := s[len(s)-half:]

	path := ""
	if rc != nil && rc.StatePath != "" {
		dir := filepath.Join(filepath.Dir(rc.StatePath), "evictions")
		if abs, err := filepath.Abs(filepath.Join(dir, fmt.Sprintf("tool_%d.txt", time.Now().UnixNano()))); err == nil {
			if mkErr := os.MkdirAll(filepath.Dir(abs), 0o755); mkErr == nil {
				if os.WriteFile(abs, []byte(s), 0o644) == nil {
					path = abs
				}
			}
		}
	}

	omitted := len(s) - 2*half
	preview := fmt.Sprintf("…输出已截断（共 %d 字节，保留前/后各 %d 字节）…\n\n%s\n…(中间省略 %d 字节)…\n%s",
		len(s), half, head, omitted, tail)
	if path != "" {
		return fmt.Sprintf("输出已截断，完整内容已保存到：%s\n可用 read_file %s（支持 start_line/end_line）分段读取，或用 grep 搜索。\n\n%s",
			path, path, preview)
	}
	return preview + "\n… [truncated]"
}
