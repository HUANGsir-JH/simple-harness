package main

import (
	"io"
	"strings"
	"testing"
	"time"
)

// drainEvents 收集 channel 事件直到关闭或超时（避免死等）。
func drainEvents(ch <-chan inputEvent) []inputEvent {
	var out []inputEvent
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-time.After(2 * time.Second):
			return out
		}
	}
}

// TestReadStdinEventsLines 验证普通行输入 → line 事件。
func TestReadStdinEventsLines(t *testing.T) {
	ch := readStdinEvents(strings.NewReader("hello\nworld\r\n"), io.Discard)
	evs := drainEvents(ch)
	if len(evs) != 2 || evs[0].line != "hello" || evs[1].line != "world" {
		t.Fatalf("events: %+v", evs)
	}
}

// TestReadStdinEventsEsc 验证 Esc(0x1b) → esc 事件。
func TestReadStdinEventsEsc(t *testing.T) {
	ch := readStdinEvents(strings.NewReader("ab\x1bcd\n"), io.Discard)
	evs := drainEvents(ch)
	// Esc 清空当前行：ab 被清掉，cd 成为新行 → 只有 1 个 line 事件，且之前有 esc 事件。
	if len(evs) != 2 || !evs[0].esc || evs[1].line != "cd" {
		t.Fatalf("events: %+v", evs)
	}
}

// TestReadStdinEventsCtrlC 验证 Ctrl+C(0x03) → esc 事件。
func TestReadStdinEventsCtrlC(t *testing.T) {
	ch := readStdinEvents(strings.NewReader("\x03"), io.Discard)
	evs := drainEvents(ch)
	if len(evs) != 1 || !evs[0].esc {
		t.Fatalf("events: %+v", evs)
	}
}

// TestReadStdinEventsBackspace 验证退格删行尾 + 回显。
func TestReadStdinEventsBackspace(t *testing.T) {
	var echo strings.Builder
	ch := readStdinEvents(strings.NewReader("abc\x7fd\n"), &echo)
	evs := drainEvents(ch)
	// abc + 退格 → ab，再 d → abd
	if len(evs) != 1 || evs[0].line != "abd" {
		t.Fatalf("events: %+v", evs)
	}
	// 回显含退格擦除序列。
	if !strings.Contains(echo.String(), "\b \b") {
		t.Errorf("echo should contain backspace erase, got %q", echo.String())
	}
}

// TestReadStdinEventsCtrlD 验证 Ctrl+D(0x04) → EOF 关闭 channel。
func TestReadStdinEventsCtrlD(t *testing.T) {
	ch := readStdinEvents(strings.NewReader("hi\x04"), io.Discard)
	evs := drainEvents(ch)
	if len(evs) != 1 || evs[0].line != "hi" {
		t.Fatalf("events: %+v", evs)
	}
	// channel 应已关闭（drainEvents 返回而非超时）。
}

// TestReadStdinEventsUnicode 验证中文（UTF-8 多字节）行输入不拆字。
func TestReadStdinEventsUnicode(t *testing.T) {
	ch := readStdinEvents(strings.NewReader("中文测试\n"), io.Discard)
	evs := drainEvents(ch)
	if len(evs) != 1 || evs[0].line != "中文测试" {
		t.Fatalf("events: %+v", evs)
	}
}
