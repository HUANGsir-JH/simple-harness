package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/agent-project/harness/internal/agent"
	"github.com/agent-project/harness/internal/messages"
)

// output 是最小渲染器抽象（完整 ui.Renderer 在阶段五引入；
// 阶段二升级为订阅 agent 回合级事件，含 thinking/turn_done）。
type output interface {
	start(t *messages.Conversation)
	event(ev agent.Event)
}

// ANSI 颜色（simple 渲染器；TUI 阶段六插拔）。
const (
	ansiReset  = "\x1b[0m"
	ansiDim    = "\x1b[2m" // thinking 灰显
	ansiYellow = "\x1b[33m"
	ansiGreen  = "\x1b[32m"
	ansiRed    = "\x1b[31m"
)

// --- 文本渲染器（默认）-----------------------------------------------------

type textRenderer struct {
	showThinking bool
}

func newTextRenderer(showThinking bool) *textRenderer {
	return &textRenderer{showThinking: showThinking}
}

func (r *textRenderer) start(*messages.Conversation) {}

func (r *textRenderer) event(ev agent.Event) {
	switch ev.Type {
	case agent.EventThinkingDelta:
		if !r.showThinking {
			return
		}
		fmt.Printf("%s%s%s", ansiDim, ev.Text, ansiReset)
	case agent.EventTextDelta:
		fmt.Print(ev.Text)
	case agent.EventToolCall:
		fmt.Printf("\n%s[工具] %s %s%s", ansiYellow, ev.ToolCall.Name, argsSummary(ev.ToolCall.Args), ansiReset)
	case agent.EventToolResult:
		if ev.ToolResult.Success {
			fmt.Printf("\n%s[工具结果] %s%s", ansiGreen, toolResultSummary(ev.ToolResult.Content), ansiReset)
		} else {
			fmt.Printf("\n%s[工具失败] %s%s", ansiRed, toolResultSummary(ev.ToolResult.Content), ansiReset)
		}
	case agent.EventTurnDone:
		fmt.Println()
	case agent.EventError:
		// 回合级错误由调用方处理；此处避免重复打印。
	}
}

// argsSummary 截断工具参数供展示。
func argsSummary(args []byte) string {
	s := string(args)
	if len(s) > 80 {
		return s[:80] + "…"
	}
	return s
}

// toolResultSummary 截断工具结果供展示。
func toolResultSummary(s string) string {
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}

// --- JSON 渲染器（--json：机器可读的 JSONL 事件）-----------------------------

type jsonRenderer struct{}

func (jsonRenderer) start(t *messages.Conversation) {
	emitJSON(map[string]any{"type": "conversation_start", "conversation": t.ID})
}

func (jsonRenderer) event(ev agent.Event) {
	switch ev.Type {
	case agent.EventThinkingDelta:
		emitJSON(map[string]any{"type": "thinking_delta", "text": ev.Text})
	case agent.EventTextDelta:
		emitJSON(map[string]any{"type": "text_delta", "text": ev.Text})
	case agent.EventToolCall:
		emitJSON(map[string]any{"type": "tool_call", "name": ev.ToolCall.Name, "args": string(ev.ToolCall.Args)})
	case agent.EventToolResult:
		emitJSON(map[string]any{"type": "tool_result", "success": ev.ToolResult.Success, "content": strings.TrimSpace(ev.ToolResult.Content)})
	case agent.EventTurnDone:
		emitJSON(map[string]any{"type": "turn_done"})
	case agent.EventError:
		emitJSON(map[string]any{"type": "error", "message": ev.Err.Error()})
	}
}

func emitJSON(ev map[string]any) {
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	fmt.Println(string(b))
}
