package session

import "strings"

// EscapePath 把绝对路径转义成安全的目录名（可读转义，保留盘符字母）：
// `: \ /` 与 Windows 非法字符（<>"|?* 控制字符）→ `_`，去除尾部分隔符。
//
//	D:\agent-project\harness → D__agent-project_harness
func EscapePath(path string) string {
	var b strings.Builder
	for _, r := range path {
		if r == ':' || r == '\\' || r == '/' || r < 0x20 || strings.ContainsRune(`<>"|?*`, r) {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	s := strings.TrimRight(b.String(), "_")
	if s == "" {
		s = "_"
	}
	return s
}
