package provider

import (
	"testing"
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
