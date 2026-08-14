package tools

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/agent-project/harness/internal/middleware"
)

// MaxOutputChars 是工具结果回填模型的最大长度（超出走 eviction：head/tail
// 双端保留 + 落盘 evictions/ + read_file 指针）。参照 codex HeadTailBuffer
// 与 opencode truncate（ADR-028）。
const MaxOutputChars = 20000

// EvictContent 截断工具结果：超 MaxOutputChars 时保留 head 前 50% + tail 后
// 50%（各 MaxOutputChars/2），中间省略并计数；全量落盘到会话目录下
// evictions/（rc.StatePath 所在目录），返回 preview + 绝对路径 + 读取提示。
// 供 impl 的 ToolOutputMiddleware 统一调用（onToolCall after 截断，ADR-028）；
// 工具自身返回完整结果——shell 工具内自行调用已移除（2026-08-13 勘误：
// 与"截断上收中间件"条款矛盾，造成双重截断 + 冗余 eviction 文件）。
// 定义在 tools 包：工具域逻辑，避免 tools → impl 反向依赖
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
		if mkErr := os.MkdirAll(dir, 0o755); mkErr == nil {
			// 唯一临时名（随机后缀 + O_EXCL）：UnixNano 合成名在并发工具结果
			// 同时超长落盘时撞名——后写者 O_TRUNC 覆盖先写者，多份 eviction
			// 指向同一文件（后台日志分配竞态同源，2026-08-14）。
			if f, cErr := os.CreateTemp(dir, "tool_*.txt"); cErr == nil {
				name := f.Name()
				if abs, aErr := filepath.Abs(name); aErr == nil {
					if _, wErr := f.WriteString(s); wErr == nil && f.Close() == nil {
						path = abs
					} else {
						f.Close()
						os.Remove(name)
					}
				} else {
					f.Close()
					os.Remove(name)
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
