package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"github.com/agent-project/harness/internal/messages"
)

// LoadConversation 读取 historys 目录下最大序号文件，按 ordinal 逐行重建 conversation。
// 最新文件即有效历史（压缩切分后新文件以摘要+保留开头；旧文件纯审计）。
func LoadConversation(historyDir string) (*messages.Conversation, error) {
	seg := currentSegment(historyDir)
	if seg == 0 {
		return nil, fmt.Errorf("session: no transcript found in %s", historyDir)
	}
	return loadHistoryFile(historyPath(historyDir, seg))
}

func loadHistoryFile(path string) (*messages.Conversation, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("session: open %s: %w", path, err)
	}
	defer f.Close()

	conv := messages.NewConversation()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var cur *messages.Message // 当前 assistant（同 msg_id 累积 thinking/text/tool_use）
	for sc.Scan() {
		var line Line
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			return nil, fmt.Errorf("session: bad line: %w", err)
		}
		switch line.Type {
		case "user":
			cur = nil
			conv.Add(&messages.Message{ID: line.MsgID, Role: messages.RoleUser, Content: line.Content})
		case "thinking":
			cur = ensureAssistant(conv, cur, line.MsgID)
			cur.Thinking += line.Text
		case "text":
			cur = ensureAssistant(conv, cur, line.MsgID)
			cur.Content += line.Text
		case "tool_use":
			cur = ensureAssistant(conv, cur, line.MsgID)
			cur.ToolCalls = append(cur.ToolCalls, messages.ToolCall{ID: line.CallID, Name: line.Name, Args: line.Args})
		case "tool_result":
			cur = nil
			appendToolResult(conv, line)
		case "meta", "turn_start", "turn_end", segMarker:
			// 无消息语义
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return conv, nil
}

// loadCommands 读取 historys 的 command 行（斜杠命令历史；TUI resume 渲染
// 系统行，ADR-030）。command 行不进 conversation（模型不可见）。
func loadCommands(historyDir string) ([]string, error) {
	seg := currentSegment(historyDir)
	if seg == 0 {
		return nil, nil
	}
	f, err := os.Open(historyPath(historyDir, seg))
	if err != nil {
		return nil, fmt.Errorf("session: open %s: %w", historyPath(historyDir, seg), err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var line Line
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			return nil, fmt.Errorf("session: bad line: %w", err)
		}
		if line.Type == "command" {
			out = append(out, line.Content)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ensureAssistant 返回消息 id 对应的 assistant 消息；若无则新建追加。
// thinking/text/tool_use 同属一个 assistant（同一次采样轮）。
func ensureAssistant(conv *messages.Conversation, cur *messages.Message, msgID string) *messages.Message {
	if cur != nil && cur.ID == msgID {
		return cur
	}
	cur = &messages.Message{ID: msgID, Role: messages.RoleAssistant}
	conv.Add(cur)
	return cur
}

// appendToolResult 追加 tool_result 块：与上一条 tool 消息合并（多块，满足
// anthropic 紧邻要求），否则新建 tool 消息。
func appendToolResult(conv *messages.Conversation, line Line) {
	succ := line.Success != nil && *line.Success
	block := messages.ToolResultBlock{ToolCallID: line.CallID, Success: succ, Content: line.Content}
	if n := len(conv.Messages); n > 0 && conv.Messages[n-1].Role == messages.RoleTool {
		last := conv.Messages[n-1]
		last.ToolResults = append(last.ToolResults, block)
		return
	}
	conv.Add(&messages.Message{Role: messages.RoleTool, ToolResults: []messages.ToolResultBlock{block}})
}
