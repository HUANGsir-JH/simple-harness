package compact

import (
	"context"
	"fmt"
	"strings"

	"github.com/agent-project/harness/internal/events"
	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/provider"
)

// ThresholdPercent 是压缩触发阈值：context_window 的 85%（ADR-037，硬编码无配置）。
const ThresholdPercent = 85

// summaryOutputTokens 是摘要请求的 max_tokens。
const summaryOutputTokens = 8192

// Options 是压缩选项。全程不加配置（ADR-037）：ContextWindow 来自模型配置，
// Model/MaxOutputTokens 由装配根（agent.Build）从 client 解析。
type Options struct {
	ContextWindow   int64  // 模型 context_window（token；0 = 无法判定阈值 → 不触发）
	Model           string // 摘要请求模型（正常采样同款；空 = client 默认）
	MaxOutputTokens int    // 摘要请求最大输出（codex 方式 4096；0 = 用 summaryOutputTokens）
}

// summaryTemplate 是 opencode 的锚定摘要模板（Objective / Important Details /
// Work State(Completed/Active/Blocked) / Next Move / Relevant Files）。逐字沿用
// opencode 原文（ADR-037 用户选定的结构化模板）。
const summaryTemplate = `Output exactly the Markdown structure shown inside <template> and keep the section order unchanged. Do not include the <template> tags in your response.
<template>
## Objective
- [one or two brief sentences describing what the user is trying to accomplish]

## Important Details
- [constraints/preferences, decisions and why, important facts/assumptions, exact context needed to continue, or "(none)"]

## Work State
### Completed
- [finished work, verified facts, or changes made; otherwise "(none)"]

### Active
- [current work, partial changes, or investigation state; otherwise "(none)"]

### Blocked
- [blockers, failing commands, or unknowns; otherwise "(none)"]

## Next Move
1. [immediate concrete action, or "(none)"]
2. [next action if known, or "(none)"]

## Relevant Files
- [file or directory path: why it matters, or "(none)"]
</template>

Rules:
- Keep every section, even when empty.
- Use terse bullets, not prose paragraphs.
- Preserve exact file paths, symbols, commands, error strings, URLs, and identifiers when known.
- Do not mention the summary process or that context was compacted.`

// BuildSummaryPrompt 构造摘要 prompt：新建式指令 + 锚定模板（opencode 结构化
// 模板）。旧摘要无需作为 previous 单独传入——压缩后 conversation 首条就是旧
// 摘要 user 消息（ADR-037 纯占位设计），LLM 在历史里可见，prompt 里再嵌一份
// 是重复喂送（review 07）。prompt 作为最后一条 user 消息追加到完整 conversation
// （codex 方式发送，ADR-037）。
func BuildSummaryPrompt() string {
	return "Create a new anchored summary from the conversation history.\n\n" + summaryTemplate
}

// contextSize 返回当前上下文占用（token）：实际 usage（LastContextTokens =
// 最近一次请求的完整占用，单轮 input+cache+output，ADR-037 勘误）优先；
// 未捕获（0）时用估算兜底（判定时实时，镜像实际发送的三通道，ADR-037 修订）：
// conversation（EstimateTokens）+ 系统提示（组合后的 rc.SystemPrompt）+
// 工具 schema（本轮采样将发送的 in.Tools）。tools 仅兜底分支使用。
//
// 触发滞后说明（review 05，接受设计）：usage 驱动读的是**上一轮**请求的
// usage，回合间新用户消息与上一轮工具结果不在其中——滞后最多一轮；单轮增长
// 受 ToolOutput 20K 截断约束，越过 85% 的幅度有界。
func contextSize(rc *middleware.RuntimeContext, tools []provider.ToolSpec) int64 {
	if rc != nil && rc.State != nil && rc.State.CurrentContextTokens() > 0 {
		return rc.State.CurrentContextTokens()
	}
	sys := ""
	if rc != nil {
		sys = rc.SystemPrompt
	}
	return int64(EstimateTokens(messagesOf(rc))) + EstimateSystemPrompt(sys) + EstimateTools(tools)
}

func messagesOf(rc *middleware.RuntimeContext) []*messages.Message {
	if rc == nil || rc.Messages == nil {
		return nil
	}
	return rc.Messages.Messages
}

// ShouldCompact 判断当前上下文是否超过压缩阈值（85% · ContextWindow）。
// tools 是下一轮采样将发送的工具 schema（仅估算兜底分支使用；usage 优先
// 路径不碰它——API 返回的 input/cache 已含 system+tools+messages 全量）。
func ShouldCompact(rc *middleware.RuntimeContext, tools []provider.ToolSpec, opts Options) bool {
	if opts.ContextWindow <= 0 {
		return false
	}
	return contextSize(rc, tools) >= int64(float64(opts.ContextWindow)*ThresholdPercent/100)
}

// Summarizer 生成压缩摘要：复用 provider.Client 单独采样（codex 方式，ADR-037）——
// 请求 = 完整 conversation + 摘要 prompt 作最后一条 user 消息，无工具，
// max_tokens 4096。conversation 经 client 内部 toAnthropicMessages 转换（含
// thinking 完整回传 + 工具邻接正确），摘要模型看到的上下文与正常采样一致
// （上下文基线依赖阶段 B，ADR-025 修订）。
type Summarizer struct {
	client provider.Client
	opts   Options
}

// NewSummarizer 构造摘要器（client 复用正常采样的 provider.Client）。
func NewSummarizer(client provider.Client, opts Options) *Summarizer {
	return &Summarizer{client: client, opts: opts}
}

// Summarize 生成摘要。失败返回错误（**不写 conversation**；调用方决定终止）。
// ctx 取消（Esc 中断压缩）返回 ctx 错误，与摘要失败同处理（ADR-037）。
func (s *Summarizer) Summarize(ctx context.Context, rc *middleware.RuntimeContext) (string, error) {
	if s.client == nil {
		return "", fmt.Errorf("compact: 未配置摘要客户端")
	}
	msgs := messagesOf(rc)
	reqMsgs := make([]*messages.Message, 0, len(msgs)+1)
	reqMsgs = append(reqMsgs, msgs...)
	reqMsgs = append(reqMsgs, &messages.Message{Role: messages.RoleUser, Content: BuildSummaryPrompt()})

	// 模型覆盖与 agent.sample 同规则（ADR-026）：rc.Model 优先（/model 运行时
	// 切换），未设置时用装配默认 opts.Model——保证摘要与正常采样模型一致。
	model := s.opts.Model
	if rc != nil && rc.Model != "" {
		model = rc.Model
	}
	maxOut := s.opts.MaxOutputTokens
	if maxOut <= 0 {
		maxOut = summaryOutputTokens
	}
	es, err := s.client.Stream(ctx, provider.Request{
		Model:           model,
		Messages:        reqMsgs,
		MaxOutputTokens: maxOut,
		// Tools 空：摘要请求不提供工具（codex/opencode 同，ADR-037）。
	})
	if err != nil {
		return "", err
	}
	defer es.Close()

	var sb strings.Builder
	for es.Next() {
		switch ev := es.Current(); ev.Type {
		// 只收 EventTextDone（整块文本，同 agent.sample 组装逻辑）：EventTextDelta
		// 是流式增量、块完成时总和等于块全文——delta+done 都收会双重计数。
		case provider.EventTextDone:
			sb.WriteString(ev.Text)
		case provider.EventError:
			if ev.Error != nil {
				return "", ev.Error
			}
		case provider.EventDone:
			return strings.TrimSpace(sb.String()), nil
		}
	}
	if err := es.Err(); err != nil {
		return "", err
	}
	return strings.TrimSpace(sb.String()), nil
}

// Runner 执行上下文压缩（自动 onReasoning + 手动 /compact 共用同一实例；
// 无状态：Summarizer + Options 不可变，只从 rc 读写，共享 chain 可并发）。
type Runner struct {
	summarizer *Summarizer
	opts       Options
}

// NewRunner 构造压缩执行器。
func NewRunner(summarizer *Summarizer, opts Options) *Runner {
	return &Runner{summarizer: summarizer, opts: opts}
}

// ShouldCompact 判定当前上下文是否超阈值（85% · ContextWindow）。
// tools 是下一轮采样将发送的工具 schema（仅估算兜底分支使用）——
// 由 CompactMiddleware（onReasoning）持 ReasoningInput.Tools 传入；
// 手动 /compact 不判定（直接 Run），无需 tools。
func (r *Runner) ShouldCompact(rc *middleware.RuntimeContext, tools []provider.ToolSpec) bool {
	if r == nil {
		return false
	}
	return ShouldCompact(rc, tools, r.opts)
}

// Run 执行一次压缩（无条件——判定由调用方决定：CompactMiddleware 先
// ShouldCompact 再 Run，手动 /compact 直接 Run，ADR-037 修订 2026-08-13）。
// 成功返回 nil；摘要失败/取消返回错误（调用方终止 Run）；**失败绝不重写
// conversation**（不丢历史，下轮可再触发或手动 /compact）。Esc 中断 = ctx
// 错误原样传播。
func (r *Runner) Run(ctx context.Context, rc *middleware.RuntimeContext) error {
	if r == nil || r.summarizer == nil {
		return nil
	}
	if rc == nil || rc.Messages == nil {
		return fmt.Errorf("compact: 无会话上下文")
	}

	// 压缩开始通知（ADR-037 扩展）：Summarize 阻塞调用前经 rc.Emit 发出
	// （自动/手动共用此起点；判定已由调用方完成，不会误报 start）。
	if rc.Emit != nil {
		rc.Emit(events.Event{Type: events.EventCompactStart})
	}

	summary, err := r.summarizer.Summarize(ctx, rc)
	if err != nil {
		return fmt.Errorf("上下文压缩失败: %w", err)
	}
	if strings.TrimSpace(summary) == "" {
		return fmt.Errorf("上下文压缩失败: 摘要为空")
	}

	// 重写 conversation = 单一 summary user 消息（纯占位，ADR-037）。"保留最新
	// 信息"由摘要 prompt 交给 LLM（opencode 结构化模板）；tool_use→tool_result
	// 邻接天然安全（纯占位全丢，无残留配对问题）。
	summaryMsg := messages.NewUserMessage(summary)
	// 先落盘后重写（review 03）：Segment 失败时内存 conversation/state 未动，
	// 双轨一致（都还是压缩前），下一轮干净重试；落盘成功后的步骤全是无失败点
	// 的内存操作，顺序安全。
	if rc.Segment != nil {
		if err := rc.Segment([]*messages.Message{summaryMsg}); err != nil {
			return fmt.Errorf("上下文压缩落盘失败: %w", err)
		}
	}
	rc.Messages.Messages = []*messages.Message{summaryMsg}
	if rc.State != nil {
		rc.State.SetLastContextTokens(0)    // 防重入：压缩后上下文占用清零
		rc.State.SetUsage(messages.Usage{}) // 压缩后用量归零（/usage 与 footer 对称），下轮采样覆盖恢复
	}
	rc.Set(middleware.CompactedKey, true)
	return nil
}
