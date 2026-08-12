// Package compact 实现上下文压缩（ADR-037 第三段：LLM 摘要式压缩）。
// EstimateTokens 在此提供"实际 usage 未捕获"时的压缩触发估算兜底。
// 完整压缩逻辑（ShouldCompact/Summarizer/Run）在阶段 C 落地。
package compact

import (
	"github.com/agent-project/harness/internal/messages"
)

// EstimateTokens 估算一组消息的 token 数（bytes/4 粗略估算），**镜像实际发送**：
// 计入 assistant 的 thinking 文本与签名（ADR-025 修订完整回传后 thinking 随请求
// 重放）、工具调用参数、工具结果内容——估算的上下文占用应与重放请求一致，
// 否则压缩触发点会偏移。仅用于实际 usage（LastContextTokens = 单轮完整占用，
// ADR-037 勘误）未捕获时的兜底；估算只覆盖输入侧（不含 output），方向保守
// （更早触发），可接受。
func EstimateTokens(msgs []*messages.Message) int {
	total := 0
	for _, m := range msgs {
		if m == nil {
			continue
		}
		total += len(m.Content) + len(m.Thinking)
		if m.ThinkingSignature != "" {
			total += len(m.ThinkingSignature) // 签名随 thinking 块发送
		}
		for _, tc := range m.ToolCalls {
			total += len(tc.Name)
			if len(tc.Args) > 0 {
				total += len(tc.Args)
			}
		}
		for _, tr := range m.ToolResults {
			total += len(tr.Content)
		}
	}
	return total / 4
}
