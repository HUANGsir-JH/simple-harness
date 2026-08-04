package messages

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// SaveJSONL writes the thread's messages to path, one JSON object per line
// (append mode). Each message is self-contained; reading back the file fully
// reconstructs the thread. Safe for concurrent appends via os.O_APPEND.
func (t *Thread) SaveJSONL(path string) error {
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

// LoadThreadJSONL reads a session JSONL file into a thread. Messages missing
// their ID are assigned generated ones so the file is always a valid thread.
func LoadThreadJSONL(path string) (*Thread, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	t := NewThread()
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

// WriteThreadJSONL writes a thread to w, one JSON object per line.
func WriteThreadJSONL(w io.Writer, t *Thread) error {
	enc := json.NewEncoder(w)
	for _, m := range t.Messages {
		if err := enc.Encode(m); err != nil {
			return err
		}
	}
	return nil
}

// ReadThreadJSONL reads a thread from r (one JSON object per line).
func ReadThreadJSONL(r io.Reader) (*Thread, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	t := NewThread()
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
