package main

import (
	"encoding/json"
	"fmt"

	"github.com/agent-project/harness/internal/messages"
)

// output is a minimal renderer abstraction (full ui.Renderer lands in phase 5;
// per ADR-008 the CLI only needs a Start/delta/finish shape for phase 1).
type output interface {
	start(t *messages.Thread)
	delta(text string)
	finish(m *messages.Message)
}

// --- text renderer (default) ------------------------------------------------

type textRenderer struct{}

func (textRenderer) start(*messages.Thread) {}
func (textRenderer) delta(text string)      { fmt.Print(text) }
func (textRenderer) finish(m *messages.Message) {
	if m.Content != "" {
		fmt.Println()
	}
}

// --- JSON renderer (--json: machine-readable events as JSONL) ----------------

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
