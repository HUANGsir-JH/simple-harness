// Package agent 实现 harness 的 agent 循环。
// 阶段二：纯 ReAct loop（采样 → 工具执行 → 回填 → 再次采样），不含任何工程
// 能力（压缩/权限/记忆等作为 middleware 挂载，ADR-021）。回合级事件通过
// events.OnEvent 回调发出（类型定义下沉 internal/events，A2），供渲染器与
// 测试订阅（turn_done 为回合边界锚点）。
package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/agent-project/harness/internal/events"
	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/provider"
	"github.com/agent-project/harness/internal/tools"
)

// Agent 驱动一个 conversation 的完整 ReAct loop。仅持有不可变配置；
// per-call 状态在 *middleware.RuntimeContext 与 conversation 中。
type Agent struct {
	client       provider.Client
	model        string
	instructions string
	tools        *tools.Registry
	mw           *middleware.Chain
}

// New 创建绑定到 provider 客户端与模型的 Agent。
func New(client provider.Client, model string) *Agent {
	return &Agent{
		client:       client,
		model:        model,
		instructions: "You are a helpful coding agent.",
		tools:        tools.NewRegistry(),
		mw:           middleware.NewChain(),
	}
}

// SetTools 设置工具注册表（nil 时内部保留空表）。
func (a *Agent) SetTools(r *tools.Registry) {
	if r != nil {
		a.tools = r
	}
}

// SetMiddleware 设置 middleware 链（nil 时保留空链）。
func (a *Agent) SetMiddleware(c *middleware.Chain) {
	if c != nil {
		a.mw = c
	}
}

// SetInstructions 设置基础系统提示（阶段四 AGENTS.md 等动态拼接替换）。
func (a *Agent) SetInstructions(s string) { a.instructions = s }

// Run 跑一个完整回合：多轮 采样 → 工具执行 → 回填，直到模型不再请求工具。
// 消息序列从 rc.Messages 读取并在其上追加（assistant/tool_result）——agent 完全
// 无状态（ADR-026），不持有会话；每次调用传入独立 rc（切换会话/并行安全）。
// 事件通过 onEvent 实时回调；conversation 被追加助手/工具结果消息（副作用）。
// 错误二分类：工具错误 RespondToModel → 结果回填、循环继续；Fatal → 终止。
func (a *Agent) Run(ctx context.Context, rc *middleware.RuntimeContext, onEvent events.OnEvent) error {
	if rc == nil {
		rc = middleware.NewRuntimeContext()
	}
	if rc.Messages == nil {
		return fmt.Errorf("agent: rc.Messages is nil")
	}
	conversation := rc.Messages
	emit := func(e events.Event) {
		if onEvent != nil {
			onEvent(e)
		}
	}

	// onAgent 包住整个回合：before 先于 turn_start（回合级准备，如加载
	// AgentState）、after 后于 turn_done（回合级收尾）。空链时透传无开销。
	wrapped := a.mw.WrapAgent(func(ctx context.Context, rc *middleware.RuntimeContext, _ middleware.AgentInput) error {
		emit(events.Event{Type: events.EventTurnStart})

		sysPrompt, err := a.mw.ComposeSystemPrompt(ctx, rc, a.instructions)
		if err != nil {
			return fmt.Errorf("agent: compose system prompt: %w", err)
		}

		var result sampleResult
		reasoning := a.mw.WrapReasoning(func(ctx context.Context, rc *middleware.RuntimeContext, in middleware.ReasoningInput) error {
			r, err := a.sample(ctx, rc, in, sysPrompt, emit)
			if err != nil {
				return err
			}
			result = *r
			return nil
		})

		for {
			result = sampleResult{}
			if err := reasoning(ctx, rc, middleware.ReasoningInput{Messages: conversation.Messages, Tools: a.tools.Specs()}); err != nil {
				emit(events.Event{Type: events.EventError, Err: err})
				return err
			}
			conversation.Add(result.assistant)
			if len(result.toolCalls) == 0 {
				break // 无工具调用 → 回合结束
			}
			if err := a.runToolBatch(ctx, rc, result.toolCalls, conversation, emit); err != nil {
				emit(events.Event{Type: events.EventError, Err: err})
				return err
			}
		}
		emit(events.Event{Type: events.EventTurnDone})
		return nil
	})
	return wrapped(ctx, rc, middleware.AgentInput{Messages: conversation.Messages})
}

// sampleResult 是一次采样轮的结果。
type sampleResult struct {
	assistant *messages.Message
	toolCalls []*messages.ToolCall
}

// sample 执行一次采样：模型调用（onModelCall 包裹）→ 收集 thinking/text/tool_call。
// rc 是 per-call 上下文：模型/thinking 档位覆盖读自 rc（ADR-026），并贯穿
// 到 onModelCall 中间件（此前漏传 nil，中间件读 rc 会解引用错误）。
func (a *Agent) sample(ctx context.Context, rc *middleware.RuntimeContext, in middleware.ReasoningInput, sysPrompt string, emit events.OnEvent) (*sampleResult, error) {
	// 每个采样轮生成一个消息 id，块事件与 assistant 消息共用（transcript 关联）。
	msgID := fmt.Sprintf("msg_%d", timeNowNanos())
	// sb/tb 用"块完成"事件（events.EventTextDone/events.EventThinkingDone）组装，避免与
	// 流式 delta 重复；delta 事件照发供渲染器流式展示。ADR-025。
	var sb strings.Builder
	var tb strings.Builder
	var calls []*messages.ToolCall

	// per-call 覆盖（ADR-026）：rc.Model / rc.ThinkingEnabled / rc.ThinkingEffort
	// 优先，未设置则用 agent/client 默认。
	model := a.model
	if rc != nil && rc.Model != "" {
		model = rc.Model
	}
	var thinkingEnabled *bool
	if rc != nil && rc.ThinkingEnabled != nil {
		v := *rc.ThinkingEnabled
		thinkingEnabled = &v
	}
	var thinkingEffort string
	if rc != nil {
		thinkingEffort = rc.ThinkingEffort
	}

	wrapped := a.mw.WrapModelCall(func(ctx context.Context, rc *middleware.RuntimeContext, min middleware.ModelCallInput) error {
		req := provider.Request{
			Model:           model,
			Instructions:    sysPrompt,
			Messages:        min.Messages,
			Tools:           min.Tools,
			ThinkingEnabled: thinkingEnabled,
			ThinkingEffort:  thinkingEffort,
		}
		es, err := a.client.Stream(ctx, req)
		if err != nil {
			return err
		}
		defer es.Close()
		for es.Next() {
			ev := es.Current()
			switch ev.Type {
			case provider.EventTextDelta:
				emit(events.Event{Type: events.EventTextDelta, Text: ev.Text})
			case provider.EventTextDone:
				sb.WriteString(ev.Text)
				emit(events.Event{Type: events.EventTextDone, Text: ev.Text, MsgID: msgID})
			case provider.EventThinkingDelta:
				emit(events.Event{Type: events.EventThinkingDelta, Text: ev.Text})
			case provider.EventThinkingDone:
				tb.WriteString(ev.Text)
				emit(events.Event{Type: events.EventThinkingDone, Text: ev.Text, MsgID: msgID})
			case provider.EventToolCall:
				calls = append(calls, ev.ToolCall)
				emit(events.Event{Type: events.EventToolCall, ToolCall: ev.ToolCall, MsgID: msgID})
			case provider.EventDone:
				return nil
			case provider.EventError:
				return ev.Error
			}
		}
		return es.Err()
	})
	if err := wrapped(ctx, rc, middleware.ModelCallInput{Messages: in.Messages, Tools: in.Tools}); err != nil {
		return nil, err
	}

	// 值切片（消息模型存储）与指针切片（执行用）分开。
	toolCallValues := make([]messages.ToolCall, 0, len(calls))
	for _, c := range calls {
		toolCallValues = append(toolCallValues, *c)
	}
	assistant := &messages.Message{
		ID:        msgID,
		Role:      messages.RoleAssistant,
		Content:   sb.String(),
		Thinking:  tb.String(),
		ToolCalls: toolCallValues,
	}
	return &sampleResult{assistant: assistant, toolCalls: calls}, nil
}

// runToolBatch 并发执行一批工具调用（onToolCall 包裹整批，onActing 包裹单个），
// 结果按 call 顺序回填 conversation。首个 Fatal 错误取消整批并终止。
//
// 时序契约（C6，2026-08-10）：emit(events.EventToolResult) 在每个工具 Handle 返回后
// 立即发生，早于外层 ToolOutputMiddleware 的 after 改写 conversation——
// transcript 因此记全量、conversation 记截断（双轨审计，ADR-025）。若把 emit
// 挪到工具批完成/截断之后，transcript 会记录截断内容、审计完整性静默丢失；
// agent 测试 TestEmitBeforeTruncation 锁定该契约。
func (a *Agent) runToolBatch(ctx context.Context, rc *middleware.RuntimeContext, calls []*messages.ToolCall, conversation *messages.Conversation, emit events.OnEvent) error {
	// 结果按 callID 收集（并发安全），回填时按 calls 顺序。
	var resultsMu sync.Mutex
	results := map[string]*messages.ToolResult{}

	acting := a.mw.WrapActing(func(ctx context.Context, rc *middleware.RuntimeContext, in middleware.ActingInput) error {
		tool, ok := a.tools.Get(in.Call.Name)
		if !ok {
			res := &messages.ToolResult{Success: false, Content: "未知工具: " + in.Call.Name}
			resultsMu.Lock()
			results[in.Call.ID] = res
			resultsMu.Unlock()
			emit(events.Event{Type: events.EventToolResult, ToolCall: in.Call, ToolResult: res})
			return nil
		}
		r, err := tool.Handle(ctx, rc, in.Call.ID, in.Call.Args)
		if err != nil {
			var te *tools.ToolError
			if errors.As(err, &te) && te.RespondToModel {
				// RespondToModel：作为失败结果回填，循环继续。
				res := &messages.ToolResult{Success: false, Content: te.Message}
				resultsMu.Lock()
				results[in.Call.ID] = res
				resultsMu.Unlock()
				emit(events.Event{Type: events.EventToolResult, ToolCall: in.Call, ToolResult: res})
				return nil
			}
			return fmt.Errorf("工具 %s 执行失败: %w", in.Call.Name, err) // Fatal
		}
		resultsMu.Lock()
		results[in.Call.ID] = &r
		resultsMu.Unlock()
		emit(events.Event{Type: events.EventToolResult, ToolCall: in.Call, ToolResult: &r})
		return nil
	})

	wrapped := a.mw.WrapToolCall(func(ctx context.Context, rc *middleware.RuntimeContext, in middleware.ToolCallInput) error {
		errCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		var wg sync.WaitGroup
		var mu sync.Mutex
		var firstErr error
		for _, c := range in.Calls {
			if c == nil {
				continue
			}
			wg.Add(1)
			go func(c *messages.ToolCall) {
				defer wg.Done()
				if err := acting(errCtx, rc, middleware.ActingInput{Call: c}); err != nil {
					// 审批拒绝（middleware.DeniedError，ADR-029）：作为失败结果
					// 回填、循环继续（ADR-006：拒绝 ≠ Fatal，不取消整批）。
					var de *middleware.DeniedError
					if errors.As(err, &de) {
						res := &messages.ToolResult{Success: false, Content: de.Reason}
						resultsMu.Lock()
						results[c.ID] = res
						resultsMu.Unlock()
						emit(events.Event{Type: events.EventToolResult, ToolCall: c, ToolResult: res})
						return
					}
					mu.Lock()
					if firstErr == nil {
						firstErr = err
						cancel() // 取消其余工具
					}
					mu.Unlock()
				}
			}(c)
		}
		wg.Wait()
		// 按 calls 顺序收集结果（不按完成顺序，保持模型预期一致），
		// 合并成一条 tool result 消息（anthropic 要求 tool_use 后下一条消息
		// 含全部 tool_result）。
		blocks := make([]messages.ToolResultBlock, 0, len(in.Calls))
		for _, c := range in.Calls {
			if r := results[c.ID]; r != nil {
				blocks = append(blocks, messages.ToolResultBlock{ToolCallID: c.ID, Success: r.Success, Content: r.Content})
			}
		}
		if len(blocks) > 0 {
			conversation.Add(messages.NewToolResultsMessage(blocks))
		}
		return firstErr
	})
	return wrapped(ctx, rc, middleware.ToolCallInput{Calls: calls})
}

// timeNowNanos 是一个小间接层，便于测试注入确定性的 ID；
// 生产环境使用墙钟。
func timeNowNanos() int64 { return time.Now().UnixNano() }
