package tui

import (
	"context"
	"errors"
	"fmt"

	"github.com/agent-project/harness/internal/agent"
	"github.com/agent-project/harness/internal/config"
	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

// RunTUI starts the full-screen interactive client.
// sess 为已加载会话（resume）或 nil（新入口，懒加载）；newSession 是懒加载
// 创建器（sess nil 时首动作触发，resume 传 nil 不触发）。
func RunTUI(a *agent.Agent, project *session.Project, cfg config.Config, sess *session.Session, newSession func() (*session.Session, error), ctx context.Context, thinkingDisplay ...bool) error {
	controller := NewController(a, project, cfg, sess, newSession, ctx)
	model := New(controller)
	if len(thinkingDisplay) > 0 {
		model.showThinking = thinkingDisplay[0]
	}
	if sess != nil {
		loadSessionHistory(&model, sess)
	}
	model.refresh(true)

	program := tea.NewProgram(
		model,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
		tea.WithContext(ctx),
	)
	controller.setSend(program.Send)
	_, runErr := program.Run()
	// SIGTERM（tea.WithContext）→ program.Run 返回，但 run goroutine 可能仍在
	// emit；先等其退出再关 writer（Bug09 治因），writer closed 兜底（Bug06(a)）。
	controller.WaitRuns()
	// 退出前兜底把 active session 的 AgentState 写回（ADR-038 退出 pre-kill：
	// SessionMiddleware 每回合保存已覆盖正常路径，此处是进程退出时刻的廉价
	// 保险，在 CloseAll 前执行——flush transcript 与写 state 互不干扰）。
	// 落盘失败忽略：兜底是保险，正常路径已保存，不阻塞退出流程。
	_ = controller.SaveActiveState()
	defer controller.CloseAll()
	if runErr != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil
		}
		return fmt.Errorf("tui: %w", runErr)
	}
	return nil
}

func loadSessionHistory(model *Model, sess *session.Session) {
	if lines, skipped, err := sess.TranscriptLines(); err == nil && len(lines) > 0 {
		loadTranscriptLines(model, lines)
		if skipped > 0 {
			// 读侧容错（Bug08）：坏行已跳过不锁死 resume，但提示用户数据不完整。
			model.appendSystem(fmt.Sprintf("resume: 忽略 %d 行损坏/超长记录", skipped), true)
		}
		return
	}
	loadHistory(model, sess.Conversation())
}

func loadTranscriptLines(model *Model, lines []session.Line) {
	assistants := map[string]*MessageItem{}
	for index, line := range lines {
		switch line.Type {
		case session.LineTypeUser:
			model.appendMessage(&MessageItem{ID: line.MsgID, Role: messages.RoleUser, Content: line.Content, Rendered: line.Content, Done: true})
		case session.LineTypeThinking, session.LineTypeText:
			id := line.MsgID
			if id == "" {
				id = fmt.Sprintf("transcript-%d", index)
			}
			item := assistants[id]
			if item == nil {
				item = &MessageItem{ID: id, Role: messages.RoleAssistant, Done: true}
				assistants[id] = item
				model.appendMessage(item)
			}
			if line.Type == session.LineTypeThinking {
				item.Thinking += line.Text
			} else {
				item.Content += line.Text
				item.Rendered = renderMarkdown(item.Content, model.contentWidth)
			}
		case session.LineTypeToolUse:
			call := &messages.ToolCall{ID: line.CallID, Name: line.Name, Args: line.Args}
			model.onToolCall(call)
		case session.LineTypeToolResult:
			for _, tool := range model.tools {
				if tool.ID == line.CallID {
					success := line.Success != nil && *line.Success
					applyToolResult(tool, &messages.ToolResult{Success: success, Content: line.Content})
					break
				}
			}
		case session.LineTypeCommand:
			model.appendSystem("Command  "+line.Content, false)
		}
	}
}

// loadHistory builds the UI timeline from the model-visible conversation.
func loadHistory(model *Model, conversation *messages.Conversation) {
	for index, message := range conversation.Messages {
		switch message.Role {
		case messages.RoleUser:
			model.appendMessage(&MessageItem{
				ID:       message.ID,
				Role:     messages.RoleUser,
				Content:  message.Content,
				Rendered: message.Content,
				Done:     true,
			})
		case messages.RoleAssistant:
			messageID := message.ID
			if messageID == "" {
				messageID = fmt.Sprintf("history-%d", index)
			}
			if message.Content != "" || message.Thinking != "" {
				model.appendMessage(&MessageItem{
					ID:       messageID,
					Role:     messages.RoleAssistant,
					Content:  message.Content,
					Rendered: renderMarkdown(message.Content, model.contentWidth),
					Thinking: message.Thinking,
					Done:     true,
				})
			}
			for i := range message.ToolCalls {
				call := message.ToolCalls[i]
				model.onToolCall(&call)
			}
		case messages.RoleTool:
			for _, result := range message.ToolResults {
				for _, tool := range model.tools {
					if tool.ID == result.ToolCallID {
						applyToolResult(tool, &messages.ToolResult{Success: result.Success, Content: result.Content})
						break
					}
				}
			}
		}
	}
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}
