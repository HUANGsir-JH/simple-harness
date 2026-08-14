package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestKeyBindingsWellFormed 键位表完整性（ADR-043）：每个绑定必有匹配方式与
// 非空动作；composer/ask 上下文末位为兜底绑定（任意按键不丢失——重构前
// handleComposerKey/handleAskKey 的 default 分支语义）。
func TestKeyBindingsWellFormed(t *testing.T) {
	if len(keyBindings) == 0 {
		t.Fatal("键位表为空")
	}
	for ctx, list := range keyBindings {
		if len(list) == 0 {
			t.Fatalf("上下文 %d 无绑定", ctx)
		}
		for i, b := range list {
			if b.fn == nil {
				t.Fatalf("上下文 %d 第 %d 条绑定缺动作", ctx, i)
			}
			if len(b.keys) == 0 && b.is == nil {
				t.Fatalf("上下文 %d 第 %d 条绑定无匹配方式", ctx, i)
			}
		}
	}
	for _, ctx := range []keyContext{ctxComposer, ctxAsk} {
		last := keyBindings[ctx][len(keyBindings[ctx])-1]
		if last.is == nil || !last.is(tea.KeyMsg{}) {
			t.Fatalf("上下文 %d 末位应为兜底绑定", ctx)
		}
	}
}

// TestDispatchKeyHelpOverlayClose 锚定 help 弹窗键位（esc/enter// 关闭）。
func TestDispatchKeyHelpOverlayClose(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyEsc},
		{Type: tea.KeyEnter},
		{Type: tea.KeyRunes, Runes: []rune{'/'}},
	} {
		m := New(nil)
		m.ovl = &overlay{kind: overlayHelp}
		nm, _, handled := dispatchKey(ctxHelp, &m, key)
		if !handled || nm.ovl != nil {
			t.Fatalf("help 弹窗按 %q 应关闭（handled=%v ovl=%v）", key.String(), handled, nm.ovl)
		}
	}
}

// TestDispatchKeyComposerPriority 锚定 composer 上下文判定顺序（ADR-043 行为
// 零变化）：输入非空时 ↑ 不回忆历史；补全可见时 ↑ 移动补全高亮。
func TestDispatchKeyComposerPriority(t *testing.T) {
	m := New(nil)
	m.input.SetValue("hello")
	before := m.historyPos
	if _, _, handled := dispatchKey(ctxComposer, &m, tea.KeyMsg{Type: tea.KeyUp}); !handled {
		t.Fatal("↑ 应被 composer 上下文处理")
	}
	if m.historyPos != before {
		t.Fatalf("输入非空时 ↑ 不应触发历史回忆（historyPos %d → %d）", before, m.historyPos)
	}
	if m.input.Value() != "hello" {
		t.Fatalf("输入非空时 ↑ 不应改动输入内容，got %q", m.input.Value())
	}
}
