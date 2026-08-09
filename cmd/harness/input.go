package main

import (
	"bufio"
	"fmt"
	"io"
)

// inputEvent 是 REPL 的 stdin 输入事件。
type inputEvent struct {
	esc  bool   // Esc/Ctrl+C 按下 → 中断当前回合
	line string // 提交的一整行（回车触发）
}

// readStdinEvents 从 reader 逐 rune 读取，产出一致化输入事件（单一读方：REPL
// 主循环与中断监听共用此 channel，避免多个 goroutine 竞争 stdin）。raw mode
// 下终端不回显，经 echo 自行回显（普通字符、退格擦除）。规则：
//
//	Esc(0x1b) / Ctrl+C(0x03) → esc 事件
//	\r 或 \n → 行提交（空行忽略）；Ctrl+D(0x04) → 关闭 channel（EOF）
//	退格(0x7f/0x08) → 删除行尾 + 回显 "\b \b"
//	其它 → 追加当前行 + 回显
func readStdinEvents(reader io.Reader, echo io.Writer) <-chan inputEvent {
	ch := make(chan inputEvent)
	go func() {
		defer close(ch)
		br := bufio.NewReader(reader)
		var line []rune
		for {
			r, _, err := br.ReadRune()
			if err != nil {
				return
			}
			switch r {
			case 0x1b, 0x03: // Esc / Ctrl+C
				line = line[:0]
				ch <- inputEvent{esc: true}
			case '\r', '\n':
				if len(line) > 0 {
					ch <- inputEvent{line: string(line)}
					line = line[:0]
				}
			case 0x7f, 0x08: // 退格
				if len(line) > 0 {
					line = line[:len(line)-1]
					if echo != nil {
						io.WriteString(echo, "\b \b")
					}
				}
			case 0x04: // Ctrl+D：非空行先提交（flush），再 EOF（退出）
				if len(line) > 0 {
					ch <- inputEvent{line: string(line)}
					line = line[:0]
				}
				return
			default:
				line = append(line, r)
				if echo != nil {
					fmt.Fprint(echo, string(r))
				}
			}
		}
	}()
	return ch
}
