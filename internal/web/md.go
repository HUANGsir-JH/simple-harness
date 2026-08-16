package web

import (
	"bytes"
	"strings"

	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/util"
)

// renderHTML 把 markdown 渲染为 HTML 片段（后端渲染，前端零依赖；feat/webui
// 阶段）。goldmark（glamour 底层，go.sum 已有）+ chroma 代码高亮。安全：
// 未开启 WithUnsafe——原始 HTML 节点被丢弃（防注入）；输出仅来自模型文本。
//
// 渲染失败回退原始文本（前端按 textContent 展示）。
func renderHTML(md string) string {
	if md == "" {
		return ""
	}
	var buf bytes.Buffer
	if err := mdRenderer().Convert([]byte(md), &buf); err != nil {
		return md
	}
	return buf.String()
}

// mdRenderer 构建 goldmark 渲染器（GFM 扩展 + chroma 代码块高亮，CSS
// classes 输出）。渲染低频（text_done/thinking_done 一次），不做缓存。
func mdRenderer() goldmark.Markdown {
	return goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
		goldmark.WithRendererOptions(
			renderer.WithNodeRenderers(util.Prioritized(&codeBlockRenderer{}, 100)),
		),
	)
}

// codeBlockRenderer 是 chroma 代码高亮的节点渲染器（对位 glamour 的
// chroma 集成，但输出 HTML 而非 ANSI）。带语言标记的 fenced 块用 chroma
// 高亮（CSS classes）；无语言块回退默认 <pre><code>。
type codeBlockRenderer struct{}

func (r *codeBlockRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindCodeBlock, r.render)
	reg.Register(ast.KindFencedCodeBlock, r.render)
}

func (r *codeBlockRenderer) render(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	var lang string
	var textBuf []byte
	switch n := node.(type) {
	case *ast.FencedCodeBlock:
		lang = string(n.Language(source))
		lines := n.Lines()
		for i := 0; i < lines.Len(); i++ {
			seg := lines.At(i)
			textBuf = append(textBuf, seg.Value(source)...)
		}
	case *ast.CodeBlock:
		lines := n.Lines()
		for i := 0; i < lines.Len(); i++ {
			seg := lines.At(i)
			textBuf = append(textBuf, seg.Value(source)...)
		}
	}
	if lang == "" {
		// 无语言标记：默认 pre/code（转义）。
		escaped := escapeHTML(string(textBuf))
		w.WriteString(`<div class="code-block"><pre><code>` + escaped + "</code></pre></div>")
		return ast.WalkSkipChildren, nil
	}
	lexer := lexers.Get(lang)
	if lexer == nil {
		lexer = lexers.Get("text")
	}
	if lexer == nil {
		lexer = lexers.Fallback
	}
	iterator, err := lexer.Tokenise(nil, string(textBuf))
	if err != nil {
		return ast.WalkSkipChildren, nil
	}
	style := styles.Get("github")
	if style == nil {
		style = styles.Fallback
	}
	w.WriteString(`<div class="code-block"><pre class="highlight">`)
	if err := chromahtml.New(chromahtml.WithClasses(true)).Format(w, style, iterator); err != nil {
		return ast.WalkSkipChildren, nil
	}
	w.WriteString(`</pre></div>`)
	return ast.WalkSkipChildren, nil
}

// escapeHTML 转义 HTML 特殊字符（无语言代码块输出用）。
func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
