package ui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/agent-project/harness/internal/events"
	"github.com/agent-project/harness/internal/messages"
)

// Output 是最小渲染器抽象（完整 ui.Renderer 在阶段五引入；
// 阶段二升级为订阅 agent 回合级事件，含 thinking/turn_done）。
type Output interface {
	Start(t *messages.Conversation)
	Event(ev events.Event)
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

type TextRenderer struct {
	showThinking bool
}

func NewTextRenderer(showThinking bool) *TextRenderer {
	return &TextRenderer{showThinking: showThinking}
}

func (r *TextRenderer) Start(*messages.Conversation) {}

func (r *TextRenderer) Event(ev events.Event) {
	switch ev.Type {
	case events.EventThinkingDelta:
		if !r.showThinking {
			return
		}
		fmt.Printf("%s%s%s", ansiDim, ev.Text, ansiReset)
	case events.EventTextDelta:
		fmt.Print(ev.Text)
	case events.EventToolCall:
		fmt.Printf("\n%s[工具] %s %s%s", ansiYellow, ev.ToolCall.Name, argsSummary(ev.ToolCall.Args), ansiReset)
	case events.EventToolResult:
		if ev.ToolResult.Success {
			fmt.Printf("\n%s[工具结果] %s%s", ansiGreen, toolResultSummary(ev.ToolResult.Content), ansiReset)
		} else {
			fmt.Printf("\n%s[工具失败] %s%s", ansiRed, toolResultSummary(ev.ToolResult.Content), ansiReset)
		}
	case events.EventTurnDone:
		fmt.Println()
	case events.EventError:
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

type JSONRenderer struct{}

func (JSONRenderer) Start(t *messages.Conversation) {
	emitJSON(map[string]any{"type": "conversation_start", "conversation": t.ID})
}

func (JSONRenderer) Event(ev events.Event) {
	switch ev.Type {
	case events.EventThinkingDelta:
		emitJSON(map[string]any{"type": "thinking_delta", "text": ev.Text})
	case events.EventTextDelta:
		emitJSON(map[string]any{"type": "text_delta", "text": ev.Text})
	case events.EventToolCall:
		emitJSON(map[string]any{"type": "tool_call", "name": ev.ToolCall.Name, "args": string(ev.ToolCall.Args)})
	case events.EventToolResult:
		emitJSON(map[string]any{"type": "tool_result", "success": ev.ToolResult.Success, "content": strings.TrimSpace(ev.ToolResult.Content)})
	case events.EventTurnDone:
		emitJSON(map[string]any{"type": "turn_done"})
	case events.EventUsage:
		// 评测/用量审计需要 usage 进 --json 流（2026-08-20：评测轨迹成本统计）。
		if ev.Usage != nil && !ev.Usage.IsZero() {
			emitJSON(map[string]any{"type": "usage", "usage": ev.Usage})
		}
	case events.EventError:
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
