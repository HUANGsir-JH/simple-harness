package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/agent-project/harness/internal/messages"
)

func TestDefaultEnvKey(t *testing.T) {
	if got := DefaultEnvKey(WireOpenAI); got != "OPENAI_API_KEY" {
		t.Errorf("openai: got %q", got)
	}
	if got := DefaultEnvKey(WireAnthropic); got != "ANTHROPIC_API_KEY" {
		t.Errorf("anthropic: got %q", got)
	}
}

func TestContextWindowFor(t *testing.T) {
	cases := []struct {
		model string
		want  int
	}{
		{"gpt-4o", 128000},
		{"gpt-5.2", 400000},
		{"claude-sonnet-5", 1000000},
		{"unknown-model-xyz", DefaultContextWindow},
	}
	for _, c := range cases {
		if got := ContextWindowFor(c.model); got != c.want {
			t.Errorf("ContextWindowFor(%q) = %d, want %d", c.model, got, c.want)
		}
	}
}

// TestFactoryMissingKey verifies NewClient errors without an API key.
func TestFactoryMissingKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	if _, err := NewClient(Config{Provider: "openai", Model: "gpt-4o"}); err == nil {
		t.Fatal("expected error when API key missing")
	}
	if _, err := NewClient(Config{Provider: "anthropic", Model: "claude-sonnet-5"}); err == nil {
		t.Fatal("expected error when API key missing")
	}
}

// TestFactoryUnknownProvider verifies unknown provider names are rejected.
func TestFactoryUnknownProvider(t *testing.T) {
	if _, err := NewClient(Config{Provider: "gemini", Model: "x"}); err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

// TestFactoryMissingModel verifies model is required.
func TestFactoryMissingModel(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	if _, err := NewClient(Config{Provider: "openai"}); err == nil {
		t.Fatal("expected error when model missing")
	}
}

// --- shared test doubles -------------------------------------------------

// FakeStream is a scripted EventStream used by the agent layer tests.
type FakeStream struct {
	events []Event
	idx    int
	err    error
}

func NewFakeStream(events []Event) *FakeStream { return &FakeStream{events: events} }

func (f *FakeStream) Next() bool {
	if f.idx < len(f.events) {
		f.idx++
		return true
	}
	return false
}
func (f *FakeStream) Current() Event {
	if f.idx == 0 || f.idx > len(f.events) {
		return Event{Type: EventError, Error: errors.New("Current called before Next")}
	}
	return f.events[f.idx-1]
}
func (f *FakeStream) Err() error   { return f.err }
func (f *FakeStream) Close() error { return nil }

// FakeClient is a scripted Client used by the agent layer tests. It records
// the last request for assertion and returns the configured stream.
type FakeClient struct {
	StreamFn func(ctx context.Context, req Request) (EventStream, error)
	LastReq  *Request
}

func (f *FakeClient) Stream(ctx context.Context, req Request) (EventStream, error) {
	f.LastReq = &req
	if f.StreamFn == nil {
		return NewFakeStream(nil), nil
	}
	return f.StreamFn(ctx, req)
}

// Ensure test doubles satisfy the public interfaces.
var _ EventStream = (*FakeStream)(nil)
var _ Client = (*FakeClient)(nil)
var _ = messages.RoleUser // keep the messages import linked in tests of this package
