package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/provider"
)

// ApplyPatchTool 应用 codex 风格的补丁（v1 支持基础子集）：
//
//	*** Begin Patch
//	*** Add File: path/to/new.txt
//	+内容行
//	*** Update File: path/to/old.txt
//	@@ 定位（可选，v1 忽略内容仅作分隔）
//	-旧行
//	+新行
//	 上下文行
//	*** Delete File: path/to/old.txt
//	*** End Patch
//
// 路径相对当前工作目录；更新时逐 hunk 在文件中匹配替换。
type ApplyPatchTool struct{}

func (ApplyPatchTool) Name() string { return "apply_patch" }

func (ApplyPatchTool) Spec() provider.ToolSpec {
	return provider.ToolSpec{
		Name:        "apply_patch",
		Description: "应用补丁编辑文件。格式：*** Begin Patch 开头、*** End Patch 结尾；文件操作头 *** Add File / *** Update File / *** Delete File；更新行以 -（删除）/ +（新增）/ 空格（上下文）开头，可用 @@ 分段。路径必须相对 cwd。",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"patch": {"type": "string", "description": "codex 风格补丁文本"}
			},
			"required": ["patch"]
		}`),
	}
}

func (ApplyPatchTool) Handle(_ context.Context, _ string, args json.RawMessage) (messages.ToolResult, error) {
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
	lines []string // 原始行（含 -/+ 前缀或 + 内容）
}

func applyPatch(patch string) (string, error) {
	ops, err := parsePatch(patch)
	if err != nil {
		return "", err
	}
	var reports []string
	for _, op := range ops {
		switch op.kind {
		case "add":
			if err := applyAdd(op); err != nil {
				return "", err
			}
			reports = append(reports, "Add File: "+op.path)
		case "delete":
			if err := os.Remove(op.path); err != nil {
				return "", fmt.Errorf("删除 %s: %w", op.path, err)
			}
			reports = append(reports, "Delete File: "+op.path)
		case "update":
			if err := applyUpdate(op); err != nil {
				return "", err
			}
			reports = append(reports, "Update File: "+op.path)
		}
	}
	if len(reports) == 0 {
		return "（空补丁，无文件操作）", nil
	}
	return strings.Join(reports, "\n"), nil
}

// parsePatch 解析 Begin/End 信封与文件操作。
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

func applyAdd(op fileOp) error {
	if op.path == "" {
		return fmt.Errorf("Add File: 路径为空")
	}
	if _, err := os.Stat(op.path); err == nil {
		return fmt.Errorf("Add File %s: 文件已存在", op.path)
	}
	var sb strings.Builder
	for _, l := range op.lines {
		sb.WriteString(strings.TrimPrefix(l, "+"))
		sb.WriteString("\n")
	}
	if err := os.MkdirAll(filepath.Dir(op.path), 0o755); err != nil {
		return fmt.Errorf("Add File %s: 创建目录: %w", op.path, err)
	}
	return os.WriteFile(op.path, []byte(sb.String()), 0o644)
}

func applyUpdate(op fileOp) error {
	data, err := os.ReadFile(op.path)
	if err != nil {
		return fmt.Errorf("Update File %s: %w", op.path, err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	hunks := parseHunks(op.lines)
	if len(hunks) == 0 {
		return fmt.Errorf("Update File %s: 无有效 hunk", op.path)
	}
	for _, hunk := range hunks {
		lines, err = applyHunk(lines, hunk)
		if err != nil {
			return fmt.Errorf("Update File %s: %w", op.path, err)
		}
	}
	return os.WriteFile(op.path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

// hunkLine 是 hunk 中的一行：kind 为 ' '（上下文）/ '-'（删除）/ '+'（新增）。
type hunkLine struct {
	kind byte
	text string
}

// parseHunks 把 Update 内容行按 @@ 分段为多个 hunk；空行作分段符。
func parseHunks(lines []string) [][]hunkLine {
	var hunks [][]hunkLine
	var cur []hunkLine
	flush := func() {
		if len(cur) > 0 {
			hunks = append(hunks, cur)
			cur = nil
		}
	}
	for _, l := range lines {
		if strings.HasPrefix(l, "@@") {
			flush()
			continue
		}
		if l == "" {
			flush()
			continue
		}
		switch l[0] {
		case ' ':
			cur = append(cur, hunkLine{' ', l[1:]})
		case '-':
			cur = append(cur, hunkLine{'-', l[1:]})
		case '+':
			cur = append(cur, hunkLine{'+', l[1:]})
		default:
			cur = append(cur, hunkLine{' ', l})
		}
	}
	flush()
	return hunks
}

// applyHunk 在一个 hunk 内：旧片段（上下文+删除行）在文件中匹配，替换为
// 新片段（上下文+新增行）。朴素顺序匹配，v1 不做智能定位。
func applyHunk(lines []string, hunk []hunkLine) ([]string, error) {
	var oldSeq, newSeq []string
	for _, hl := range hunk {
		switch hl.kind {
		case ' ', '-':
			oldSeq = append(oldSeq, hl.text)
			if hl.kind == ' ' {
				newSeq = append(newSeq, hl.text)
			}
		case '+':
			newSeq = append(newSeq, hl.text)
		}
	}
	if len(oldSeq) == 0 {
		return nil, fmt.Errorf("hunk 没有要删除或匹配的行")
	}
	idx := findSubsequence(lines, oldSeq)
	if idx < 0 {
		return nil, fmt.Errorf("hunk 内容在文件中未匹配到")
	}
	var out []string
	out = append(out, lines[:idx]...)
	out = append(out, newSeq...)
	out = append(out, lines[idx+len(oldSeq):]...)
	return out, nil
}

func findSubsequence(lines, seq []string) int {
	for i := 0; i+len(seq) <= len(lines); i++ {
		match := true
		for j := range seq {
			if lines[i+j] != seq[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
