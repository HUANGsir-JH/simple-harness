// Package approval 实现工具审批策略（阶段三权限，ADR-029）。
//
// 设计参照 codex approval + opencode permission 的简化版：
//   - 三档全局模式（readonly / acceptedit / bypass），决策粒度 = 工具分类
//     + shell 命令黑白名单前缀/子串匹配（opencode 的 bash 命令粒度简化）
//   - 会话级记忆（AgentState.Permission.Approved）：用户批准过的操作
//     （key = 工具名 / 规范化命令前缀）本会话不再询问（codex
//     ApprovedForSession 对位）
//   - 拒绝 ≠ Fatal（ADR-006）：审批拒绝作为失败结果回填模型，模型换思路
//
// 策略判定是纯函数（Decide），便于单测；人机交互（Approver）与策略解耦，
// CLI 层注入实现。级联拒绝 / 全局 allowlist / 拒绝反馈留增强。
package approval

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/agent-project/harness/internal/messages"
)

// 审批模式（AgentState.Permission.Mode；config approval.mode 默认值）。
const (
	// ModeReadonly 只读模式：只读操作放行；写操作 / shell 命令询问。
	ModeReadonly = "readonly"
	// ModeAcceptEdits 接受编辑：只读 + 编辑工具放行；shell 命令询问
	// （危险命令黑名单任何非 bypass 模式下都询问）。
	ModeAcceptEdits = "acceptedit"
	// ModeBypass 绕过：全部放行（用户显式完全信任，不审批）。
	ModeBypass = "bypass"
)

// DefaultMode 是未配置 approval.mode 时的默认模式。
const DefaultMode = ModeAcceptEdits

// Modes 是全部合法模式（validate 用）。
var Modes = []string{ModeReadonly, ModeAcceptEdits, ModeBypass}

// toolClass 是工具分类（决定审批策略的默认粒度）。
type toolClass int

const (
	classRead toolClass = iota // 只读（read_file/list_dir/glob）
	classEdit                  // 编辑（write_file/apply_patch）
	classTodo                  // 低风险状态工具（update_todo）
	classShell                 // shell_command
	classUnknown
)

var (
	readTools = map[string]bool{"read_file": true, "list_dir": true, "glob": true}
	editTools = map[string]bool{"write_file": true, "apply_patch": true}
)

func classify(name string) toolClass {
	switch {
	case readTools[name]:
		return classRead
	case editTools[name]:
		return classEdit
	case name == "update_todo":
		return classTodo
	case name == "shell_command":
		return classShell
	default:
		return classUnknown
	}
}

// shell 只读安全命令白名单（前缀匹配）：这类命令无需审批直接放行。
// 对应 codex is_known_safe_command / opencode untrusted 模式白名单。
var safeCommandPrefixes = []string{
	"ls", "dir", "cat", "type", "pwd", "echo", "printenv", "env", "which",
	"whoami", "git status", "git log", "git diff", "git branch",
	"grep", "find", "head", "tail", "get-content", "select-string",
}

// shell 危险命令黑名单（子串匹配，小写比较）：这类命令触发审批，
// 即使 acceptedit 模式（用户编辑授权不等于信任破坏性命令）。
var dangerousSubstrings = []string{
	"rm -rf", "rm -fr", "rm -r ", "sudo ", "chmod -R", "chmod 777", "chown -R",
	"mkfs", "dd if=", "del /s", "rmdir /s", "format ", "diskpart",
	"taskkill /f", "shutdown", "reg delete",
}

// dangerousPipe 匹配"下载即执行"模式（curl|sh 等），常见供应链风险。
var dangerousPipe = regexp.MustCompile(`(?i)(curl|wget|iwr|invoke-webrequest)\s+\S+.*\|\s*(sh|bash|powershell|pwsh|cmd|zsh)`)

// NormalizeCommand 规范化 shell 命令为审批 key：trim + 折叠空白 + 取前 2
// token（`git status --porcelain` → `git status`）。对齐 opencode arity
// 字典理念（git=2）：把"语义相同的命令"归约到同一 key，审批记忆与
// 黑白名单共用。v1 不做 bash 语法解析（tree-sitter 留增强）。
func NormalizeCommand(cmd string) string {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return ""
	}
	n := 2
	if len(fields) < n {
		n = len(fields)
	}
	return strings.Join(fields[:n], " ")
}

// isDangerous 判定命令是否命中危险黑名单（任何非 bypass 模式都应询问）。
func isDangerous(cmd string) bool {
	lc := strings.ToLower(cmd)
	for _, p := range dangerousSubstrings {
		if strings.Contains(lc, p) {
			return true
		}
	}
	return dangerousPipe.MatchString(cmd)
}

// isSafe 判定命令是否命中只读安全白名单（前缀匹配）。
func isSafe(cmd string) bool {
	lc := strings.ToLower(strings.TrimSpace(cmd))
	for _, p := range safeCommandPrefixes {
		if strings.HasPrefix(lc, p) {
			return true
		}
	}
	return false
}

// cmdOf 从 shell_command 参数提取 command 字段（空串表示解析失败/无命令）。
func cmdOf(call *messages.ToolCall) string {
	if call.Name != "shell_command" {
		return ""
	}
	var p struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(call.Args, &p); err != nil {
		return ""
	}
	return p.Command
}

// ApprovalKey 返回工具调用的审批记忆 key（会话级 approved 匹配用）。
// shell → 规范化命令前缀；其它工具 → 工具名（批准一次本会话该工具放行）。
func ApprovalKey(call *messages.ToolCall) string {
	if call.Name == "shell_command" {
		return NormalizeCommand(cmdOf(call))
	}
	return call.Name
}

// Outcome 是策略决策结果。
type Outcome int

const (
	// OutcomeAllow 直接放行。
	OutcomeAllow Outcome = iota
	// OutcomeAsk 需人工审批（Approver.Request）。
	OutcomeAsk
	// OutcomeDeny 直接拒绝（携带理由）。
	OutcomeDeny
)

// Decide 判定一次工具调用的审批结果（纯函数）。
//
// mode：当前审批模式（readonly/acceptedit/bypass）；
// approved：会话级已批准 key 列表（AgentState.Permission.Approved）。
// 返回 Outcome + 拒绝理由（仅 OutcomeDeny 时非空）。
//
// 判定顺序（v1 简化，opencode 规则集"最后匹配胜出"不适用——我们无规则集）：
//  1. bypass → Allow（完全信任）
//  2. 会话记忆命中 → Allow（用户本会话明确批准过）
//  3. 只读 / todo → Allow
//  4. 编辑 → acceptedit Allow；readonly Ask
//  5. shell → 危险 Ask / 安全 Allow / 其它 Ask
//  6. 未知工具 → Ask（保守）
func Decide(call *messages.ToolCall, mode string, approved []string) (Outcome, string) {
	if call == nil {
		return OutcomeDeny, "空工具调用"
	}
	if mode == ModeBypass {
		return OutcomeAllow, ""
	}
	key := ApprovalKey(call)
	for _, k := range approved {
		if k == key {
			return OutcomeAllow, ""
		}
	}
	switch classify(call.Name) {
	case classRead, classTodo:
		return OutcomeAllow, ""
	case classEdit:
		if mode == ModeAcceptEdits {
			return OutcomeAllow, ""
		}
		return OutcomeAsk, "" // readonly → 询问
	case classShell:
		cmd := cmdOf(call)
		switch {
		case isDangerous(cmd):
			return OutcomeAsk, "" // 危险命令 → 询问（即使 acceptedit）
		case isSafe(cmd):
			return OutcomeAllow, "" // 只读安全命令 → 放行
		default:
			return OutcomeAsk, "" // 其它命令 → 询问
		}
	default:
		return OutcomeAsk, "" // 未知工具 → 询问（保守）
	}
}
