package session

import "testing"

// TestEscapePath 验证可读转义规则（保留盘符 + 非法字符替换 + 去尾部分隔符）。
func TestEscapePath(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`D:\agent-project\harness`, "D__agent-project_harness"},
		{`C:\Users\86131`, "C__Users_86131"},
		{`D:\a\`, "D__a"},                    // 尾部分隔符去掉
		{`D:\a`, "D__a"},                     // 无尾随
		{`D:/a/b`, "D__a_b"},                 // 正斜杠统一
		{`a/b/c`, "a_b_c"},                   // 相对路径
		{`a<b>c|d?e*f:"g`, "a_b_c_d_e_f__g"}, // Windows 非法字符
		{"a\x00b", "a_b"},                    // 控制字符（NUL）
		{`:`, "_"},                           // 全被替换 → 兜底
	}
	for _, c := range cases {
		if got := EscapePath(c.in); got != c.want {
			t.Errorf("EscapePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
