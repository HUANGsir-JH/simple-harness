package tui

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// withColorProfile 测试期间强制 256 色档案：非 TTY 下 lipgloss 默认 Ascii 档案
// 会剥掉颜色（Render 输出无 ANSI，比较失真），本函数保证颜色断言真实生效。
func withColorProfile(t *testing.T, p termenv.Profile) {
	t.Helper()
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(p)
	t.Cleanup(func() { lipgloss.SetColorProfile(old) })
}

// TestDetectThemeDefaultsDarkInTests 单测进程 stdout 非 TTY：termenv 探测不到
// 背景色 → 按黑（dark）处理，保证无 TTY 测试的渲染确定性（ADR-043）。
func TestDetectThemeDefaultsDarkInTests(t *testing.T) {
	if got := detectTheme(); !got.Dark {
		t.Fatalf("非 TTY 环境应默认 dark 主题，got %q", got.Name)
	}
}

// TestDarkThemeStylesMatchRefactorBaseline 锚定 Phase 1 零视觉变化：harness-dark
// 派生的语义样式/颜色与重构前 view.go 写死的 9 色逐字节一致。任何视觉回归都
// 会让本测试失败（ADR-043 契约）。
func TestDarkThemeStylesMatchRefactorBaseline(t *testing.T) {
	withColorProfile(t, termenv.ANSI256)
	setThemeForTest(harnessDark)
	t.Cleanup(func() { setThemeForTest(harnessDark) })

	styles := []struct {
		name string
		got  lipgloss.Style
		want lipgloss.Style
	}{
		{"brand", styleBrand, lipgloss.NewStyle().Foreground(lipgloss.Color("81")).Bold(true)},
		{"text", styleText, lipgloss.NewStyle().Foreground(lipgloss.Color("252"))},
		{"muted", styleMuted, lipgloss.NewStyle().Foreground(lipgloss.Color("244"))},
		{"user", styleUser, lipgloss.NewStyle().Foreground(lipgloss.Color("81")).Bold(true)},
		{"assistant", styleAssistant, lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Bold(true)},
		{"system", styleSystem, lipgloss.NewStyle().Foreground(lipgloss.Color("244"))},
		{"error", styleError, lipgloss.NewStyle().Foreground(lipgloss.Color("203"))},
		{"success", styleSuccess, lipgloss.NewStyle().Foreground(lipgloss.Color("78"))},
		{"running", styleRunning, lipgloss.NewStyle().Foreground(lipgloss.Color("220"))},
		{"add", styleAdd, lipgloss.NewStyle().Foreground(lipgloss.Color("78"))},
		{"delete", styleDelete, lipgloss.NewStyle().Foreground(lipgloss.Color("203"))},
		{"selected", styleSelected, lipgloss.NewStyle().Background(lipgloss.Color("237")).Foreground(lipgloss.Color("252"))},
		{"panel", stylePanel, lipgloss.NewStyle().Background(lipgloss.Color("235")).Foreground(lipgloss.Color("252"))},
		{"border", styleBorder, lipgloss.NewStyle().Foreground(lipgloss.Color("240"))},
	}
	for _, s := range styles {
		if got, want := s.got.Render("x"), s.want.Render("x"); got != want {
			t.Errorf("%s: got %q want %q", s.name, got, want)
		}
	}

	colors := []struct {
		name string
		got  lipgloss.Color
		want lipgloss.Color
	}{
		{"canvas", colorCanvas, lipgloss.Color("234")},
		{"panel", colorPanel, lipgloss.Color("235")},
		{"raised", colorRaised, lipgloss.Color("237")},
		{"border", colorBorder, lipgloss.Color("240")},
		{"muted", colorMuted, lipgloss.Color("244")},
		{"text", colorText, lipgloss.Color("252")},
		{"cyan", colorCyan, lipgloss.Color("81")},
		{"green", colorGreen, lipgloss.Color("78")},
		{"yellow", colorYellow, lipgloss.Color("220")},
		{"red", colorRed, lipgloss.Color("203")},
	}
	for _, c := range colors {
		if c.got != c.want {
			t.Errorf("%s: got %q want %q", c.name, c.got, c.want)
		}
	}

	// 兼容别名与主样式一致（tool/markdown 测试依赖）。
	aliases := []struct {
		name string
		got  lipgloss.Style
		want lipgloss.Style
	}{
		{"sys", styleSys, styleSystem},
		{"dim", styleDim, styleMuted},
		{"err", styleErr, styleError},
		{"ok", styleOK, styleSuccess},
		{"hdr", styleHdr, styleRunning},
		{"asst", styleAsst, styleAssistant},
		{"del", styleDel, styleDelete},
	}
	for _, a := range aliases {
		if got, want := a.got.Render("x"), a.want.Render("x"); got != want {
			t.Errorf("%s 别名: got %q want %q", a.name, got, want)
		}
	}
}

// TestThemeSwitchRebuildsStyles 主题切换重建样式并保持 currentTheme 一致。
func TestThemeSwitchRebuildsStyles(t *testing.T) {
	withColorProfile(t, termenv.ANSI256)
	setThemeForTest(harnessDark)
	t.Cleanup(func() { setThemeForTest(harnessDark) })
	darkText := styleText.Render("x")

	setThemeForTest(harnessLight)
	if currentTheme().Name != "harness-light" {
		t.Fatalf("currentTheme 应为 harness-light，got %q", currentTheme().Name)
	}
	if lightText := styleText.Render("x"); lightText == darkText {
		t.Fatal("light 主题应派生与 dark 不同的样式")
	}

	setThemeForTest(harnessDark)
	if got, want := styleText.Render("x"), darkText; got != want {
		t.Fatalf("切回 dark 后样式应恢复，got %q want %q", got, want)
	}
}
