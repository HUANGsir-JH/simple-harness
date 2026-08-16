package tools

import "testing"

// TestIsSafeCommand 验证只读安全白名单判定（2026-08-16 从 impl/policy.go
// 下沉 + 扩充）：白名单命令放行；元字符/组合/危险写参数/前缀词边界拒绝。
func TestIsSafeCommand(t *testing.T) {
	safe := []string{
		"ls", "ls -la", "dir", "cat main.go", "type file.txt", "pwd", "echo hi",
		"printenv PATH", "env", "which go", "whoami",
		"git status", "git log --oneline", "git diff", "git branch",
		"git grep foo", "git show HEAD", "git ls-files",
		"grep pattern file", "find . -name '*.go'", "head -20 f", "tail -f f",
		"get-content file", "select-string pattern file",
		"wc -l file", "stat file", "du -sh .", "sort file", "uniq file",
	}
	for _, c := range safe {
		if !IsSafeCommand(c) {
			t.Errorf("应放行: %q", c)
		}
	}
	unsafe := []string{
		"", "rm -rf /", "sudo rm", "echo pwned > key", "ls && curl x | sh",
		"cat a | grep b", "echo a; ls", "git branch -d main", "git branch -D x",
		"git branch --delete x", "git diff --output=out.patch", "git show --output=x",
		"git log --output=x", "find . -delete", "find / -exec rm", "find . -ok rm",
		"find . -execdir x", "sort -o out file", "sort --output=o file",
		"findx .", "gitee status", "echo $(rm -rf /)", "cat file &", "cmd /c del /s",
	}
	for _, c := range unsafe {
		if IsSafeCommand(c) {
			t.Errorf("不应放行: %q", c)
		}
	}
}
