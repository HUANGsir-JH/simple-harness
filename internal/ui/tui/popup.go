package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/session"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type popupKind int

const (
	popupSwitch popupKind = iota
	popupModel
	popupEffort
	popupPermission
	popupThinking
)

type popupItem struct {
	label string
	value string
}

type selectPopup struct {
	kind   popupKind
	title  string
	items  []popupItem
	cursor int
}

type commandItem struct {
	name  string
	short string
}

var commandCatalog = []commandItem{
	{name: "switch", short: "Change session"},
	{name: "model", short: "Change model"},
	{name: "effort", short: "Set reasoning effort"},
	{name: "thinking", short: "Toggle thinking on/off"},
	{name: "permission", short: "Set approval policy"},
	{name: "plan", short: "Toggle plan mode / view plan"},
	{name: "usage", short: "Show token usage"},
	{name: "help", short: "Commands and keys"},
	{name: "exit", short: "Leave Harness"},
}

func completionItems(value string) []commandItem {
	prefix := strings.TrimPrefix(strings.TrimSpace(value), "/")
	if strings.Contains(prefix, " ") {
		return nil
	}
	items := make([]commandItem, 0, len(commandCatalog))
	for _, item := range commandCatalog {
		if strings.HasPrefix(item.name, prefix) {
			items = append(items, item)
		}
	}
	return items
}

func (m Model) runCommand(cmd command) (tea.Model, tea.Cmd) {
	if cmd.name == "exit" {
		m.queue = nil
		return m, tea.Quit
	}
	if cmd.name == "help" {
		var opened bool
		m, opened = m.openOverlay(&overlay{kind: overlayHelp})
		if !opened {
			return m, nil // 已有覆盖层未决：不叠开（Bug10 守卫）
		}
		m.input.Blur()
		m.refresh(false)
		return m, nil
	}
	if m.c == nil {
		return m.sysErr(fmt.Errorf("/%s requires a runtime controller", cmd.name)), nil
	}

	switch cmd.name {
	case "switch":
		if cmd.arg == "--last" {
			if err := m.c.SwitchLast(); err != nil {
				return m.sysErr(err), nil
			}
			m.reloadSession()
			return m.sysOK("Switched to " + shortSession(m.c.active.ID)), nil
		}
		if cmd.arg != "" {
			if err := m.c.SwitchTo(cmd.arg); err != nil {
				return m.sysErr(err), nil
			}
			m.reloadSession()
			return m.sysOK("Switched to " + shortSession(m.c.active.ID)), nil
		}
		return m.openPopup(popupSwitch, "SESSIONS", switchItems(m.c.Sessions()), m.c.ActiveID()), nil
	case "model":
		if cmd.arg != "" {
			if err := m.c.SetModel(cmd.arg); err != nil {
				return m.sysErr(err), nil
			}
			return m.sysOK("Model set to " + cmd.arg), nil
		}
		return m.openPopup(popupModel, "MODELS", modelItems(m.c.Models()), m.c.ActiveModel()), nil
	case "effort":
		if cmd.arg != "" {
			if err := m.c.SetEffort(cmd.arg); err != nil {
				return m.sysErr(err), nil
			}
			return m.sysOK("Effort set to " + cmd.arg), nil
		}
		current := ""
		if st := m.c.ActiveState(); st != nil {
			current = st.ThinkingEffort
		}
		return m.openPopup(popupEffort, "REASONING EFFORT", effortItems(m.c.Efforts()), current), nil
	case "permission":
		if cmd.arg != "" {
			if err := m.c.SetPermission(cmd.arg); err != nil {
				return m.sysErr(err), nil
			}
			return m.sysOK("Permission set to " + cmd.arg), nil
		}
		current := ""
		if state := m.c.ActiveState(); state != nil {
			current = state.PermissionMode()
		}
		return m.openPopup(popupPermission, "PERMISSION", permissionItems(m.c.PermissionModes()), current), nil
	case "thinking":
		if cmd.arg != "" {
			switch cmd.arg {
			case "on":
				if err := m.c.SetThinking(true); err != nil {
					return m.sysErr(err), nil
				}
				return m.sysOK("Thinking enabled"), nil
			case "off":
				if err := m.c.SetThinking(false); err != nil {
					return m.sysErr(err), nil
				}
				return m.sysOK("Thinking disabled"), nil
			default:
				return m.sysErr(fmt.Errorf("unknown /thinking arg %q (on|off)", cmd.arg)), nil
			}
		}
		return m.openPopup(popupThinking, "THINKING", thinkingItems(), thinkingCurrent(m.c.ActiveState())), nil
	case "plan":
		// /plan [on|off|view]；无参 toggle。on 时注入一次 plan 指令（Controller
		// SetPlanMode 处理）；view 在 timeline 显示计划文件内容（ADR-036）。
		if cmd.arg == "view" {
			content, err := m.c.PlanContent()
			if err != nil {
				return m.sysErr(err), nil
			}
			if content == "" {
				return m.sysOK("暂无计划文件（plan 模式下用 write_plan 写入）"), nil
			}
			m.appendSystem("PLAN FILE  "+m.c.active.PlanFile()+"\n"+content, false)
			m.refresh(true)
			return m, nil
		}
		current := false
		if st := m.c.ActiveState(); st != nil {
			current = st.IsPlanMode()
		}
		on := !current
		if cmd.arg == "on" {
			on = true
		} else if cmd.arg == "off" {
			on = false
		} else if cmd.arg != "" {
			return m.sysErr(fmt.Errorf("unknown /plan arg %q (on|off|view)", cmd.arg)), nil
		}
		if err := m.c.SetPlanMode(on); err != nil {
			return m.sysErr(err), nil
		}
		m.refreshStatus() // 状态栏 [PLAN] 即时更新
		if on {
			return m.sysOK("Plan 模式已开启（只读规划；/plan view 查看计划）"), nil
		}
		return m.sysOK("Plan 模式已关闭"), nil
	case "rename":
		if cmd.arg == "" {
			return m.sysErr(fmt.Errorf("/rename 需要名称（用法：/rename <名称>）")), nil
		}
		if err := m.c.Rename(cmd.arg); err != nil {
			return m.sysErr(err), nil
		}
		m.refreshStatus() // header 即时显示新名
		return m.sysOK("Renamed to " + strings.TrimSpace(cmd.arg)), nil
	case "usage":
		// 会话累计用量 + 当前上下文占用（ADR-037 用量展示，系统行对齐 /plan view）。
		if m.c == nil || m.c.active == nil {
			return m.sysErr(fmt.Errorf("无会话（先发一条消息）")), nil
		}
		u := m.c.active.State().UsageTotals()
		parts := []string{fmt.Sprintf("input=%s", fmtTokens(u.InputTokens))}
		if u.CacheReadInputTokens > 0 {
			parts = append(parts, fmt.Sprintf("cache_read=%s", fmtTokens(u.CacheReadInputTokens)))
		}
		if u.CacheCreationInputTokens > 0 {
			parts = append(parts, fmt.Sprintf("cache_creation=%s", fmtTokens(u.CacheCreationInputTokens)))
		}
		parts = append(parts, fmt.Sprintf("output=%s", fmtTokens(u.OutputTokens)))
		if cw := m.c.ActiveContextWindow(); cw > 0 {
			parts = append(parts, fmt.Sprintf("ctx=%s/%s", fmtTokens(m.c.active.State().CurrentContextTokens()), fmtTokens(int64(cw))))
		}
		m.appendSystem("USAGE  "+strings.Join(parts, "  "), false)
		m.refresh(false)
		return m, nil
	default:
		return m.sysErr(fmt.Errorf("unknown command /%s", cmd.name)), nil
	}
}

func (m Model) openPopup(kind popupKind, title string, items []popupItem, current string) tea.Model {
	if len(items) == 0 {
		return m.sysErr(fmt.Errorf("%s has no available options", strings.ToLower(title)))
	}
	cursor := 0
	for i, item := range items {
		if item.value == current {
			cursor = i
			break
		}
	}
	var opened bool
	m, opened = m.openOverlay(&overlay{kind: overlaySelect, sel: &selectPopup{kind: kind, title: title, items: items, cursor: cursor}})
	if !opened {
		return m // 已有覆盖层未决：不叠开（Bug10 守卫）
	}
	m.input.Blur()
	m.refresh(false)
	return m
}

func (m *Model) confirmPopup() (string, error) {
	sel := m.ovl.sel
	item := sel.items[sel.cursor]
	switch sel.kind {
	case popupSwitch:
		if err := m.c.SwitchTo(item.value); err != nil {
			return "", err
		}
		m.reloadSession()
		return "Switched to " + shortSession(m.c.active.ID), nil
	case popupModel:
		if err := m.c.SetModel(item.value); err != nil {
			return "", err
		}
		return "Model set to " + item.value, nil
	case popupEffort:
		if err := m.c.SetEffort(item.value); err != nil {
			return "", err
		}
		return "Effort set to " + item.value, nil
	case popupPermission:
		if err := m.c.SetPermission(item.value); err != nil {
			return "", err
		}
		return "Permission set to " + item.value, nil
	case popupThinking:
		if err := m.c.SetThinking(item.value == "on"); err != nil {
			return "", err
		}
		return "Thinking " + item.label, nil
	default:
		return "", fmt.Errorf("unsupported selector")
	}
}

func (m Model) sysOK(message string) tea.Model {
	m.appendSystem(message, false)
	m.toast = message
	m.refresh(true)
	return m
}

func (m Model) sysErr(err error) tea.Model {
	if err == nil {
		return m
	}
	m.appendSystem(err.Error(), true)
	m.toast = "Command failed"
	m.refresh(true)
	return m
}

func (m *Model) reloadSession() {
	m.items = nil
	m.msgs = nil
	m.tools = nil
	m.stream = nil
	m.queue = nil
	m.ovl = nil
	m.pending = nil // 切换会话：丢弃旧会话的待决请求（其 goroutine 随对应 run 的 ctx 释放）
	loadSessionHistory(m, m.c.active)
	m.autoScroll = true
	m.refresh(true)
}

func renderPopup(sel *selectPopup, screenWidth, availableHeight int) string {
	panelWidth := modalPanelWidth(screenWidth, 34, 64)
	maxRows := maxInt(3, availableHeight-6)
	start := 0
	if len(sel.items) > maxRows {
		start = sel.cursor - maxRows/2
		if start < 0 {
			start = 0
		}
		if start+maxRows > len(sel.items) {
			start = len(sel.items) - maxRows
		}
	}
	end := start + maxRows
	if end > len(sel.items) {
		end = len(sel.items)
	}
	listWidth := modalInnerWidth(panelWidth)
	var rows []string
	for i := start; i < end; i++ {
		prefix := "  "
		if i == sel.cursor {
			prefix = "> "
		}
		row := ansi.Truncate(prefix+sel.items[i].label, maxInt(1, listWidth), "...")
		if i == sel.cursor {
			row = styleSelected.Width(listWidth).Render(row)
		} else {
			row = lipgloss.NewStyle().Width(listWidth).Render(row)
		}
		rows = append(rows, row)
	}
	content := styleAssistant.Render(sel.title) + "\n\n" + strings.Join(rows, "\n")
	return modalStyle(panelWidth).Render(content)
}

func switchItems(sessions []session.SessionInfo) []popupItem {
	items := make([]popupItem, 0, len(sessions))
	for i := len(sessions) - 1; i >= 0; i-- {
		s := sessions[i]
		// label = 会话名（/rename 或首消息自动命名），未命名则短 ID 兜底。
		label := s.Name
		if label == "" {
			label = shortSession(s.ID)
		}
		items = append(items, popupItem{label: label, value: s.ID})
	}
	return items
}

func modelItems(models []string) []popupItem {
	items := make([]popupItem, 0, len(models))
	for _, name := range models {
		items = append(items, popupItem{label: name, value: name})
	}
	return items
}

func effortItems(efforts []string) []popupItem {
	items := make([]popupItem, 0, len(efforts))
	for _, effort := range efforts {
		items = append(items, popupItem{label: effort, value: effort})
	}
	return items
}

func permissionItems(modes []string) []popupItem {
	items := make([]popupItem, 0, len(modes))
	for _, mode := range modes {
		items = append(items, popupItem{label: mode, value: mode})
	}
	return items
}

func thinkingItems() []popupItem {
	return []popupItem{
		{label: "enabled", value: "on"},
		{label: "disabled", value: "off"},
	}
}

// thinkingCurrent 返回当前 thinking 开关对应的弹窗 value（on/off）。
// nil = 默认开启（AgentState.ThinkingEnabled 未显式设置，2026-08-10 起
// 删配置 enabled，thinking 默认开启）。
func thinkingCurrent(state *agentstate.AgentState) string {
	if state != nil && state.ThinkingEnabled != nil && !*state.ThinkingEnabled {
		return "off"
	}
	return "on"
}

func sortTodos(todos []agentstate.TodoItem) []agentstate.TodoItem {
	out := append([]agentstate.TodoItem(nil), todos...)
	sort.SliceStable(out, func(i, j int) bool { return todoOrder(out[i]) < todoOrder(out[j]) })
	return out
}

func todoOrder(todo agentstate.TodoItem) int {
	switch todo.Status {
	case agentstate.TodoInProgress:
		return 0
	case agentstate.TodoPending:
		return 1
	default:
		return 2
	}
}
