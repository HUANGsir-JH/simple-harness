// Package agent 实现 harness 的 agent 循环。
// 阶段二：纯 ReAct loop（采样 → 工具执行 → 回填 → 再次采样），不含任何工程
// 能力（压缩/权限/记忆等作为 middleware 挂载，ADR-021）。回合级事件通过
// OnEvent 回调发出，供渲染器与测试订阅（turn_done 为回合边界锚点）。
package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/provider"
	"github.com/agent-project/harness/internal/tools"
)

// EventType 是 agent 回合级事件类型（渲染器/测试订阅）。
type EventType string

const (
	// EventTurnStart 标记一个回合（一次 agent.Run）的开始。
	EventTurnStart EventType = "turn_start"
	// EventThinkingDelta 是模型推理文本增量（thinking 展示）。
	EventThinkingDelta EventType = "thinking_delta"
	// EventTextDelta 是助手回复文本增量。
	EventTextDelta EventType = "text_delta"
	// EventToolCall 是模型发起的一个工具调用。
	EventToolCall EventType = "tool_call"
	// EventToolResult 是单个工具的执行结果。
	EventToolResult EventType = "tool_result"
	// EventTurnDone 标记一个回合的结束（★ 测试锚点）。
	EventTurnDone EventType = "turn_done"
	// EventError 是回合级错误。
	EventError EventType = "error"
)

// Event 是单个回合级事件。
type Event struct {
	Type       EventType
	Text       string
	ToolCall   *messages.ToolCall
	ToolResult *messages.ToolResult
	Err        error
}

// OnEvent 是回合级事件回调（nil 允许，渲染器/测试订阅）。
type OnEvent func(Event)

// Agent 驱动一个 thread 的完整 ReAct loop。仅持有不可变配置；
// per-call 状态在 *middleware.RuntimeContext 与 thread 中。
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
// 事件通过 onEvent 实时回调；thread 被追加助手/工具结果消息（副作用）。
// 错误二分类：工具错误 RespondToModel → 结果回填、循环继续；Fatal → 终止。
func (a *Agent) Run(ctx context.Context, rc *middleware.RuntimeContext, thread *messages.Thread, onEvent OnEvent) error {
	if rc == nil {
		rc = middleware.NewRuntimeContext()
	}
	if thread == nil {
		return fmt.Errorf("agent: thread is nil")
	}
	emit := func(e Event) {
		if onEvent != nil {
			onEvent(e)
		}
	}

	// onAgent 包住整个回合：before 先于 turn_start（回合级准备，如加载
	// AgentState）、after 后于 turn_done（回合级收尾）。空链时透传无开销。
	wrapped := a.mw.WrapAgent(func(ctx context.Context, rc *middleware.RuntimeContext, _ middleware.AgentInput) error {
		emit(Event{Type: EventTurnStart})

		sysPrompt, err := a.mw.ComposeSystemPrompt(ctx, rc, a.instructions)
		if err != nil {
			return fmt.Errorf("agent: compose system prompt: %w", err)
		}

		var result sampleResult
		reasoning := a.mw.WrapReasoning(func(ctx context.Context, rc *middleware.RuntimeContext, in middleware.ReasoningInput) error {
			r, err := a.sample(ctx, in, sysPrompt, emit)
			if err != nil {
				return err
			}
			result = *r
			return nil
		})

		for {
			result = sampleResult{}
			if err := reasoning(ctx, rc, middleware.ReasoningInput{Messages: thread.Messages, Tools: a.tools.Specs()}); err != nil {
				emit(Event{Type: EventError, Err: err})
				return err
			}
			thread.Add(result.assistant)
			if len(result.toolCalls) == 0 {
				break // 无工具调用 → 回合结束
			}
			if err := a.runToolBatch(ctx, rc, result.toolCalls, thread, emit); err != nil {
				emit(Event{Type: EventError, Err: err})
				return err
			}
		}
		emit(Event{Type: EventTurnDone})
		return nil
	})
	return wrapped(ctx, rc, middleware.AgentInput{Messages: thread.Messages})
}

// sampleResult 是一次采样轮的结果。
type sampleResult struct {
	assistant *messages.Message
	toolCalls []*messages.ToolCall
}

// sample 执行一次采样：模型调用（onModelCall 包裹）→ 收集 thinking/text/tool_call。
func (a *Agent) sample(ctx context.Context, in middleware.ReasoningInput, sysPrompt string, emit OnEvent) (*sampleResult, error) {
	var sb strings.Builder
	var calls []*messages.ToolCall

	wrapped := a.mw.WrapModelCall(func(ctx context.Context, rc *middleware.RuntimeContext, min middleware.ModelCallInput) error {
		req := provider.Request{
			Model:        a.model,
			Instructions: sysPrompt,
			Messages:     min.Messages,
			Tools:        min.Tools,
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
				sb.WriteString(ev.Text)
				emit(Event{Type: EventTextDelta, Text: ev.Text})
			case provider.EventThinkingDelta:
				emit(Event{Type: EventThinkingDelta, Text: ev.Text})
			case provider.EventToolCall:
				calls = append(calls, ev.ToolCall)
				emit(Event{Type: EventToolCall, ToolCall: ev.ToolCall})
			case provider.EventDone:
				return nil
			case provider.EventError:
				return ev.Error
			}
		}
		return es.Err()
	})
	if err := wrapped(ctx, nil, middleware.ModelCallInput{Messages: in.Messages, Tools: in.Tools}); err != nil {
		return nil, err
	}

	// 值切片（消息模型存储）与指针切片（执行用）分开。
	toolCallValues := make([]messages.ToolCall, 0, len(calls))
	for _, c := range calls {
		toolCallValues = append(toolCallValues, *c)
	}
	assistant := &messages.Message{
		ID:        fmt.Sprintf("msg_%d", timeNowNanos()),
		Role:      messages.RoleAssistant,
		Content:   sb.String(),
		ToolCalls: toolCallValues,
	}
	return &sampleResult{assistant: assistant, toolCalls: calls}, nil
}

// runToolBatch 并发执行一批工具调用（onToolCall 包裹整批，onActing 包裹单个），
// 结果按 call 顺序回填 thread。首个 Fatal 错误取消整批并终止。
func (a *Agent) runToolBatch(ctx context.Context, rc *middleware.RuntimeContext, calls []*messages.ToolCall, thread *messages.Thread, emit OnEvent) error {
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
			emit(Event{Type: EventToolResult, ToolCall: in.Call, ToolResult: res})
			return nil
		}
		r, err := tool.Handle(ctx, in.Call.ID, in.Call.Args)
		if err != nil {
			var te *tools.ToolError
			if errors.As(err, &te) && te.RespondToModel {
				// RespondToModel：作为失败结果回填，循环继续。
				res := &messages.ToolResult{Success: false, Content: te.Message}
				resultsMu.Lock()
				results[in.Call.ID] = res
				resultsMu.Unlock()
				emit(Event{Type: EventToolResult, ToolCall: in.Call, ToolResult: res})
				return nil
			}
			return fmt.Errorf("工具 %s 执行失败: %w", in.Call.Name, err) // Fatal
		}
		resultsMu.Lock()
		results[in.Call.ID] = &r
		resultsMu.Unlock()
		emit(Event{Type: EventToolResult, ToolCall: in.Call, ToolResult: &r})
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
			thread.Add(messages.NewToolResultsMessage(blocks))
		}
		return firstErr
	})
	return wrapped(ctx, rc, middleware.ToolCallInput{Calls: calls})
}
