package provider

// modelWindows 记录已知模型的上下文窗口大小（token）。未知模型
// 回退到 DefaultContextWindow。
var modelWindows = map[string]int{
	// OpenAI（Responses API）
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
	// Anthropic（Messages API）
	"claude-sonnet-4-5":         200000,
	"claude-sonnet-5":           1000000,
	"claude-opus-4":             200000,
	"claude-opus-4-1":           200000,
	"claude-haiku-4-5":          200000,
	"claude-haiku-4-5-20251001": 200000,
}

// DefaultContextWindow 用于模型未知时的默认值。
const DefaultContextWindow = 128000

// ContextWindowFor 返回 model 的已知上下文窗口，未知则返回默认值。
func ContextWindowFor(model string) int {
	if w, ok := modelWindows[model]; ok {
		return w
	}
	return DefaultContextWindow
}
