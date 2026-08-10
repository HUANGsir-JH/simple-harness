package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/agent-project/harness/internal/messages"
)

// LoadConversation 读取 historys 目录下最大序号文件，按 ordinal 逐行重建 conversation。
// 最新文件即有效历史（压缩切分后新文件以摘要+保留开头；旧文件纯审计）。
// 读侧容错（Bug08）：坏行跳过、不再锁死 resume，跳过计数见 LoadLines。
func LoadConversation(historyDir string) (*messages.Conversation, error) {
	seg := currentSegment(historyDir)
	if seg == 0 {
		return nil, fmt.Errorf("session: no transcript found in %s", historyDir)
	}
	conv, _, err := loadHistoryFile(historyPath(historyDir, seg))
	return conv, err
}

// LoadLines returns the latest transcript segment in ordinal order. UI clients
// use it when they need non-model entries such as command rows as well as the
// conversation reconstructed by LoadConversation.
// 返回 skipped = 跳过的坏行数（读侧容错，Bug08）。
func LoadLines(historyDir string) ([]Line, int, error) {
	seg := currentSegment(historyDir)
	if seg == 0 {
		return nil, 0, nil
	}
	path := historyPath(historyDir, seg)
	var lines []Line
	skipped, err := forEachLine(path, func(raw []byte) error {
		var line Line
		if err := json.Unmarshal(raw, &line); err != nil {
			return err // 坏行（JSON 损坏）→ skipped++，不中断
		}
		lines = append(lines, line)
		return nil
	})
	if err != nil {
		return nil, skipped, err
	}
	return lines, skipped, nil
}

// forEachLine 逐行读取 transcript 文件并回调。bufio.Reader.ReadBytes 无行长
// 限制（替换 bufio.Scanner 的 4MB 上限——一次超大工具输出或一行写坏不再锁死
// resume，Bug08）；回调返回非 nil（坏行）时跳过并计数，不中断读取。
func forEachLine(path string, fn func(raw []byte) error) (skipped int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("session: open %s: %w", path, err)
	}
	defer f.Close()
	r := bufio.NewReader(f)
	for {
		raw, rerr := r.ReadBytes('\n')
		if len(raw) > 0 {
			if perr := fn(raw); perr != nil {
				skipped++
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				break
			}
			return skipped, rerr
		}
	}
	return skipped, nil
}

func loadHistoryFile(path string) (*messages.Conversation, int, error) {
	conv := messages.NewConversation()
	var cur *messages.Message // 当前 assistant（同 msg_id 累积 thinking/text/tool_use）
	skipped, err := forEachLine(path, func(raw []byte) error {
		var line Line
		if err := json.Unmarshal(raw, &line); err != nil {
			return err
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
		return nil
	})
	if err != nil {
		return nil, skipped, err
	}
	return conv, skipped, nil
}

// loadCommands 读取 historys 的 command 行（斜杠命令历史；TUI resume 渲染
// 系统行，ADR-030）。command 行不进 conversation（模型不可见）。
func loadCommands(historyDir string) ([]string, error) {
	lines, _, err := LoadLines(historyDir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range lines {
		if line.Type == "command" {
			out = append(out, line.Content)
		}
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
