// Package agent 实现 harness 的 agent 循环。阶段一提供 RunOnce
// （单次采样，无工具）；阶段二将其扩展为完整循环
// （采样 → 执行工具调用 → 回填 → 再次采样）。
package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/provider"
)

// Agent 驱动一个 thread 进行采样及（后续的）工具执行。
type Agent struct {
	client provider.Client
	model  string
	tools  []provider.ToolSpec // 阶段二填充
}

// New 创建一个绑定到 provider 客户端与模型的 Agent。
func New(client provider.Client, model string) *Agent {
	return &Agent{client: client, model: model}
}

// RunOnce 对 thread 执行一次采样并返回产生的助手消息。阶段一没有工具：
// 只要模型停止产出文本，采样即结束。文本增量实时发给可选的
// onDelta 回调（传 nil 表示禁用）。
func (a *Agent) RunOnce(ctx context.Context, thread *messages.Thread, onDelta func(string)) (*messages.Message, error) {
	events, err := a.client.Stream(ctx, provider.Request{
		Model:        a.model,
		Instructions: "You are a helpful coding agent.",
		Messages:     thread.Messages,
		Tools:        a.tools,
	})
	if err != nil {
		return nil, fmt.Errorf("agent: start stream: %w", err)
	}
	defer events.Close()

	var sb strings.Builder
	for events.Next() {
		ev := events.Current()
		switch ev.Type {
		case provider.EventTextDelta:
			sb.WriteString(ev.Text)
			if onDelta != nil {
				onDelta(ev.Text)
			}
		case provider.EventToolCall:
			// 阶段二：执行工具并将结果回填。
			continue
		case provider.EventError:
			return nil, ev.Error
		case provider.EventDone:
			return assistantMessage(sb.String()), nil
		}
	}
	if err := events.Err(); err != nil {
		return nil, fmt.Errorf("agent: stream: %w", err)
	}
	return assistantMessage(sb.String()), nil
}

func assistantMessage(content string) *messages.Message {
	return &messages.Message{
		ID:      fmt.Sprintf("msg_%d", timeNowNanos()),
		Role:    messages.RoleAssistant,
		Content: content,
	}
}
