package messages

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// SaveJSONL 将 conversation 的消息写入 path，每行一个 JSON 对象（追加模式）。
// 每条消息自包含；读回文件即可完整重建 conversation。
// 通过 os.O_APPEND 支持并发追加。
func (t *Conversation) SaveJSONL(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	for _, m := range t.Messages {
		if err := enc.Encode(m); err != nil {
			return fmt.Errorf("encode message: %w", err)
		}
	}
	return nil
}

// LoadConversationJSONL 将会话 JSONL 文件读入 conversation。缺少 ID 的消息
// 会被赋予生成的 ID，保证文件始终是合法的 conversation。
func LoadConversationJSONL(path string) (*Conversation, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	t := NewConversation()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // allow lines up to 4 MiB
	line := 0
	for sc.Scan() {
		line++
		var m Message
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if m.ID == "" {
			m.ID = fmt.Sprintf("msg_%d", line)
		}
		t.Messages = append(t.Messages, &m)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return t, nil
}

// WriteConversationJSONL 将 conversation 写入 w，每行一个 JSON 对象。
func WriteConversationJSONL(w io.Writer, t *Conversation) error {
	enc := json.NewEncoder(w)
	for _, m := range t.Messages {
		if err := enc.Encode(m); err != nil {
			return err
		}
	}
	return nil
}

// WriteMessages 将一组消息写入 w，每行一个 JSON 对象。
// 用于增量追加（调用方负责 O_APPEND 文件管理），区别于全量的 WriteConversationJSONL。
func WriteMessages(w io.Writer, msgs []*Message) error {
	enc := json.NewEncoder(w)
	for _, m := range msgs {
		if err := enc.Encode(m); err != nil {
			return err
		}
	}
	return nil
}

// ReadConversationJSONL 从 r 读取 conversation（每行一个 JSON 对象）。
func ReadConversationJSONL(r io.Reader) (*Conversation, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	t := NewConversation()
	line := 0
	for sc.Scan() {
		line++
		var m Message
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if m.ID == "" {
			m.ID = fmt.Sprintf("msg_%d", line)
		}
		t.Messages = append(t.Messages, &m)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return t, nil
}
