package main

import (
	"encoding/json"
	"fmt"

	"github.com/agent-project/harness/internal/messages"
)

// output 是最小渲染器抽象（完整 ui.Renderer 在阶段五引入；
// 依据 ADR-008，阶段一 CLI 只需 Start/delta/finish 形态）。
type output interface {
	start(t *messages.Thread)
	delta(text string)
	finish(m *messages.Message)
}

// --- 文本渲染器（默认）-----------------------------------------------------

type textRenderer struct{}

func (textRenderer) start(*messages.Thread) {}
func (textRenderer) delta(text string)      { fmt.Print(text) }
func (textRenderer) finish(m *messages.Message) {
	if m.Content != "" {
		fmt.Println()
	}
}

// --- JSON 渲染器（--json：机器可读的 JSONL 事件）-----------------------------

type jsonEvent struct {
	Type    string `json:"type"`
	Text    string `json:"text,omitempty"`
	Message string `json:"message,omitempty"`
}

type jsonRenderer struct{}

func (jsonRenderer) start(t *messages.Thread) {
	emitJSON(jsonEvent{Type: "thread_start", Message: t.ID})
}
func (jsonRenderer) delta(text string) {
	emitJSON(jsonEvent{Type: "text_delta", Text: text})
}
func (jsonRenderer) finish(m *messages.Message) {
	emitJSON(jsonEvent{Type: "assistant_message", Message: m.Content})
}

func emitJSON(ev jsonEvent) {
	b, _ := json.Marshal(ev)
	fmt.Println(string(b))
}
