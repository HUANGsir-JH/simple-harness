// Package agentsmd 实现项目级指令文件（AGENTS.md / CLAUDE.md 回退）的发现、
// 读取与拼接（阶段四，ADR-043）。
//
// 纯逻辑、只依赖标准库：不依赖 session / middleware，由
// impl.AgentsMdMiddleware 调用并把结果追加进系统提示。发现规则（codex 对齐）：
// 从 cwd 向上找最近的含 .git 的目录作为项目根，收集项目根 → cwd（含两端）之间
// 每层目录的一个指令文件（先 AGENTS.md 后回退 CLAUDE.md），按根→cwd 顺序拼接。
// 预算内截断，任何文件读失败/为空都跳过——AGENTS.md 注入绝不终止回合。
package agentsmd

import (
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// 项目级指令文件名（逐目录先查 AGENTS.md，缺失回退 CLAUDE.md，ADR-043）。
const (
	DefaultFilename  = "AGENTS.md"
	FallbackFilename = "CLAUDE.md"
	// DefaultMaxBytes 是注入预算上限（IMPLEMENTATION_PLAN §9：200KB）。
	DefaultMaxBytes = 200 * 1024
)

// Options 控制注入：全局 persona 文件路径 + 总预算。
type Options struct {
	GlobalPath string // 全局 persona（~/.harness/agents.md）；空 = 不读全局
	MaxBytes   int    // 总预算；<=0 用 DefaultMaxBytes
}

// Compose 返回待注入系统提示的指令文本；无任何内容时返回 ""。
// 任何文件读失败/为空都跳过，绝不返回错误——AGENTS.md 不得终止回合。
func Compose(opts Options, cwd string) string {
	abs := absOf(cwd)
	if abs == "" {
		return ""
	}
	remaining := opts.MaxBytes
	if remaining <= 0 {
		remaining = DefaultMaxBytes
	}

	var sb strings.Builder
	appendSection := func(prefix, text string) {
		if remaining <= 0 || text == "" {
			return
		}
		text = truncateUTF8(text, remaining)
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		if prefix != "" {
			sb.WriteString(prefix)
		}
		sb.WriteString(text)
		remaining -= len(text)
	}

	// 全局 persona 恒在最前（原样）。
	if opts.GlobalPath != "" {
		if text, ok := readTrim(opts.GlobalPath); ok {
			appendSection("", text)
		}
	}

	// 项目级文件：根→cwd 顺序，每个前加来源标注。
	root := projectRoot(abs)
	for _, p := range discover(abs) {
		if remaining <= 0 {
			break
		}
		if text, ok := readTrim(p); ok {
			appendSection("来自 "+relPath(root, p)+"：\n", text)
		}
	}
	return sb.String()
}

// Discover 返回项目级指令文件路径（根→cwd 顺序）；无内容返回 nil。
// 供测试与后续复用。
func Discover(cwd string) []string {
	abs := absOf(cwd)
	if abs == "" {
		return nil
	}
	return discover(abs)
}

// discover 从 abs 向上到项目根（含）逐层收集指令文件路径，反转得根→cwd 顺序。
func discover(abs string) []string {
	root := projectRoot(abs)
	var found []string
	dir := abs
	for {
		if p := projectFile(dir); p != "" {
			found = append(found, p)
		}
		if dir == root {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	for i, j := 0, len(found)-1; i < j; i, j = i+1, j-1 {
		found[i], found[j] = found[j], found[i]
	}
	return found
}

// projectRoot 从 abs 向上找最近的含 .git（文件或目录，兼容 worktree/submodule）
// 的目录；找不到返回 abs 本身（只看当前目录）。
func projectRoot(abs string) string {
	start := abs
	for {
		if hasGit(abs) {
			return abs
		}
		parent := filepath.Dir(abs)
		if parent == abs { // 已到文件系统根
			return start
		}
		abs = parent
	}
}

// projectFile 返回 dir 下的指令文件路径：AGENTS.md 优先，缺失回退 CLAUDE.md；
// 两者都不存在（或不是普通文件）返回 ""。
func projectFile(dir string) string {
	for _, name := range []string{DefaultFilename, FallbackFilename} {
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// readTrim 读取文件并去除首尾空白；读失败或空内容返回 ok=false。
func readTrim(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return "", false
	}
	return text, true
}

// relPath 返回 p 相对 root 的路径；计算失败回退绝对路径。
func relPath(root, p string) string {
	if rel, err := filepath.Rel(root, p); err == nil {
		return rel
	}
	return p
}

// hasGit 判断 dir 是否含 .git（文件或目录）。
func hasGit(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// absOf 把 cwd 规范化为绝对路径；cwd 空用 os.Getwd()，失败返回 ""。
func absOf(cwd string) string {
	if cwd == "" {
		var err error
		if cwd, err = os.Getwd(); err != nil {
			return ""
		}
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return ""
	}
	return abs
}

// truncateUTF8 截断 s 至最多 max 字节，回退到 UTF-8 边界（不切断多字节 rune）。
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	end := max
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}
