package tui

import (
	"strings"

	"github.com/agent-project/harness/internal/middleware"
	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
)

// keyContext 是键位作用域（ADR-043：键位收拢到集中表，行为零变化）。
// 每个上下文一张有序绑定表，首条匹配且 handled 的绑定结束分发；
// handled=false 表示守卫条件不满足（如 home 仅在时间线焦点生效），继续试后续绑定。
type keyContext uint8

const (
	ctxGlobal keyContext = iota // 无弹窗、与焦点无关的全局键
	ctxComposer                 // 输入框焦点
	ctxTimeline                 // 时间线焦点
	ctxApproval                 // 审批弹窗
	ctxAsk                      // 提问弹窗
	ctxSelect                   // 选择器弹窗（/switch /model /effort /permission /thinking）
	ctxHelp                     // 帮助弹窗
)

// overlayContext 弹窗类型 → 键位上下文。
func overlayContext(k overlayKind) keyContext {
	switch k {
	case overlayApproval:
		return ctxApproval
	case overlaySelect:
		return ctxSelect
	case overlayAsk:
		return ctxAsk
	default:
		return ctxHelp
	}
}

// keyFn 是键位动作：handled=false 时继续尝试后续绑定（含全局→焦点上下文的降级）。
type keyFn func(m *Model, msg tea.KeyMsg) (handled bool, cmd tea.Cmd)

// keyBinding 是单条键位绑定：keys 命中 msg.String()，或 is 自定义匹配器
// （如 enter 需排除 alt/shift 变体）满足时执行 fn。
type keyBinding struct {
	keys []string
	is   func(tea.KeyMsg) bool
	fn   keyFn
}

func (b keyBinding) matches(msg tea.KeyMsg) bool {
	if b.is != nil {
		return b.is(msg)
	}
	for _, k := range b.keys {
		if msg.String() == k {
			return true
		}
	}
	return false
}

// keyIs 大小写不敏感匹配（审批 y/s/n 支持按住 shift 输入大写）。
func keyIs(k string) func(tea.KeyMsg) bool {
	return func(msg tea.KeyMsg) bool { return strings.ToLower(msg.String()) == k }
}

// isEnterSubmit 纯 Enter（非 alt/shift 变体）才提交（原 handleComposerKey 语义）。
func isEnterSubmit(msg tea.KeyMsg) bool { return msg.Type == tea.KeyEnter && !msg.Alt }

// keyBindings 是全部用户可见键位的单一事实来源（ADR-043）。表内顺序即分发
// 优先级——与重构前 handleKey 系列 switch 的判定顺序逐字节等价（Phase 1 验收：
// 单测零改动全绿）。各上下文无匹配绑定时的默认行为：composer = 输入进 textarea
// （表中兜底项）；其余 = 忽略。
var keyBindings = map[keyContext][]keyBinding{
	ctxGlobal: {
		{keys: []string{"ctrl+c"}, fn: copyComposerKey},
		{keys: []string{"esc"}, fn: interruptKey},
		{keys: []string{"tab"}, fn: tabKey},
		{keys: []string{"pgup"}, fn: pageUpKey},
		{keys: []string{"pgdown"}, fn: pageDownKey},
		{keys: []string{"home"}, fn: homeKey},
		{keys: []string{"end"}, fn: endKey},
	},
	ctxComposer: {
		{keys: []string{"up", "down"}, fn: completionNavKey},
		{keys: []string{"up", "down"}, fn: historyRecallKey},
		{keys: []string{"shift+enter", "alt+enter"}, fn: newlineKey},
		{is: isEnterSubmit, fn: submitKey},
		{is: func(tea.KeyMsg) bool { return true }, fn: defaultComposerKey},
	},
	ctxTimeline: {
		{keys: []string{"up", "k"}, fn: timelineUpKey},
		{keys: []string{"down", "j"}, fn: timelineDownKey},
		{keys: []string{"enter", "space"}, fn: timelineToggleKey},
	},
	ctxApproval: {
		{is: keyIs("y"), fn: approvalAllowKey},
		{is: keyIs("s"), fn: approvalSessionKey},
		{is: keyIs("n"), fn: approvalDenyKey},
		{is: keyIs("esc"), fn: approvalEscKey},
	},
	ctxAsk: {
		{keys: []string{"up"}, fn: askUpKey},
		{keys: []string{"down"}, fn: askDownKey},
		{keys: []string{" "}, fn: askSpaceKey},
		{keys: []string{"enter"}, fn: askEnterKey},
		{keys: []string{"esc"}, fn: askEscKey},
		{keys: []string{"backspace"}, fn: askBackspaceKey},
		{keys: []string{"tab", "shift+enter", "alt+enter", "ctrl+c", "pgup", "pgdown", "home", "end"}, fn: askIgnoreKey},
		{is: func(tea.KeyMsg) bool { return true }, fn: askCustomKey},
	},
	ctxSelect: {
		{keys: []string{"up", "k"}, fn: selectUpKey},
		{keys: []string{"down", "j"}, fn: selectDownKey},
		{keys: []string{"enter"}, fn: selectEnterKey},
		{keys: []string{"esc"}, fn: selectEscKey},
	},
	ctxHelp: {
		{keys: []string{"esc", "enter", "/"}, fn: helpCloseKey},
	},
}

// dispatchKey 按上下文查表分发；返回最终 Model、cmd 与是否被任一绑定处理。
func dispatchKey(ctx keyContext, m *Model, msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	for _, b := range keyBindings[ctx] {
		if !b.matches(msg) {
			continue
		}
		handled, cmd := b.fn(m, msg)
		if handled {
			return *m, cmd, true
		}
	}
	return *m, nil, false
}

// ---- ctxGlobal 绑定动作 ----

// copyComposerKey Ctrl+C：复制 composer 全文到系统剪贴板（不可用时 toast 提示）。
func copyComposerKey(m *Model, _ tea.KeyMsg) (bool, tea.Cmd) {
	if value := m.input.Value(); value != "" {
		if err := clipboard.WriteAll(value); err == nil {
			m.toast = "Composer copied"
		} else {
			m.toast = "Clipboard unavailable"
		}
		m.refresh(false)
	}
	return true, nil
}

// interruptKey Esc：运行中请求中断当前回合。
func interruptKey(m *Model, _ tea.KeyMsg) (bool, tea.Cmd) {
	if m.running && m.c != nil {
		m.requestInterrupt()
	}
	return true, nil
}

// tabKey Tab：补全可见时接受补全，否则切换焦点（输入区↔时间线）。
func tabKey(m *Model, _ tea.KeyMsg) (bool, tea.Cmd) {
	if m.completionVisible() {
		m.acceptCompletion()
		return true, nil
	}
	m.toggleFocus()
	m.refresh(false)
	return true, nil
}

// pageUpKey PgUp：时间线向上翻页并脱离贴底。
func pageUpKey(m *Model, _ tea.KeyMsg) (bool, tea.Cmd) {
	m.viewport.PageUp()
	m.autoScroll = false
	return true, nil
}

// pageDownKey PgDn：时间线向下翻页；到底则恢复贴底。
func pageDownKey(m *Model, _ tea.KeyMsg) (bool, tea.Cmd) {
	m.viewport.PageDown()
	m.autoScroll = m.viewport.AtBottom()
	return true, nil
}

// homeKey Home：时间线焦点时回到顶部（否则不处理，降级到焦点上下文）。
func homeKey(m *Model, _ tea.KeyMsg) (bool, tea.Cmd) {
	if m.focus == focusTimeline {
		m.viewport.GotoTop()
		m.autoScroll = false
		return true, nil
	}
	return false, nil
}

// endKey End：时间线焦点时跳到底部并恢复贴底（否则降级）。
func endKey(m *Model, _ tea.KeyMsg) (bool, tea.Cmd) {
	if m.focus == focusTimeline {
		m.viewport.GotoBottom()
		m.autoScroll = true
		return true, nil
	}
	return false, nil
}

// ---- ctxComposer 绑定动作（顺序即原 handleComposerKey 判定顺序）----

// completionNavKey ↑/↓：补全列表可见时移动补全高亮。
func completionNavKey(m *Model, msg tea.KeyMsg) (bool, tea.Cmd) {
	if !m.completionVisible() {
		return false, nil
	}
	switch msg.String() {
	case "up":
		m.moveCompletion(-1)
	case "down":
		m.moveCompletion(1)
	}
	return true, nil
}

// historyRecallKey ↑/↓：输入为空时回忆输入历史。
func historyRecallKey(m *Model, msg tea.KeyMsg) (bool, tea.Cmd) {
	if m.input.Value() != "" {
		return false, nil
	}
	switch msg.String() {
	case "up":
		m.recallHistory(-1)
	case "down":
		m.recallHistory(1)
	}
	return true, nil
}

// newlineKey Shift/Alt+Enter：插入换行（不提交）。
func newlineKey(m *Model, _ tea.KeyMsg) (bool, tea.Cmd) {
	m.input.InsertRune('\n')
	m.updateComposerHeight()
	m.refresh(false)
	return true, nil
}

// submitKey Enter：补全可见接受补全，否则提交输入。
func submitKey(m *Model, _ tea.KeyMsg) (bool, tea.Cmd) {
	if m.completionVisible() {
		m.acceptCompletion()
		return true, nil
	}
	nm, cmd := m.submit()
	*m = nm.(Model)
	return true, cmd
}

// defaultComposerKey 兜底：其余按键进 textarea 并重置历史/补全状态。
func defaultComposerKey(m *Model, msg tea.KeyMsg) (bool, tea.Cmd) {
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.historyPos = -1
	m.completion = normalizeCompletion(m.input.Value(), m.completion)
	m.updateComposerHeight()
	m.refresh(false)
	return true, cmd
}

// ---- ctxTimeline 绑定动作 ----

func timelineUpKey(m *Model, _ tea.KeyMsg) (bool, tea.Cmd) {
	m.moveSelectedHit(-1)
	return true, nil
}

func timelineDownKey(m *Model, _ tea.KeyMsg) (bool, tea.Cmd) {
	m.moveSelectedHit(1)
	return true, nil
}

func timelineToggleKey(m *Model, _ tea.KeyMsg) (bool, tea.Cmd) {
	m.toggleSelectedHit()
	return true, nil
}

// ---- ctxApproval 绑定动作（y/s/n/esc，大小写不敏感）----

// approvalDecide 回送决策并关闭弹窗（closeOverlay 统一收尾，含待决请求弹出）。
func approvalDecide(m *Model, decision middleware.Decision) (bool, tea.Cmd) {
	m.ovl.appr.respCh <- decision
	nm, cmd := m.closeOverlay()
	*m = nm
	return true, cmd
}

func approvalAllowKey(m *Model, _ tea.KeyMsg) (bool, tea.Cmd) {
	return approvalDecide(m, middleware.DecisionAllow)
}

func approvalSessionKey(m *Model, _ tea.KeyMsg) (bool, tea.Cmd) {
	return approvalDecide(m, middleware.DecisionAllowSession)
}

func approvalDenyKey(m *Model, _ tea.KeyMsg) (bool, tea.Cmd) {
	return approvalDecide(m, middleware.DecisionDeny)
}

// approvalEscKey Esc：拒绝 + 运行中同时请求中断（原 handleApprovalKey 语义）。
func approvalEscKey(m *Model, _ tea.KeyMsg) (bool, tea.Cmd) {
	if m.running && m.c != nil {
		m.requestInterrupt()
	}
	return approvalDecide(m, middleware.DecisionDeny)
}

// ---- ctxAsk 绑定动作 ----

func askUpKey(m *Model, _ tea.KeyMsg) (bool, tea.Cmd) {
	ask := m.ovl.ask
	if ask.cursor > 0 {
		ask.cursor--
	}
	m.refresh(false)
	return true, nil
}

func askDownKey(m *Model, _ tea.KeyMsg) (bool, tea.Cmd) {
	ask := m.ovl.ask
	if len(ask.req.Options) > 0 && ask.cursor < len(ask.req.Options)-1 {
		ask.cursor++
	}
	m.refresh(false)
	return true, nil
}

// askSpaceKey Space：多选时勾选/取消当前项。
func askSpaceKey(m *Model, _ tea.KeyMsg) (bool, tea.Cmd) {
	ask := m.ovl.ask
	if ask.req.Multiple && len(ask.selected) > 0 {
		ask.selected[ask.cursor] = !ask.selected[ask.cursor]
	}
	m.refresh(false)
	return true, nil
}

func askEnterKey(m *Model, _ tea.KeyMsg) (bool, tea.Cmd) {
	ask := m.ovl.ask
	nm, cmd := m.finishAsk(ask)
	*m = nm.(Model)
	return true, cmd
}

func askEscKey(m *Model, _ tea.KeyMsg) (bool, tea.Cmd) {
	ask := m.ovl.ask
	ask.respCh <- middleware.AskResult{} // 取消 = 空回答
	nm, cmd := m.closeOverlay()
	*m = nm
	return true, cmd
}

func askBackspaceKey(m *Model, _ tea.KeyMsg) (bool, tea.Cmd) {
	ask := m.ovl.ask
	if r := []rune(ask.custom); len(r) > 0 {
		ask.custom = string(r[:len(r)-1])
	}
	m.refresh(false)
	return true, nil
}

// askIgnoreKey 防误触全局快捷键的忽略键（原实现忽略后仍走统一 refresh）。
func askIgnoreKey(m *Model, _ tea.KeyMsg) (bool, tea.Cmd) {
	m.refresh(false)
	return true, nil
}

// askCustomKey 兜底：可打印字符追加到 Other 自定义输入（AllowCustom 关闭时忽略）。
func askCustomKey(m *Model, msg tea.KeyMsg) (bool, tea.Cmd) {
	ask := m.ovl.ask
	if !ask.req.AllowCustom {
		return true, nil
	}
	if len(msg.Runes) > 0 {
		ask.custom += string(msg.Runes)
	}
	m.refresh(false)
	return true, nil
}

// ---- ctxSelect 绑定动作 ----

func selectUpKey(m *Model, _ tea.KeyMsg) (bool, tea.Cmd) {
	sel := m.ovl.sel
	if sel.cursor > 0 {
		sel.cursor--
	}
	m.refresh(false)
	return true, nil
}

func selectDownKey(m *Model, _ tea.KeyMsg) (bool, tea.Cmd) {
	sel := m.ovl.sel
	if sel.cursor < len(sel.items)-1 {
		sel.cursor++
	}
	m.refresh(false)
	return true, nil
}

// selectEnterKey Enter：确认选择并关闭弹窗（原实现丢弃 closeOverlay 的 cmd）。
func selectEnterKey(m *Model, _ tea.KeyMsg) (bool, tea.Cmd) {
	message, err := m.confirmPopup()
	nm, _ := m.closeOverlay()
	if err != nil {
		*m = nm.sysErr(err).(Model)
		return true, nil
	}
	*m = nm.sysOK(message).(Model)
	return true, nil
}

func selectEscKey(m *Model, _ tea.KeyMsg) (bool, tea.Cmd) {
	nm, cmd := m.closeOverlay()
	*m = nm
	return true, cmd
}

// ---- ctxHelp 绑定动作 ----

func helpCloseKey(m *Model, _ tea.KeyMsg) (bool, tea.Cmd) {
	nm, cmd := m.closeOverlay()
	*m = nm
	return true, cmd
}
