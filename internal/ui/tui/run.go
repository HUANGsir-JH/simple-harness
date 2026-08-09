package tui

import (
	"context"
	"fmt"

	"github.com/agent-project/harness/internal/agent"
	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/provider"
	"github.com/agent-project/harness/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

// RunTUI 启动 TUI（bubbletea Program，alt-screen 全屏；ADR-030）。
// a/proj/cfg/sess/ctx 由 cmd 层装配（共享无状态 agent + 项目桶 + 配置 + 会话）。
func RunTUI(a *agent.Agent, proj *session.Project, cfg provider.Config, sess *session.Session, ctx context.Context) error {
	c := NewController(a, proj, cfg, sess, ctx)
	m := New(c)
	loadHistory(&m, sess.Conversation())
	// 历史斜杠命令渲染为系统行（resume 呈现；模型不可见，ADR-030）。
	if cmds, err := sess.Commands(); err == nil {
		for _, c := range cmds {
			m.msgs = append(m.msgs, &MessageItem{Role: "", Content: "[命令] " + c, Rendered: "[命令] " + c, Done: true})
		}
	}
	m.refresh()

	p := tea.NewProgram(m, tea.WithAltScreen())
	c.setSend(p.Send)
	_, err := p.Run()
	c.CloseAll()
	if err != nil {
		return fmt.Errorf("tui: %w", err)
	}
	return nil
}

// loadHistory 从会话历史载入首屏（resume 历史可见；工具块重建 W3）。
func loadHistory(m *Model, conv *messages.Conversation) {
	for _, msg := range conv.Messages {
		switch msg.Role {
		case messages.RoleUser:
			m.msgs = append(m.msgs, &MessageItem{Role: messages.RoleUser, Content: msg.Content, Rendered: msg.Content, Done: true})
		case messages.RoleAssistant:
			if msg.Content == "" && len(msg.ToolCalls) == 0 {
				continue
			}
			rendered := renderMarkdown(msg.Content, m.width)
			m.msgs = append(m.msgs, &MessageItem{
				Role:     messages.RoleAssistant,
				Content:  msg.Content,
				Rendered: rendered,
				Thinking: msg.Thinking,
				Done:     true,
			})
		case messages.RoleTool:
			// 历史工具结果简略显示（W3 折叠块重建）。
			c := msg.Content
			if c == "" && len(msg.ToolResults) > 0 {
				c = fmt.Sprintf("%d 个工具结果", len(msg.ToolResults))
			}
			m.msgs = append(m.msgs, &MessageItem{Role: "", Content: c, Rendered: truncate(c, 120), Done: true})
		}
	}
}

// truncate 截断字符串（超长补省略号）。
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
