package tui

import (
	"fmt"
	"sort"

	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

// popupKind 是弹窗选择器类型（/switch /model /effort /permission）。
type popupKind int

const (
	popupSwitch popupKind = iota
	popupModel
	popupEffort
	popupPermission
)

// popupItem 是选择器的一项（label 显示 / desc 右侧说明 / value 执行值）。
type popupItem struct {
	label string
	desc  string
	value string
}

// selectPopup 是斜杠命令弹窗选择器（↑/↓ 选 + Enter 确认 + Esc 取消）。
type selectPopup struct {
	kind   popupKind
	title  string
	items  []popupItem
	cursor int
}

// runCommand 处理一条斜杠命令：无参弹窗选择器；带参/特殊命令直接执行。
func (m Model) runCommand(cmd command) (tea.Model, tea.Cmd) {
	switch cmd.name {
	case "exit":
		return m, tea.Quit
	case "help":
		m.msgs = append(m.msgs, &MessageItem{Role: "", Content: "命令: /switch（弹窗） /model /effort /permission /help /exit。Esc 中断回合；Ctrl+C 复制。", Rendered: "命令: /switch（弹窗） /model /effort /permission /help /exit。Esc 中断回合；Ctrl+C 复制。", Done: true})
		m.refresh()
		return m, nil
	case "switch":
		if cmd.arg == "--last" {
			if err := m.c.SwitchLast(); err != nil {
				return m.sysErr(err), nil
			}
			m.reloadSession()
			return m.sysOK("已切换到最新会话 " + m.c.active.ID), nil
		}
		if cmd.arg != "" {
			if err := m.c.SwitchTo(cmd.arg); err != nil {
				return m.sysErr(err), nil
			}
			m.reloadSession()
			return m.sysOK("已切换到会话 " + m.c.active.ID), nil
		}
		return m.openPopup(popupSwitch, "切换会话", switchItems(m.c.Sessions())), nil
	case "model":
		return m.openPopup(popupModel, "切换模型", modelItems(m.c.Models())), nil
	case "effort":
		return m.openPopup(popupEffort, "切换推理档位", effortItems(m.c.Efforts())), nil
	case "permission":
		return m.openPopup(popupPermission, "切换审批模式", permissionItems(m.c.PermissionModes())), nil
	default:
		return m.sysErr(fmt.Errorf("未知命令 /%s（支持: /switch /model /effort /permission /help /exit）", cmd.name)), nil
	}
}

// openPopup 打开选择器弹窗（焦点自动抢占）。
func (m Model) openPopup(kind popupKind, title string, items []popupItem) tea.Model {
	if len(items) == 0 {
		return m.sysErr(fmt.Errorf("%s：无可用选项", title))
	}
	m.sel = &selectPopup{kind: kind, title: title, items: items}
	m.refresh()
	return m
}

// confirmPopup 应用当前选择并关闭弹窗，返回成功消息或错误。
func (m *Model) confirmPopup() (string, error) {
	sel := m.sel
	item := sel.items[sel.cursor]
	switch sel.kind {
	case popupSwitch:
		if err := m.c.SwitchTo(item.value); err != nil {
			return "", err
		}
		m.reloadSession()
		return "已切换到会话 " + m.c.active.ID, nil
	case popupModel:
		if err := m.c.SetModel(item.value); err != nil {
			return "", err
		}
		return "已切换模型 " + item.value, nil
	case popupEffort:
		if err := m.c.SetEffort(item.value); err != nil {
			return "", err
		}
		return "已切换 effort " + item.value, nil
	case popupPermission:
		if err := m.c.SetPermission(item.value); err != nil {
			return "", err
		}
		return "已切换审批模式 " + item.value, nil
	}
	return "", nil
}

// sysOK 系统行（成功反馈）。
func (m Model) sysOK(msg string) tea.Model {
	m.msgs = append(m.msgs, &MessageItem{Role: "", Content: "[系统] " + msg, Rendered: "[系统] " + msg, Done: true})
	m.refresh()
	return m
}

// sysErr 系统行（错误反馈）。
func (m Model) sysErr(err error) tea.Model {
	if err == nil {
		return m
	}
	m.msgs = append(m.msgs, &MessageItem{Role: "", Content: "[错误] " + err.Error(), Rendered: "[错误] " + err.Error(), Done: true, Err: true})
	m.refresh()
	return m
}

// reloadSession 切换会话：消息区全量替换 + 工具/流式/队列/审批清空（ADR-030）。
func (m *Model) reloadSession() {
	m.msgs = nil
	m.tools = nil
	m.stream = nil
	m.queue = nil
	m.appr = nil
	loadHistory(m, m.c.active.Conversation())
	m.refresh()
}

// --- 弹窗数据源（实时从配置/会话获取，非硬编码）--------------------------------

func switchItems(sessions []session.SessionInfo) []popupItem {
	items := make([]popupItem, 0, len(sessions))
	for _, s := range sessions {
		items = append(items, popupItem{label: s.ID, value: s.ID, desc: "会话"})
	}
	return items
}

func modelItems(models []string) []popupItem {
	items := make([]popupItem, 0, len(models))
	for _, name := range models {
		items = append(items, popupItem{label: name, value: name, desc: "模型"})
	}
	return items
}

func effortItems(efforts []string) []popupItem {
	items := make([]popupItem, 0, len(efforts))
	for _, e := range efforts {
		items = append(items, popupItem{label: e, value: e, desc: "推理档位"})
	}
	return items
}

func permissionItems(modes []string) []popupItem {
	items := make([]popupItem, 0, len(modes))
	for _, mode := range modes {
		desc := map[string]string{
			"readonly":   "只读放行，写操作/shell 询问",
			"acceptedit": "只读+编辑放行，shell 询问",
			"bypass":     "全部放行",
		}[mode]
		items = append(items, popupItem{label: mode, value: mode, desc: desc})
	}
	return items
}

// --- todo 常驻条数据（输入框上方，进行中-待办-完成排序）------------------------

// sortTodos 排序：in_progress → pending → completed（ADR-030）。
func sortTodos(todos []agentstate.TodoItem) []agentstate.TodoItem {
	out := append([]agentstate.TodoItem(nil), todos...)
	sort.SliceStable(out, func(i, j int) bool {
		return todoOrder(out[i]) < todoOrder(out[j])
	})
	return out
}

func todoOrder(t agentstate.TodoItem) int {
	switch t.Status {
	case agentstate.TodoInProgress:
		return 0
	case agentstate.TodoPending:
		return 1
	default:
		return 2
	}
}
