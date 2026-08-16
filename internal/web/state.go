package web

import (
	"fmt"

	"github.com/agent-project/harness/internal/agentstate"
	"github.com/agent-project/harness/internal/config"
	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/session"
)

// StateSnapshot 是 /api/state 的响应（前端全量重建/对齐用）。字段契约见
// 计划（feat/webui）。
type StateSnapshot struct {
	Sessions []SessionBrief   `json:"sessions"`
	Active   *ActiveSession   `json:"active"`
	Timeline []TimelineItem   `json:"timeline"`
	Running  bool             `json:"running"`
	QueueLen int              `json:"queue_len"`
	Pending  []map[string]any `json:"pending"`
}

// SessionBrief 是会话列表条目（对位 session.SessionInfo；前端倒序显示）。
type SessionBrief struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Model string `json:"model"`
}

// ActiveSession 是当前会话状态栏数据（对位 tui StatusBar）。
type ActiveSession struct {
	ID                string                `json:"id"`
	Name              string                `json:"name"`
	Model             string                `json:"model"`
	Permission        string                `json:"permission"`
	Effort            string                `json:"effort"`
	Thinking          bool                  `json:"thinking"`
	Plan              bool                  `json:"plan"`
	ContextTokens     int64                 `json:"context_tokens"`
	ContextWindow     int                   `json:"context_window"`
	Todos             []agentstate.TodoItem `json:"todos"`
	ViewingSubagent   bool                  `json:"viewing_subagent"`
	ViewingSubagentID string                `json:"viewing_subagent_id"`
}

// TimelineItem 是 timeline 的一个元素（message/tool/system 三态）。
type TimelineItem struct {
	Kind     string    `json:"kind"` // message | tool | system
	ID       string    `json:"id"`
	Role     string    `json:"role,omitempty"` // user | assistant
	Text     string    `json:"text,omitempty"`
	HTML     string    `json:"html,omitempty"`
	Thinking string    `json:"thinking,omitempty"`
	Done     bool      `json:"done"`
	Error    bool      `json:"error,omitempty"`
	Tool     *ToolView `json:"tool,omitempty"`
}

// State 构建当前状态快照（锁内读 active/open/queue/pending；timeline 从
// transcript 重建——对位 tui loadSessionHistory）。
func (c *Controller) State() *StateSnapshot {
	c.mu.Lock()
	active := c.active
	running := c.running
	queueLen := len(c.queue)
	viewingSubagent := c.viewSubID != ""
	viewingSubagentID := c.viewSubID
	c.mu.Unlock()

	s := &StateSnapshot{
		Sessions: c.sessionBriefs(),
		Running:  running,
		QueueLen: queueLen,
		Pending:  c.pendingSnapshots(),
	}
	if active != nil {
		s.Active = activeSession(active, c.cfg, viewingSubagent, viewingSubagentID)
		s.Timeline = buildTimeline(active)
	}
	return s
}

// sessionBriefs 列出项目会话（对位 Controller.Sessions）。
func (c *Controller) sessionBriefs() []SessionBrief {
	list, err := c.proj.Sessions()
	if err != nil {
		return nil
	}
	out := make([]SessionBrief, 0, len(list))
	for _, si := range list {
		out = append(out, SessionBrief{ID: si.ID, Name: si.Name, Model: si.Model})
	}
	return out
}

// activeSession 构建 active 会话状态栏数据。
func activeSession(s *session.Session, cfg config.Config, viewing bool, viewingID string) *ActiveSession {
	st := s.State()
	thinking := true
	if st.ThinkingEnabled != nil {
		thinking = *st.ThinkingEnabled
	}
	cw := 0
	if res, err := config.Resolve(cfg, s.Model()); err == nil {
		cw = res.ContextWindow
	}
	return &ActiveSession{
		ID:                s.ID,
		Name:              s.Name(),
		Model:             s.Model(),
		Permission:        st.PermissionMode(),
		Effort:            st.ThinkingEffort,
		Thinking:          thinking,
		Plan:              st.IsPlanMode(),
		ContextTokens:     st.CurrentContextTokens(),
		ContextWindow:     cw,
		Todos:             st.TodoItems(),
		ViewingSubagent:   viewing,
		ViewingSubagentID: viewingID,
	}
}

// buildTimeline 从会话 transcript 重建 timeline（对位 tui loadSessionHistory：
// transcript 优先，坏行跳过；无 transcript 回退 conversation）。元素 id 规则
// （review M5）：msg-<会话内自增序号>（不依赖 msg_id——可能为空）；
// tool-<call_id> 前缀区分命名空间。
func buildTimeline(s *session.Session) []TimelineItem {
	if lines, skipped, err := s.TranscriptLines(); err == nil && len(lines) > 0 {
		return buildTimelineFromLines(lines, skipped)
	}
	return buildTimelineFromConversation(s.Conversation())
}

// buildTimelineFromLines 从 transcript 行重建（对位 tui loadTranscriptLines）。
func buildTimelineFromLines(lines []session.Line, skipped int) []TimelineItem {
	var items []TimelineItem
	msgSeq := 0
	assistants := map[string]int{}      // msg_id → items 索引（thinking/text 归并）
	tools := map[string]*toolCallInfo{} // call_id → tool 调用信息（分派用）
	toolIdx := map[string]int{}         // call_id → items 索引
	for _, line := range lines {
		switch line.Type {
		case session.LineTypeUser:
			items = append(items, TimelineItem{
				Kind: "message", ID: msgID(msgSeq), Role: string(messages.RoleUser),
				Text: line.Content, HTML: renderHTML(line.Content), Done: true,
			})
			msgSeq++
		case session.LineTypeThinking, session.LineTypeText:
			id := line.MsgID
			idx, ok := assistants[id]
			if !ok {
				if id == "" {
					id = fmt.Sprintf("transcript-%d", len(items))
				}
				idx = len(items)
				assistants[line.MsgID] = idx
				items = append(items, TimelineItem{
					Kind: "message", ID: msgID(msgSeq), Role: string(messages.RoleAssistant), Done: true,
				})
				msgSeq++
			}
			item := &items[idx]
			if line.Type == session.LineTypeThinking {
				item.Thinking += line.Text
			} else {
				item.Text += line.Text
				item.HTML = renderHTML(item.Text)
			}
		case session.LineTypeToolUse:
			idx := len(items)
			tools[line.CallID] = prepareToolCall(line.Name, line.Args)
			toolIdx[line.CallID] = idx
			items = append(items, TimelineItem{
				Kind: "tool", ID: "tool-" + line.CallID, Done: false,
				Tool: &ToolView{
					Name:    line.Name,
					Summary: toolCallSummary(line.Name, line.Args),
					Args:    formatToolArgs(line.Args),
				},
			})
		case session.LineTypeToolResult:
			info, ok := tools[line.CallID]
			idx, idxOK := toolIdx[line.CallID]
			if !ok || !idxOK {
				continue
			}
			success := line.Success != nil && *line.Success
			res := &messages.ToolResult{Success: success, Content: line.Content}
			view := applyToolResult(info, res)
			view.Name = info.name
			view.Summary = toolCallSummary(info.name, info.args)
			item := &items[idx]
			item.Done = true
			item.Tool = &view
			delete(tools, line.CallID)
			delete(toolIdx, line.CallID)
		case session.LineTypeCommand:
			items = append(items, TimelineItem{
				Kind: "system", Text: "Command  " + line.Content, Done: true,
			})
		}
	}
	// 坏行提示（读侧容错，Bug08）。
	if skipped > 0 {
		items = append(items, TimelineItem{
			Kind: "system", Text: fmt.Sprintf("resume: 忽略 %d 行损坏/超长记录", skipped), Error: true, Done: true,
		})
	}
	return items
}

// buildTimelineFromConversation 从 conversation 重建（无 transcript 回退；
// 对位 tui loadHistory）。
func buildTimelineFromConversation(conv *messages.Conversation) []TimelineItem {
	var items []TimelineItem
	msgSeq := 0
	tools := map[string]*toolCallInfo{} // call_id → 调用信息
	toolIdx := map[string]int{}
	for _, message := range conv.Messages {
		switch message.Role {
		case messages.RoleUser:
			items = append(items, TimelineItem{
				Kind: "message", ID: msgID(msgSeq), Role: string(messages.RoleUser),
				Text: message.Content, HTML: renderHTML(message.Content), Done: true,
			})
			msgSeq++
		case messages.RoleAssistant:
			if message.Content != "" || message.Thinking != "" {
				items = append(items, TimelineItem{
					Kind: "message", ID: msgID(msgSeq), Role: string(messages.RoleAssistant),
					Text: message.Content, HTML: renderHTML(message.Content),
					Thinking: message.Thinking, Done: true,
				})
				msgSeq++
			}
			for i := range message.ToolCalls {
				call := message.ToolCalls[i]
				idx := len(items)
				tools[call.ID] = prepareToolCall(call.Name, call.Args)
				toolIdx[call.ID] = idx
				items = append(items, TimelineItem{
					Kind: "tool", ID: "tool-" + call.ID,
					Tool: &ToolView{
						Name:    call.Name,
						Summary: toolCallSummary(call.Name, call.Args),
						Args:    formatToolArgs(call.Args),
					},
				})
			}
		case messages.RoleTool:
			for _, result := range message.ToolResults {
				info, ok := tools[result.ToolCallID]
				idx, idxOK := toolIdx[result.ToolCallID]
				if !ok || !idxOK {
					continue
				}
				view := applyToolResult(info, &messages.ToolResult{Success: result.Success, Content: result.Content})
				view.Name = info.name
				view.Summary = toolCallSummary(info.name, info.args)
				item := &items[idx]
				item.Done = true
				item.Tool = &view
				delete(tools, result.ToolCallID)
				delete(toolIdx, result.ToolCallID)
			}
		}
	}
	return items
}

// msgID 生成消息元素 id（msg-<序号>；review M5 不依赖 msg_id）。
func msgID(seq int) string { return fmt.Sprintf("msg-%d", seq) }
