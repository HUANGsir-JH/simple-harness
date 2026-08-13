// Package compact 实现上下文压缩（ADR-037 第三段：LLM 摘要式压缩）。
// EstimateTokens/EstimateSystemPrompt/EstimateTools 在此提供"实际 usage 未捕获"
// 时的压缩触发估算兜底（判定时实时，镜像实际发送的三通道：messages + system + tools）。
package compact

import (
	"encoding/json"

	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/provider"
)

// EstimateTokens 估算一组消息的 token 数（bytes/4 粗略估算），**镜像实际发送**：
// 计入 assistant 的 thinking 文本与签名（ADR-025 修订完整回传后 thinking 随请求
// 重放）、工具调用参数、工具结果内容——估算的上下文占用应与重放请求一致，
// 否则压缩触发点会偏移。仅用于实际 usage（LastContextTokens = 单轮完整占用，
// ADR-037 勘误）未捕获时的兜底（首轮/压缩后首轮）。
//
// 口径边界（ADR-037 修订 2026-08-13）：本函数只覆盖 conversation；系统提示与
// 工具 schema 是每次请求的两个独立通道（params.System / params.Tools，不进
// messages），由判定时实时估算补齐：EstimateSystemPrompt(rc.SystemPrompt) +
// EstimateTools(in.Tools)——不再装配期固定注入（固定值在阶段四动态注入后失效）。
// 只覆盖输入侧（不含 output）则与压缩判定的输入侧口径一致。
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

// EstimateSystemPrompt 估算系统提示的 token 数（bytes/4）。系统提示随每次请求
// 走 params.System 顶层字段发送，不进 messages——兜底估算需单独计入
// （ADR-037 修订 2026-08-13：判定时实时，读组合后的 rc.SystemPrompt）。
func EstimateSystemPrompt(s string) int64 {
	return int64(len(s) / 4)
}

// EstimateTools 估算工具 schema 的 token 数（JSON 序列化后 bytes/4，与 wire
// 实际发送形状一致）。工具定义随每次请求走 params.Tools 顶层字段发送
// （实测 7 内置工具约 7KB ≈ 1.8K token），不进 messages——兜底估算需单独计入。
func EstimateTools(tools []provider.ToolSpec) int64 {
	if len(tools) == 0 {
		return 0
	}
	b, err := json.Marshal(tools)
	if err != nil {
		return 0
	}
	return int64(len(b) / 4)
}
