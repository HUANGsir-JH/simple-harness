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

// App 是装配完成的 TUI 实例（显式三阶段生命周期：Assemble → Run → Close，
// 架构整理 2026-08-14）。原 RunTUI 把装配/运行/拆除混在一个函数体里、启动与
// 收尾顺序靠语句排列保证；拆成三阶段后每段有名字有边界，拆除与装配对称可见。
type App struct {
	controller *Controller
	program    *tea.Program
	closed     bool
}

// Assemble 装配阶段：controller → model → 历史加载 → program → setSend
// 补偿登记。只接线不运行——接线完整性可在测试中不启动事件循环直接断言。
// sess 为已加载会话（resume）或 nil（新入口，懒加载）；newSession 是懒加载
// 创建器（sess nil 时首动作触发，resume 传 nil 不触发）。
// 构造鸡生蛋（bubbletea 固有：Program 需初始 Model，send 只能后注入）经
// setSend 补偿登记收敛在此阶段，不推翻。
func Assemble(a *agent.Agent, project *session.Project, cfg config.Config, sess *session.Session, newSession func() (*session.Session, error), ctx context.Context, showThinking bool) *App {
	controller := NewController(a, project, cfg, sess, newSession, ctx)
	model := New(controller)
	model.showThinking = showThinking
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
	return &App{controller: controller, program: program}
}

// Run 运行阶段：事件循环 + 运行侧收尾（WaitRuns → SaveActiveState，顺序显式；
// 原 RunTUI 收尾逻辑逐字保留）。
func (a *App) Run() error {
	_, runErr := a.program.Run()
	// SIGTERM（tea.WithContext）→ program.Run 返回，但 run goroutine 可能仍在
	// emit；先等其退出再关 writer（Bug09 治因），writer closed 兜底（Bug06(a)）。
	a.controller.WaitRuns()
	// 退出前兜底把 active session 的 AgentState 写回（ADR-038 退出 pre-kill：
	// SessionMiddleware 每回合保存已覆盖正常路径，此处是进程退出时刻的廉价
	// 保险，在 Close 前执行——flush transcript 与写 state 互不干扰）。
	// 落盘失败忽略：兜底是保险，正常路径已保存，不阻塞退出流程。
	_ = a.controller.SaveActiveState()
	if runErr != nil {
		if errors.Is(a.controller.ctx.Err(), context.Canceled) {
			return nil
		}
		return fmt.Errorf("tui: %w", runErr)
	}
	return nil
}

// Close 拆除阶段：CloseAll flush 所有打开会话（与 Assemble 对称；closed 守卫
// 幂等，外部 Teardown 兜底可重复调用）。
func (a *App) Close() {
	if a.closed {
		return
	}
	a.closed = true
	a.controller.CloseAll()
}

// RunTUI 是 Assemble→Run→Close 三阶段的便捷封装（既有调用点/外部引用兼容；
// 新代码建议直接走三阶段 API）。
func RunTUI(a *agent.Agent, project *session.Project, cfg config.Config, sess *session.Session, newSession func() (*session.Session, error), ctx context.Context, thinkingDisplay ...bool) error {
	showThinking := true
	if len(thinkingDisplay) > 0 {
		showThinking = thinkingDisplay[0]
	}
	app := Assemble(a, project, cfg, sess, newSession, ctx, showThinking)
	err := app.Run()
	app.Close()
	return err
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
