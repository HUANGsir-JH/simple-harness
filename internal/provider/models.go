package provider

// modelWindows holds known context window sizes (tokens). Unknown models
// default to DefaultContextWindow.
var modelWindows = map[string]int{
	// OpenAI (Responses API)
	"gpt-4o":       128000,
	"gpt-4o-mini":  128000,
	"gpt-4.1":      1047576,
	"gpt-4.1-mini": 1047576,
	"o3":           200000,
	"o3-mini":      200000,
	"o4-mini":      200000,
	"gpt-5":        400000,
	"gpt-5-mini":   400000,
	"gpt-5.1":      400000,
	"gpt-5.2":      400000,
	// Anthropic (Messages API)
	"claude-sonnet-4-5":         200000,
	"claude-sonnet-5":           1000000,
	"claude-opus-4":             200000,
	"claude-opus-4-1":           200000,
	"claude-haiku-4-5":          200000,
	"claude-haiku-4-5-20251001": 200000,
}

// DefaultContextWindow applies when a model is unknown.
const DefaultContextWindow = 128000

// ContextWindowFor returns the known context window for model, or the default.
func ContextWindowFor(model string) int {
	if w, ok := modelWindows[model]; ok {
		return w
	}
	return DefaultContextWindow
}
