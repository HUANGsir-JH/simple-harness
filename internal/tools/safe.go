package tools

import (
	"strings"
)

// IsSafeCommand 判定命令是否命中只读安全白名单（前缀匹配）。
//
// 只对"单一简单命令"放行：含 shell 元字符（; | & > < $( ` 等）的命令行可能
// 是管道/重定向/命令组合，前缀匹配会误放行破坏性命令（如 echo pwned > key、
// ls && curl ... | sh，Bug02）；白名单命令带写参数也排除（find -delete/-exec、
// git branch -d、git diff --output=、sort -o——无元字符时元字符过滤堵不住）。
// 前缀须为完整词（"findx" 不以 "find " 命中，防未知命令借前缀放行）。
//
// 原实现位于 impl/policy.go（审批豁免 + 只读 shell 判定用，2026-08-16 下沉到
// 工具包：ShellCommandTool{Readonly} 强制只读复用；impl → tools 依赖已存在，
// 无环）。对应 codex is_known_safe_command / opencode untrusted 模式白名单。
func IsSafeCommand(cmd string) bool {
	lc := strings.ToLower(strings.TrimSpace(cmd))
	if hasShellMeta(lc) {
		return false
	}
	if hasDangerousWriteArg(lc) {
		return false
	}
	for _, p := range safeCommandPrefixes {
		if !strings.HasPrefix(lc, p) {
			continue
		}
		rest := lc[len(p):]
		if rest != "" && rest[0] != ' ' {
			continue // 前缀须为完整词（防 findx/gitee 之类借前缀放行）
		}
		return true
	}
	return false
}

// safeCommandPrefixes 是 shell 只读安全命令白名单（前缀匹配）：这类命令无需
// 审批直接放行，也是 explore 只读 shell 的允许集（2026-08-16 扩充调研常用
// 命令：git grep/show/ls-files、wc/stat/du/sort/uniq）。
var safeCommandPrefixes = []string{
	"ls", "dir", "cat", "type", "pwd", "echo", "printenv", "env", "which",
	"whoami", "git status", "git log", "git diff", "git branch",
	"git grep", "git show", "git ls-files",
	"grep", "find", "head", "tail", "get-content", "select-string",
	"wc", "stat", "du", "sort", "uniq",
}

// shellMetaChars 是白名单禁用的 shell 元字符/组合符：白名单只放行单一简单
// 命令，含这些符号说明有管道/重定向/命令组合/命令替换，前缀匹配不可信。
var shellMetaChars = []string{"&&", "||", ";", "|", "&", ">", "<", "$(", "`"}

// hasShellMeta 判定命令行是否含 shell 元字符。
func hasShellMeta(cmd string) bool {
	for _, m := range shellMetaChars {
		if strings.Contains(cmd, m) {
			return true
		}
	}
	return false
}

// safePrefixWriteArgs 是白名单前缀下的危险写参数（词边界匹配）：白名单按
// 前缀放行，但部分命令带写参数仍可写文件/删分支/执行任意内容，必须排除
// （Bug02：find / -delete 无元字符，元字符过滤堵不住；git branch -d main、
// git diff --output=file、sort -o file 同理）。
var safePrefixWriteArgs = map[string]map[string]bool{
	"find":       {"-delete": true, "-exec": true, "-execdir": true, "-ok": true},
	"git branch": {"-d": true, "-D": true, "-m": true, "-M": true, "-c": true, "-C": true, "--delete": true, "--move": true, "--copy": true},
	"git diff":   {"--output": true},
	"git show":   {"--output": true},
	"git log":    {"--output": true},
	"sort":       {"-o": true, "--output": true},
}

// hasDangerousWriteArg 判定命令是否携带白名单前缀下的危险写参数。前缀须为
// 完整词边界（"findx" 不以 "find " 前缀命中）。遍历按最长前缀优先——key
// "git branch" 与 "git" 不共存（白名单无裸 "git"），直接逐 key 前缀匹配即可。
func hasDangerousWriteArg(cmd string) bool {
	for prefix, args := range safePrefixWriteArgs {
		if !strings.HasPrefix(cmd, prefix) {
			continue
		}
		rest := cmd[len(prefix):]
		if rest != "" && rest[0] != ' ' {
			continue // 前缀须为完整词（防 findx 之类借前缀放行）
		}
		for _, f := range strings.Fields(rest) {
			// 支持 --flag=value 形式（如 --output=file 拆分后命中 --output）。
			key := f
			if i := strings.IndexByte(f, '='); i > 0 {
				key = f[:i]
			}
			if args[key] {
				return true
			}
		}
	}
	return false
}
