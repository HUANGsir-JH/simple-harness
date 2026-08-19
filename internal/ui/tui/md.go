package tui

import (
	"sync"

	"github.com/charmbracelet/glamour"
	glamouransi "github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
)

// renderMarkdown 用 glamour 渲染 markdown → ANSI（ADR-030：流式纯文本 →
// 块完成时渲染完整 markdown；每块渲染一次，非逐 token）。渲染器按终端宽缓存。
func renderMarkdown(s string, width int) string {
	if s == "" {
		return ""
	}
	r := mdRendererFor(width)
	out, err := r.Render(s)
	if err != nil {
		return s // 渲染失败回退原始文本
	}
	return out
}

var (
	mdMu     sync.Mutex
	mdRender = map[int]*glamour.TermRenderer{}
)

// mdRendererFor 按宽度取（或建）glamour 渲染器。基于 dark 风格定制
// （AutoStyle 需终端探测，内存渲染下不确定）。
func mdRendererFor(width int) *glamour.TermRenderer {
	if width < 20 {
		width = 80
	}
	mdMu.Lock()
	defer mdMu.Unlock()
	if r, ok := mdRender[width]; ok {
		return r
	}
	r, err := glamour.NewTermRenderer(glamour.WithStyles(markdownStyle()), glamour.WithWordWrap(width))
	if err != nil {
		r, _ = glamour.NewTermRenderer(glamour.WithStandardStyle("dark"))
	}
	mdRender[width] = r
	return r
}

// markdownStyle keeps fenced blocks neutral. Chroma guesses a lexer when the
// fence has no language; prose-heavy directory trees are then misclassified as
// source and ordinary Chinese text is rendered with the error-token background.
func markdownStyle() glamouransi.StyleConfig {
	style := styles.DarkStyleConfig
	style.CodeBlock.Chroma = nil
	style.CodeBlock.Theme = ""
	return style
}
