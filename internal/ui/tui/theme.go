package tui

import (
	"sync"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Token 是语义样式角色（ADR-043：颜色只表达语义，不随意自定义 RGB）。
type Token int

const (
	TokenCanvas      Token = iota // 屏幕背景
	TokenPanel                    // 面板背景（todo/弹窗）
	TokenRaised                   // 选中/悬浮背景
	TokenBorder                   // 普通边框
	TokenBorderFocus              // 焦点边框
	TokenMuted                    // 次级文字
	TokenText                     // 正文
	TokenAccent                   // 品牌/焦点/用户消息
	TokenUser                     // 用户消息
	TokenSuccess                  // 成功
	TokenWarning                  // 运行中
	TokenError                    // 失败
	TokenDiffAdd                  // diff 新增行
	TokenDiffDelete               // diff 删除行
	TokenDiffMeta                 // diff 元信息行
)

// Theme 是命名调色板：语义 token → 终端颜色；Border 为边框字形（Phase 2 起
// dialog/composer 使用，Phase 1 保留 ASCII 兜底边框）。
type Theme struct {
	Name   string
	Dark   bool
	Colors map[Token]lipgloss.Color
	Border lipgloss.Border
}

// 内置主题（ADR-043）：harness-dark 沿用现有 234 底座精修（默认）；harness-light
// 为浅色终端变体。两主题经 rebuildStyles 派生同一组语义样式变量。
var (
	harnessDark = Theme{
		Name: "harness-dark",
		Dark: true,
		Colors: map[Token]lipgloss.Color{
			TokenCanvas:      lipgloss.Color("234"),
			TokenPanel:       lipgloss.Color("235"),
			TokenRaised:      lipgloss.Color("237"),
			TokenBorder:      lipgloss.Color("240"),
			TokenBorderFocus: lipgloss.Color("81"),
			TokenMuted:       lipgloss.Color("244"),
			TokenText:        lipgloss.Color("252"),
			TokenAccent:      lipgloss.Color("81"),
			TokenUser:        lipgloss.Color("81"),
			TokenSuccess:     lipgloss.Color("78"),
			TokenWarning:     lipgloss.Color("220"),
			TokenError:       lipgloss.Color("203"),
			TokenDiffAdd:     lipgloss.Color("78"),
			TokenDiffDelete:  lipgloss.Color("203"),
			TokenDiffMeta:    lipgloss.Color("244"),
		},
		Border: lipgloss.RoundedBorder(),
	}
	harnessLight = Theme{
		Name: "harness-light",
		Dark: false,
		Colors: map[Token]lipgloss.Color{
			TokenCanvas:      lipgloss.Color("255"),
			TokenPanel:       lipgloss.Color("254"),
			TokenRaised:      lipgloss.Color("252"),
			TokenBorder:      lipgloss.Color("245"),
			TokenBorderFocus: lipgloss.Color("33"),
			TokenMuted:       lipgloss.Color("241"),
			TokenText:        lipgloss.Color("235"),
			TokenAccent:      lipgloss.Color("33"),
			TokenUser:        lipgloss.Color("33"),
			TokenSuccess:     lipgloss.Color("28"),
			TokenWarning:     lipgloss.Color("136"),
			TokenError:       lipgloss.Color("160"),
			TokenDiffAdd:     lipgloss.Color("28"),
			TokenDiffDelete:  lipgloss.Color("160"),
			TokenDiffMeta:    lipgloss.Color("241"),
		},
		Border: lipgloss.RoundedBorder(),
	}
)

var (
	themeMu     sync.RWMutex
	activeTheme = detectTheme() // 进程启动一次探测；测试注入经 setThemeForTest
)

// detectTheme 明暗探测（ADR-043）：非 TTY（单测/管道）时 termenv 探测不到背景
// 色、按暗色判定 → harness-dark，保证测试输出确定性；真实终端浅色背景 → light。
func detectTheme() *Theme {
	if termenv.HasDarkBackground() {
		return &harnessDark
	}
	return &harnessLight
}

// currentTheme 返回当前主题（读侧加锁；主题切换仅测试与未来 /theme 使用）。
func currentTheme() Theme {
	themeMu.RLock()
	defer themeMu.RUnlock()
	return *activeTheme
}

// setThemeForTest 测试注入主题并重建样式（渲染确定性断言用；非并发路径）。
func setThemeForTest(t Theme) {
	themeMu.Lock()
	defer themeMu.Unlock()
	activeTheme = &t
	rebuildStyles(t)
}

// 语义样式全局变量：全部由 rebuildStyles 从当前主题派生，调用点不直接拼颜色。
// （Phase 1 保持与重构前逐字节相同的 dark 值，零视觉变化；主题切换时重建。）
var (
	styleBrand     lipgloss.Style
	styleText      lipgloss.Style
	styleMuted     lipgloss.Style
	styleUser      lipgloss.Style
	styleAssistant lipgloss.Style
	styleSystem    lipgloss.Style
	styleError     lipgloss.Style
	styleSuccess   lipgloss.Style
	styleRunning   lipgloss.Style
	styleAdd       lipgloss.Style
	styleDelete    lipgloss.Style
	styleSelected  lipgloss.Style
	stylePanel     lipgloss.Style
	styleBorder    lipgloss.Style

	// 滚动条样式（ADR-043 §6.2.1）：轨道 = border 淡化，拇指 = accent。
	thumbStyle lipgloss.Style
	trackStyle lipgloss.Style

	// Compatibility aliases used by tool/markdown tests.
	styleSys  lipgloss.Style
	styleDim  lipgloss.Style
	styleErr  lipgloss.Style
	styleOK   lipgloss.Style
	styleHdr  lipgloss.Style
	styleAsst lipgloss.Style
	styleDel  lipgloss.Style

	// 颜色全局变量：同样由主题派生（view/tool 的边框/底色调用点）。
	colorCanvas lipgloss.Color
	colorPanel  lipgloss.Color
	colorRaised lipgloss.Color
	colorBorder lipgloss.Color
	colorMuted  lipgloss.Color
	colorText   lipgloss.Color
	colorCyan   lipgloss.Color
	colorGreen  lipgloss.Color
	colorYellow lipgloss.Color
	colorRed    lipgloss.Color

	asciiBorder = lipgloss.Border{
		Top: "-", Bottom: "-", Left: "|", Right: "|",
		TopLeft: "+", TopRight: "+", BottomLeft: "+", BottomRight: "+",
	}
)

func init() {
	rebuildStyles(currentTheme())
}

// rebuildStyles 从主题重建全部语义样式/颜色变量。渲染只发生在 bubbletea 主
// goroutine，主题切换（测试注入/未来 /theme）与 View 不并发，无需锁。
func rebuildStyles(t Theme) {
	c := func(tok Token) lipgloss.Color { return t.Colors[tok] }

	colorCanvas = c(TokenCanvas)
	colorPanel = c(TokenPanel)
	colorRaised = c(TokenRaised)
	colorBorder = c(TokenBorder)
	colorMuted = c(TokenMuted)
	colorText = c(TokenText)
	colorCyan = c(TokenAccent)
	colorGreen = c(TokenSuccess)
	colorYellow = c(TokenWarning)
	colorRed = c(TokenError)

	styleBrand = lipgloss.NewStyle().Foreground(c(TokenAccent)).Bold(true)
	styleText = lipgloss.NewStyle().Foreground(c(TokenText))
	styleMuted = lipgloss.NewStyle().Foreground(c(TokenMuted))
	styleUser = lipgloss.NewStyle().Foreground(c(TokenUser)).Bold(true)
	styleAssistant = lipgloss.NewStyle().Foreground(c(TokenText)).Bold(true)
	styleSystem = lipgloss.NewStyle().Foreground(c(TokenMuted))
	styleError = lipgloss.NewStyle().Foreground(c(TokenError))
	styleSuccess = lipgloss.NewStyle().Foreground(c(TokenSuccess))
	styleRunning = lipgloss.NewStyle().Foreground(c(TokenWarning))
	styleAdd = lipgloss.NewStyle().Foreground(c(TokenDiffAdd))
	styleDelete = lipgloss.NewStyle().Foreground(c(TokenDiffDelete))
	styleSelected = lipgloss.NewStyle().Background(c(TokenRaised)).Foreground(c(TokenText))
	stylePanel = lipgloss.NewStyle().Background(c(TokenPanel)).Foreground(c(TokenText))
	styleBorder = lipgloss.NewStyle().Foreground(c(TokenBorder))
	thumbStyle = lipgloss.NewStyle().Foreground(c(TokenAccent))
	trackStyle = lipgloss.NewStyle().Foreground(c(TokenBorder)).Faint(true)

	styleSys = styleSystem
	styleDim = styleMuted
	styleErr = styleError
	styleOK = styleSuccess
	styleHdr = styleRunning
	styleAsst = styleAssistant
	styleDel = styleDelete
}
