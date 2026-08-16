package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/session"
	"github.com/agent-project/harness/internal/subagent"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type popupKind int

const (
	popupSwitch popupKind = iota
	popupModel
	popupEffort
	popupPermission
	popupThinking
	popupSubagents
)

type popupItem struct {
	label       string
	value       string
	description string
}

type selectPopup struct {
	kind    popupKind
	title   string
	items   []popupItem
	cursor  int
	current string
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
	{name: "subagents", short: "List / view sub agents"},
	{name: "usage", short: "Show token usage"},
	{name: "compact", short: "Compress context (LLM summary)"},
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

// subagentItems 生成 /subagents 弹窗选项（id/name/type/status/depth）。
func subagentItems(views []subagent.EntryView) []popupItem {
	items := make([]popupItem, 0, len(views))
	for _, v := range views {
		status := v.Status
		if v.Running {
			status += " · running"
		}
		items = append(items, popupItem{
			label:       v.Name + "（" + status + "）",
			value:       v.ID,
			description: fmt.Sprintf("%s · depth %d · %s", v.Type, v.Depth, v.ID),
		})
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
		// 子 agent 只读查看模式：Esc 是主退出键（handleKey，输入框禁用无法
		// 输入命令），/switch 命令同样可退（无参直接回父，带参继续原切
		// 会话逻辑）。
		if m.viewingSubagent {
			m.c.ExitSubagentView()
			m.viewingSubagent = false
			m.reloadSession()
			if cmd.arg == "" {
				return m.sysOK("Back to " + shortSession(m.c.active.ID)), nil
			}
		}
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
		return m.openPopup(popupSwitch, "Sessions", switchItems(m.c.Sessions()), m.c.ActiveID()), nil
	case "model":
		if cmd.arg != "" {
			if err := m.c.SetModel(cmd.arg); err != nil {
				return m.sysErr(err), nil
			}
			return m.sysOK("Model set to " + cmd.arg), nil
		}
		return m.openPopup(popupModel, "Models", modelItems(m.c.Models()), m.c.ActiveModel()), nil
	case "effort":
		if cmd.arg != "" {
			if err := m.c.SetEffort(cmd.arg); err != nil {
				return m.sysErr(err), nil
			}
			return m.sysOK("Effort set to " + cmd.arg), nil
		}
		if err := m.c.ensureActive(); err != nil {
			return m.sysErr(err), nil
		}
		efforts := m.c.Efforts()
		current := ""
		if st := m.c.ActiveState(); st != nil {
			current = st.ThinkingEffort
		}
		return m.openPopup(popupEffort, "Reasoning effort", effortItems(efforts), current), nil
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
		return m.openPopup(popupPermission, "Permission", permissionItems(m.c.PermissionModes()), current), nil
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
		return m.openPopup(popupThinking, "Thinking", thinkingItems(), thinkingCurrent(m.c.ActiveState())), nil
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
			m.appendSystem("Plan file  "+m.c.active.PlanFile()+"\n"+content, false)
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
		m.refreshStatus() // 会话条 plan 标记即时更新
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
	case "subagents":
		// /subagents [id]：列出当前会话的子 agent（运行态 + 磁盘历史）；选中
		// 或带参直接进入只读查看（输入框禁用，/switch 返回父会话；子事件实时
		// 滚动——运行中查看可见进度）。
		if cmd.arg != "" {
			if err := m.c.ViewSubagent(cmd.arg); err != nil {
				return m.sysErr(err), nil
			}
			m.viewingSubagent = true
			m.reloadSession()
			return m.sysOK("Viewing sub agent " + shortSession(m.c.active.ID) + "（只读，/switch 返回）"), nil
		}
		views := m.c.ListSubagents()
		if len(views) == 0 {
			return m.sysOK("（当前会话没有子 agent）"), nil
		}
		return m.openPopup(popupSubagents, "Sub agents", subagentItems(views), ""), nil
	case "usage":
		// 最近一次 API 调用的完整用量 + 当前上下文占用（ADR-037 用量展示，
		// 覆盖语义：每次采样返回的 usage 即该次调用的完整账目，系统行对齐 /plan view）。
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
		m.appendSystem("Usage  "+strings.Join(parts, "  "), false)
		m.refresh(false)
		return m, nil
	case "compact":
		// 手动压缩（ADR-037）：tea.Cmd 内跑 compactor.Run(force=true)（含 LLM
		// 摘要调用，避免阻塞 UI）；完成经 compactDoneMsg reloadSession 显示摘要
		// 占位。摘要失败不重写 conversation，系统行提示重试。
		if m.c.active == nil {
			return m.sysErr(fmt.Errorf("无会话（先发一条消息）")), nil
		}
		// 审查修复 01（2026-08-14）：/compact 分派**同步**置 running——RunCompact
		// 的 setCancel 在异步 cmd 内，若不置位，分派后、cmd 执行前的间隙
		// completionWakeMsg（或用户输入）会穿过双闸并发启动 run，与压缩并发
		// 读写同一 conversation（data race）。handleCompactDone 复位。
		m.running = true
		m.turnDone = false
		m.eventError = false
		m.toast = "压缩中…"
		m.refresh(false)
		return m, m.c.RunCompact()
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
	m, opened = m.openOverlay(&overlay{kind: overlaySelect, sel: &selectPopup{kind: kind, title: title, items: items, cursor: cursor, current: current}})
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
	case popupSubagents:
		if err := m.c.ViewSubagent(item.value); err != nil {
			return "", err
		}
		m.viewingSubagent = true
		m.reloadSession()
		return "Viewing sub agent " + shortSession(m.c.active.ID) + "（只读，/switch 返回）", nil
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
	m.clearSelection()
	loadSessionHistory(m, m.c.active)
	m.autoScroll = true
	m.refresh(true)
}

func renderPopup(sel *selectPopup, screenWidth, availableHeight int) string {
	panelWidth := modalPanelWidth(screenWidth, 34, 64)
	listWidth := modalInnerWidth(panelWidth)
	if len(sel.items) == 0 {
		return renderInlinePanel(sel.title, []string{styleMuted.Render("No options")}, panelWidth, styleAssistant)
	}
	cursor := clamp(sel.cursor, 0, len(sel.items)-1)
	contentBudget := maxInt(1, availableHeight-2) // panel title + bottom rule
	showDescriptions := listWidth >= 38
	showHint := true
	showIndicators := true
	for popupRowsHeight(sel, cursor, cursor+1, showDescriptions, showHint, showIndicators) > contentBudget {
		switch {
		case showDescriptions:
			showDescriptions = false
		case showHint:
			showHint = false
		case showIndicators:
			showIndicators = false
		default:
			break
		}
		if !showDescriptions && !showHint && !showIndicators {
			break
		}
	}

	start, end := cursor, cursor+1
	for {
		preferUp := cursor-start <= end-cursor-1
		grew := false
		for attempt := 0; attempt < 2; attempt++ {
			tryUp := preferUp
			if attempt == 1 {
				tryUp = !tryUp
			}
			candidateStart, candidateEnd := start, end
			if tryUp && start > 0 {
				candidateStart--
			} else if !tryUp && end < len(sel.items) {
				candidateEnd++
			} else {
				continue
			}
			if popupRowsHeight(sel, candidateStart, candidateEnd, showDescriptions, showHint, showIndicators) <= contentBudget {
				start, end = candidateStart, candidateEnd
				grew = true
				break
			}
		}
		if !grew {
			break
		}
	}

	var rows []string
	if showIndicators && start > 0 {
		rows = append(rows, styleMuted.Render(fmt.Sprintf("  ↑ %d more", start)))
	}
	for i := start; i < end; i++ {
		prefix := "  "
		if i == sel.cursor {
			prefix = "❯ "
		}
		mark := "  "
		if sel.items[i].value == sel.current {
			mark = "✓ "
		}
		row := ansi.Truncate(prefix+mark+sel.items[i].label, maxInt(1, listWidth), "...")
		if i == sel.cursor {
			row = styleSelected.Render(fitLine(row, listWidth))
		}
		rows = append(rows, row)
		if showDescriptions && sel.items[i].description != "" {
			rows = append(rows, styleMuted.Render("      "+ansi.Truncate(sel.items[i].description, listWidth-6, "...")))
		}
	}
	if showIndicators && end < len(sel.items) {
		rows = append(rows, styleMuted.Render(fmt.Sprintf("  ↓ %d more", len(sel.items)-end)))
	}
	if showHint {
		rows = append(rows, styleMuted.Render(ansi.Truncate("↑/↓ move · Enter select · Esc cancel", listWidth, "...")))
	}
	return renderInlinePanel(sel.title, rows, panelWidth, styleAssistant)
}

func popupRowsHeight(sel *selectPopup, start, end int, descriptions, hint, indicators bool) int {
	height := 0
	if indicators && start > 0 {
		height++
	}
	for i := start; i < end; i++ {
		height++
		if descriptions && sel.items[i].description != "" {
			height++
		}
	}
	if indicators && end < len(sel.items) {
		height++
	}
	if hint {
		height++
	}
	return height
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
		description := "Ask before operations that need approval"
		switch mode {
		case "readonly":
			description = "Allow reads; ask before changes and commands"
		case "accept-edits":
			description = "Allow workspace edits; ask before risky commands"
		case "full-access":
			description = "Allow all supported operations without prompting"
		}
		items = append(items, popupItem{label: mode, value: mode, description: description})
	}
	return items
}

func thinkingItems() []popupItem {
	return []popupItem{
		{label: "enabled", value: "on", description: "Show model reasoning in the timeline"},
		{label: "disabled", value: "off", description: "Hide reasoning from the timeline"},
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
