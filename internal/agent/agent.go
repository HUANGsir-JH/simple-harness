// Package agent implements the harness agent loop. Phase 1 ships RunOnce
// (single sampling, no tools); phase 2 extends it into the full loop
// (sample → execute tool calls → feed back → re-sample).
package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/provider"
)

// Agent drives a thread through sampling and (later) tool execution.
type Agent struct {
	client provider.Client
	model  string
	tools  []provider.ToolSpec // populated in phase 2
}

// New creates an Agent bound to a provider client and model.
func New(client provider.Client, model string) *Agent {
	return &Agent{client: client, model: model}
}

// RunOnce performs a single sampling pass over the thread and returns the
// assistant message produced. In phase 1 there are no tools: the sampling is
// finished as soon as the model stops producing text. Text deltas are emitted
// to the optional onDelta callback as they arrive (nil disables).
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
			// Phase 2: execute tool and feed the result back.
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
