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
)

type popupItem struct {
	label string
	desc  string
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
	{name: "permission", short: "Set approval policy"},
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
		m.help = true
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
		return m.openPopup(popupSwitch, "SESSIONS", switchItems(m.c.Sessions()), m.c.active.ID), nil
	case "model":
		if cmd.arg != "" {
			if err := m.c.SetModel(cmd.arg); err != nil {
				return m.sysErr(err), nil
			}
			return m.sysOK("Model set to " + cmd.arg), nil
		}
		return m.openPopup(popupModel, "MODELS", modelItems(m.c.Models()), m.c.active.Model()), nil
	case "effort":
		if cmd.arg != "" {
			if err := m.c.SetEffort(cmd.arg); err != nil {
				return m.sysErr(err), nil
			}
			return m.sysOK("Effort set to " + cmd.arg), nil
		}
		current := m.c.active.State().ThinkingEffort
		return m.openPopup(popupEffort, "REASONING EFFORT", effortItems(m.c.Efforts()), current), nil
	case "permission":
		if cmd.arg != "" {
			if err := m.c.SetPermission(cmd.arg); err != nil {
				return m.sysErr(err), nil
			}
			return m.sysOK("Permission set to " + cmd.arg), nil
		}
		current := ""
		if state := m.c.active.State().Permission; state != nil {
			current = state.Mode
		}
		return m.openPopup(popupPermission, "PERMISSION", permissionItems(m.c.PermissionModes()), current), nil
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
	m.sel = &selectPopup{kind: kind, title: title, items: items, cursor: cursor}
	m.input.Blur()
	m.refresh(false)
	return m
}

func (m *Model) confirmPopup() (string, error) {
	item := m.sel.items[m.sel.cursor]
	switch m.sel.kind {
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
	m.appr = nil
	m.sel = nil
	loadSessionHistory(m, m.c.active)
	m.autoScroll = true
	m.refresh(true)
}

func renderPopup(sel *selectPopup, screenWidth, availableHeight int) string {
	panelWidth := clamp(screenWidth-8, 38, 82)
	maxRows := maxInt(3, availableHeight-8)
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
	listWidth := panelWidth
	if panelWidth >= 64 {
		listWidth = panelWidth/2 - 2
	}
	var rows []string
	for i := start; i < end; i++ {
		prefix := "  "
		if i == sel.cursor {
			prefix = "> "
		}
		row := ansi.Truncate(prefix+sel.items[i].label, listWidth-2, "...")
		if i == sel.cursor {
			row = styleSelected.Width(listWidth).Render(row)
		} else {
			row = lipgloss.NewStyle().Width(listWidth).Render(row)
		}
		rows = append(rows, row)
	}
	list := strings.Join(rows, "\n")
	description := styleMuted.Render(sel.items[sel.cursor].desc)
	body := list + "\n\n" + description
	if panelWidth >= 64 {
		detailWidth := panelWidth - listWidth - 4
		detail := styleMuted.Width(detailWidth).Render(ansi.Hardwrap(sel.items[sel.cursor].desc, detailWidth, true))
		body = lipgloss.JoinHorizontal(lipgloss.Top, list, "  ", detail)
	}
	content := styleAssistant.Render(sel.title) + "\n\n" + body
	return modalStyle(panelWidth).Render(content)
}

func switchItems(sessions []session.SessionInfo) []popupItem {
	items := make([]popupItem, 0, len(sessions))
	for i := len(sessions) - 1; i >= 0; i-- {
		s := sessions[i]
		items = append(items, popupItem{label: s.ID, value: s.ID, desc: "Resume this session and replace the current timeline."})
	}
	return items
}

func modelItems(models []string) []popupItem {
	items := make([]popupItem, 0, len(models))
	for _, name := range models {
		items = append(items, popupItem{label: name, value: name, desc: "Use this model for future turns in the active session."})
	}
	return items
}

func effortItems(efforts []string) []popupItem {
	descriptions := map[string]string{
		"low":  "Faster reasoning with a smaller token budget.",
		"high": "Balanced reasoning for most coding tasks.",
		"max":  "Use the model's largest available reasoning budget.",
	}
	items := make([]popupItem, 0, len(efforts))
	for _, effort := range efforts {
		items = append(items, popupItem{label: effort, value: effort, desc: descriptions[effort]})
	}
	return items
}

func permissionItems(modes []string) []popupItem {
	descriptions := map[string]string{
		"readonly":   "Read operations proceed. Writes and shell commands require approval.",
		"acceptedit": "Reads and file edits proceed. Shell commands require approval.",
		"bypass":     "All tool calls proceed without interactive approval.",
	}
	items := make([]popupItem, 0, len(modes))
	for _, mode := range modes {
		items = append(items, popupItem{label: mode, value: mode, desc: descriptions[mode]})
	}
	return items
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
