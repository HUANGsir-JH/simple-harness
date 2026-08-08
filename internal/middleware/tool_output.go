package middleware

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// MaxOutputChars 是工具结果回填模型的最大长度（超出走 eviction：head/tail
// 双端保留 + 落盘 evictions/ + read_file 指针）。参照 codex HeadTailBuffer
// 与 opencode truncate（ADR-028）。
const MaxOutputChars = 20000

// EvictContent 截断工具结果：超 MaxOutputChars 时保留 head 前 50% + tail 后
// 50%（各 MaxOutputChars/2），中间省略并计数；全量落盘到会话目录下
// evictions/（rc.StatePath 所在目录），返回 preview + 绝对路径 + 读取提示。
// 导出供 tools 包（shell 超时错误分支）与 ToolOutputMiddleware 复用（ADR-028）。
//
// rc 为 nil 或 StatePath 为空（非会话场景/测试）→ 退化纯截断（不落盘），
// 保持工具测试无需会话环境。短文本原样返回。
func EvictContent(rc *RuntimeContext, s string) string {
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

// ToolOutputMiddleware 是工具结果统一截断中间件（ADR-028）：挂 onToolCall，
// after 阶段改写本批新增的 tool_result 消息内容（截断 + 落盘 evictions/）。
// 工具自身返回完整结果（职责纯），截断策略在此一处定义、可插拔。
//
// 注：transcript（会话落盘）记完整结果（审计全量）；conversation（模型
// 上下文）经本中间件截断为 preview + 路径，模型用 read_file/grep 读全量。
type ToolOutputMiddleware struct {
	Base
}

// OnToolCall 在工具批执行完成后，对本批新增消息（tool_result）统一截断。
func (ToolOutputMiddleware) OnToolCall(ctx context.Context, rc *RuntimeContext, in ToolCallInput, next ToolCallHandler) error {
	if rc == nil || rc.Messages == nil {
		return next(ctx, rc, in)
	}
	before := len(rc.Messages.Messages)
	err := next(ctx, rc, in)
	// after：只处理本批新增的消息（before 之前的属历史，resume 全量保留）。
	for _, msg := range rc.Messages.Messages[before:] {
		for i := range msg.ToolResults {
			msg.ToolResults[i].Content = EvictContent(rc, msg.ToolResults[i].Content)
		}
	}
	return err
}
