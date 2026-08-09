// Package tui 是 harness 的 bubbletea 全屏交互 UI（替代 REPL，ADR-030）。
// elm 架构：Model.Update(msg) (Model, Cmd) + Model.View() string 均为纯函数，
// 可无 TTY 单测（对症 REPL 测试痛点）。
//
// 目录布局：
//
//	model.go    根 Model + UI state（W2 起：msgs/stream/tools/queue/status）
//	update.go   Update 主 switch（W2 起扩展：agent 事件/按键/审批/命令）
//	view.go     View 布局（lipgloss：消息区/输入区/队列条/todo 条/状态栏）
//	events.go   agent 事件桥（onEvent → program.Send）与内部 Msg 类型
//	approver.go TUIApprover（审批弹窗桥）
//	run.go      RunTUI 入口
package tui

import (
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
)

// Model 是 TUI 根组件（bubbletea elm）。字段为纯 UI state。
// W1 骨架：仅输入区 + 终端尺寸 + /exit 退出；W2 起扩展。
type Model struct {
	input  textarea.Model // 底部多行输入区
	width  int            // 终端宽
	height int            // 终端高
	// W2：msgs []*MessageItem  历史 + 增量消息
	// W2：stream *StreamState  当前流式块
	// W3：tools []ToolStatus   本批工具折叠块
	// W3：status StatusBar     模型/会话/权限/todo
	// W4：queue []string       用户输入队列（prompt + /命令）
	// W4：appr  *ApprovalPrompt 审批弹窗
}

// New 构造根 Model。
func New() Model {
	ta := textarea.New()
	ta.Placeholder = "输入消息…（Enter 提交 · Shift+Enter 换行）   /help 查看命令"
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.SetWidth(80)
	ta.SetHeight(3)
	ta.Focus()
	return Model{input: ta}
}

// Init 返回初始 Cmd（首版无）。
func (m Model) Init() tea.Cmd { return nil }

// Update 是纯函数 reducer：消息 → 新 state + 副作用 Cmd。
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.SetWidth(msg.Width - 4)
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	default:
		return m, nil
	}
}

// handleKey 处理键盘事件。
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		// Ctrl+C = 复制语义（ADR-030）；剪贴板接入前 no-op。
		return m, nil
	case "esc":
		// Esc = 中断当前回合（ADR-028）；无回合时 no-op（W3 接回合）。
		return m, nil
	}

	if msg.Type == tea.KeyEnter && !msg.Alt {
		// Enter（非 Alt）= 提交。W1 仅支持 /exit 退出，其余待 W2 接 agent。
		line := m.input.Value()
		m.input.Reset()
		switch line {
		case "/exit":
			return m, tea.Quit
		default:
			return m, nil
		}
	}

	// 其余按键交给 textarea（换行键：bubbletea v1.3 无 Shift+Enter 区分，
	// 换行走 Alt+Enter —— W2 在提交分支里 InsertString("\n") 实现）。
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// View 渲染整个屏幕（纯函数，可对输出断言）。
func (m Model) View() string {
	s := "harness TUI（W1 骨架）\n\n"
	s += m.input.View()
	return s
}
