package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/provider"
)

// ApplyPatchTool 应用 codex 风格的补丁（v1 支持基础子集，Bug07 增强 2026-08-10）：
//
//	*** Begin Patch
//	*** Add File: path/to/new.txt
//	+内容行
//	*** Update File: path/to/old.txt
//	@@ 定位文本（可选，用于消歧：hunk 从含该文本的行之后开始找）
//	-旧行
//	+新行
//	 上下文行
//	*** Delete File: path/to/old.txt
//	*** End Patch
//
// 路径相对进程工作目录或绝对路径。定位对齐 codex：
//   - 顺序推进（每个 hunk 从上一 hunk 匹配结束处继续找）
//   - @@ 定位文本作锚点（把搜索游标推到锚点行之后）
//   - 4 级模糊匹配（精确 → 忽略行尾空白 → 忽略首尾空白 → Unicode 标点规范化）
//   - 无 @@ 且多处匹配时报歧义（回填候选行号，模型用 @@ 重新定位），不静默错改
//   - 多操作补丁两阶段事务：全量校验+预演通过才统一落盘（失败零改动）
type ApplyPatchTool struct{}

func (ApplyPatchTool) Name() string { return "apply_patch" }

func (ApplyPatchTool) Spec() provider.ToolSpec {
	return provider.ToolSpec{
		Name:        "apply_patch",
		Description: "应用补丁编辑文件。格式：*** Begin Patch 开头、*** End Patch 结尾；文件操作头 *** Add File / *** Update File / *** Delete File；更新行以 -（删除）/ +（新增）/ 空格（上下文）开头，可用 @@ 分段；@@ 后跟定位文本（如函数名）可消除多处匹配的歧义。路径相对进程工作目录或绝对路径。",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"patch": {"type": "string", "description": "codex 风格补丁文本"}
			},
			"required": ["patch"]
		}`),
	}
}

func (ApplyPatchTool) Handle(_ context.Context, _ *middleware.RuntimeContext, _ string, args json.RawMessage) (messages.ToolResult, error) {
	var p struct {
		Patch string `json:"patch"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "apply_patch: 参数解析失败: " + err.Error()}
	}
	if strings.TrimSpace(p.Patch) == "" {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "apply_patch: patch 不能为空"}
	}
	report, err := applyPatch(p.Patch)
	if err != nil {
		return messages.ToolResult{}, &ToolError{RespondToModel: true, Message: "apply_patch: " + err.Error()}
	}
	return messages.ToolResult{Success: true, Content: report}, nil
}

// fileOp 是一次补丁里的文件操作。
type fileOp struct {
	kind  string // add | delete | update
	path  string
	lines []string // add: 内容行（+ 前缀）；update: 原始行（含 -/+ 前缀与 @@）
}

// chunk 是一个 @@ 分段的修改块（对齐 codex UpdateFileChunk）。
type chunk struct {
	anchor   string   // @@ 定位文本（空 = 无锚点）
	oldLines []string // 待匹配/删除的行
	newLines []string // 替换后的行
	isEOF    bool     // 命中文件尾部优先（*** End of File 标记后）
}

// replacement 是一次替换：替换 lines[start:start+oldLen] 为 newLines。
type replacement struct {
	start  int
	oldLen int
	newL   []string
}

// plannedOp 是预演通过后待落盘的操作（两阶段事务，Bug07(b)）。
type plannedOp struct {
	kind    string
	path    string
	content []byte // add/update 的新内容；delete 为 nil
}

// applyPatch 应用补丁。两阶段事务（Bug07(b)，2026-08-10）：
// 阶段 1 全量校验 + 预演（add 存在性 / delete 存在性 / update 逐 hunk 匹配
// 算出新内容），任一失败返回错误、磁盘零改动；阶段 2 全部通过才统一落盘。
func applyPatch(patch string) (string, error) {
	ops, err := parsePatch(patch)
	if err != nil {
		return "", err
	}
	var plans []plannedOp
	var reports []string
	for _, op := range ops {
		switch op.kind {
		case "add":
			if op.path == "" {
				return "", fmt.Errorf("Add File: 路径为空")
			}
			// Add 严格新建（与 write_file 分工：Add=新建，write=覆盖）。
			if _, err := os.Stat(op.path); err == nil {
				return "", fmt.Errorf("Add File %s: 文件已存在（新建用 Add，覆盖用 write_file）", op.path)
			}
			plans = append(plans, plannedOp{"add", op.path, addContent(op.lines)})
			reports = append(reports, "Add File: "+op.path)
		case "delete":
			if _, err := os.Stat(op.path); err != nil {
				return "", fmt.Errorf("Delete File %s: %w", op.path, err)
			}
			plans = append(plans, plannedOp{"delete", op.path, nil})
			reports = append(reports, "Delete File: "+op.path)
		case "update":
			data, err := os.ReadFile(op.path)
			if err != nil {
				return "", fmt.Errorf("Update File %s: %w", op.path, err)
			}
			lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
			chunks := parseChunks(op.lines)
			if len(chunks) == 0 {
				return "", fmt.Errorf("Update File %s: 无有效 hunk", op.path)
			}
			reps, err := computeReplacements(lines, chunks)
			if err != nil {
				return "", fmt.Errorf("Update File %s: %w", op.path, err)
			}
			newLines := applyReplacements(lines, reps)
			plans = append(plans, plannedOp{"update", op.path, []byte(strings.Join(newLines, "\n") + "\n")})
			reports = append(reports, "Update File: "+op.path)
		}
	}
	if len(reports) == 0 {
		return "（空补丁，无文件操作）", nil
	}
	// 阶段 2：全部校验通过后统一落盘。
	for _, p := range plans {
		switch p.kind {
		case "add":
			if err := os.MkdirAll(filepath.Dir(p.path), 0o755); err != nil {
				return "", fmt.Errorf("Add File %s: 创建目录: %w", p.path, err)
			}
			if err := os.WriteFile(p.path, p.content, 0o644); err != nil {
				return "", fmt.Errorf("Add File %s: %w", p.path, err)
			}
		case "delete":
			if err := os.Remove(p.path); err != nil {
				return "", fmt.Errorf("Delete File %s: %w", p.path, err)
			}
		case "update":
			if err := os.WriteFile(p.path, p.content, 0o644); err != nil {
				return "", fmt.Errorf("Update File %s: %w", p.path, err)
			}
		}
	}
	return strings.Join(reports, "\n"), nil
}

// parsePatch 解析 Begin/End 信封与文件操作。*** End of File 作为内容行传给
// parseChunks（设后续 hunk 的 EOF 优先语义）。
func parsePatch(patch string) ([]fileOp, error) {
	patch = strings.ReplaceAll(patch, "\r\n", "\n")
	raw := strings.Split(patch, "\n")
	// 去首尾空行，保留内容。
	for len(raw) > 0 && strings.TrimSpace(raw[0]) == "" {
		raw = raw[1:]
	}
	for len(raw) > 0 && strings.TrimSpace(raw[len(raw)-1]) == "" {
		raw = raw[:len(raw)-1]
	}
	if len(raw) < 2 || strings.TrimSpace(raw[0]) != "*** Begin Patch" || strings.TrimSpace(raw[len(raw)-1]) != "*** End Patch" {
		return nil, fmt.Errorf("patch 必须以 `*** Begin Patch` 开头、`*** End Patch` 结尾")
	}
	var ops []fileOp
	var cur *fileOp
	flush := func() {
		if cur != nil {
			ops = append(ops, *cur)
			cur = nil
		}
	}
	for _, line := range raw[1 : len(raw)-1] {
		switch {
		case strings.HasPrefix(line, "*** Add File: "):
			flush()
			cur = &fileOp{kind: "add", path: strings.TrimSpace(strings.TrimPrefix(line, "*** Add File: "))}
		case strings.HasPrefix(line, "*** Delete File: "):
			flush()
			cur = &fileOp{kind: "delete", path: strings.TrimSpace(strings.TrimPrefix(line, "*** Delete File: "))}
		case strings.HasPrefix(line, "*** Update File: "):
			flush()
			cur = &fileOp{kind: "update", path: strings.TrimSpace(strings.TrimPrefix(line, "*** Update File: "))}
		case strings.HasPrefix(line, "*** End of File"):
			// EOF 标记（追加/改文件尾用），作为内容行传给 parseChunks。
			if cur == nil {
				return nil, fmt.Errorf("*** End of File 出现在文件操作之前: %q", line)
			}
			cur.lines = append(cur.lines, line)
		case strings.HasPrefix(line, "*** "):
			return nil, fmt.Errorf("未知操作头: %q", line)
		default:
			if cur == nil {
				return nil, fmt.Errorf("补丁内容出现在文件操作头之前: %q", line)
			}
			if cur.kind == "delete" {
				return nil, fmt.Errorf("Delete File 后不应有内容: %q", line)
			}
			cur.lines = append(cur.lines, line)
		}
	}
	flush()
	return ops, nil
}

// parseChunks 把 Update 的原始行按 @@ 分段为多个 chunk；@@ 后的文本作为定位
// 锚点（消歧）；*** End of File 后的 chunk 标记 EOF 优先。
func parseChunks(lines []string) []chunk {
	var chunks []chunk
	var cur *chunk
	eof := false
	flush := func() {
		if cur != nil && (len(cur.oldLines) > 0 || len(cur.newLines) > 0) {
			chunks = append(chunks, *cur)
			cur = nil
		}
	}
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "*** End of File"):
			eof = true
			flush()
		case strings.HasPrefix(l, "@@"):
			flush()
			cur = &chunk{anchor: strings.TrimSpace(strings.TrimPrefix(l, "@@")), isEOF: eof}
		case l == "":
			flush()
		default:
			if cur == nil {
				cur = &chunk{isEOF: eof}
			}
			switch l[0] {
			case ' ':
				cur.oldLines = append(cur.oldLines, l[1:])
				cur.newLines = append(cur.newLines, l[1:])
			case '-':
				cur.oldLines = append(cur.oldLines, l[1:])
			case '+':
				cur.newLines = append(cur.newLines, l[1:])
			default:
				cur.oldLines = append(cur.oldLines, l)
				cur.newLines = append(cur.newLines, l)
			}
		}
	}
	flush()
	return chunks
}

// computeReplacements 计算一个文件的所有替换（对齐 codex compute_replacements）：
// lineIdx 顺序推进 + @@ 锚点定位 + 4 级模糊匹配 + 歧义兜底 + EOF 优先。
func computeReplacements(lines []string, chunks []chunk) ([]replacement, error) {
	var reps []replacement
	lineIdx := 0
	for _, c := range chunks {
		// @@ 定位锚点：把搜索游标推进到锚点行之后（消歧，对齐 codex context）。
		if c.anchor != "" {
			idx, ok := seekLine(lines, c.anchor, lineIdx)
			if !ok {
				return nil, fmt.Errorf("定位文本 %q 在文件中未找到", c.anchor)
			}
			lineIdx = idx + 1
		}
		if len(c.oldLines) == 0 {
			// 纯新增：插到文件尾（最后一个空行前，若存在）。
			insertIdx := len(lines)
			if lines[len(lines)-1] == "" {
				insertIdx = len(lines) - 1
			}
			reps = append(reps, replacement{insertIdx, 0, c.newLines})
			continue
		}
		positions := findSubsequences(lines, c.oldLines, lineIdx)
		if len(positions) == 0 {
			return nil, fmt.Errorf("hunk 内容在文件中未匹配到")
		}
		start := positions[0]
		switch {
		case c.isEOF:
			// EOF 优先：取最靠文件尾的匹配。
			start = positions[len(positions)-1]
		case len(positions) > 1 && c.anchor == "":
			// 歧义兜底（Bug07(a)，2026-08-10）：无 @@ 定位且多处匹配时拒绝静默
			// 错改，回填候选行号让模型用 @@ 重新定位。
			return nil, fmt.Errorf("hunk 内容在文件多处出现（行 %s），请用 @@ 定位（如 @@ 函数名）指定目标", lineNumbers(positions))
		}
		reps = append(reps, replacement{start, len(c.oldLines), c.newLines})
		lineIdx = start + len(c.oldLines) // 顺序推进，不回头（对齐 codex）
	}
	return reps, nil
}

// applyReplacements 逆序应用替换（后面的替换不移动前面的索引）。
func applyReplacements(lines []string, reps []replacement) []string {
	sort.Slice(reps, func(i, j int) bool { return reps[i].start > reps[j].start })
	for _, r := range reps {
		out := make([]string, 0, len(lines)+len(r.newL))
		out = append(out, lines[:r.start]...)
		out = append(out, r.newL...)
		out = append(out, lines[r.start+r.oldLen:]...)
		lines = out
	}
	return lines
}

// findSubsequences 从 start 起找 pattern 的全部匹配位置（4 级模糊比较）。
func findSubsequences(lines, pattern []string, start int) []int {
	if len(pattern) == 0 || len(pattern) > len(lines) {
		return nil
	}
	var out []int
	for i := start; i+len(pattern) <= len(lines); i++ {
		match := true
		for j := range pattern {
			if !lineEqual(lines[i+j], pattern[j]) {
				match = false
				break
			}
		}
		if match {
			out = append(out, i)
		}
	}
	return out
}

// lineEqual 4 级模糊比较（对齐 codex seek_sequence）：精确 → 忽略行尾空白 →
// 忽略首尾空白 → Unicode 标点规范化。
func lineEqual(a, b string) bool {
	if a == b {
		return true
	}
	if strings.TrimRight(a, " \t") == strings.TrimRight(b, " \t") {
		return true
	}
	ta, tb := strings.TrimSpace(a), strings.TrimSpace(b)
	if ta == tb {
		return true
	}
	return normalisePunct(ta) == normalisePunct(tb)
}

// punctNormaliser 把常见 Unicode 排版标点映射到 ASCII 等价（对齐 codex：em-dash
// → ASCII 连字符、弯引号 → ASCII 直引号等，模拟 git apply 的宽松匹配）。
var punctNormaliser = strings.NewReplacer(
	"‐", "-", "‑", "-", "‒", "-", "–", "-", "—", "-", "―", "-", "−", "-",
	"‘", "'", "’", "'", "‚", "'", "‛", "'",
	"“", "\"", "”", "\"", "„", "\"", "‟", "\"",
	" ", " ", " ", " ", " ", " ", " ", " ", " ", " ", " ", " ", " ", " ",
	" ", " ", " ", " ", " ", " ", " ", " ", " ", " ", "　", " ",
)

func normalisePunct(s string) string { return punctNormaliser.Replace(s) }

// seekLine 从 start 起找包含 anchor 文本的行（@@ 定位锚点，对齐 codex context）。
func seekLine(lines []string, anchor string, start int) (int, bool) {
	for i := start; i < len(lines); i++ {
		if strings.Contains(lines[i], anchor) {
			return i, true
		}
	}
	return 0, false
}

// lineNumbers 把 0-based 索引转成 1-based 行号列表（歧义错误回填给模型）。
func lineNumbers(idxs []int) string {
	var parts []string
	for _, i := range idxs {
		parts = append(parts, strconv.Itoa(i+1))
	}
	return strings.Join(parts, ", ")
}

// addContent 拼 Add File 的内容（去 + 前缀，行尾补换行）。
func addContent(lines []string) []byte {
	var sb strings.Builder
	for _, l := range lines {
		sb.WriteString(strings.TrimPrefix(l, "+"))
		sb.WriteString("\n")
	}
	return []byte(sb.String())
}
