// Package impl 实现 harness 的全部内置中间件（ADR-021 capabilities）：
// 工具说明注入 / 会话状态 load-save / todo 偏离提醒 / 工具结果截断 / 审批。
// 框架契约（Middleware 接口、Chain、RuntimeContext、Approver/DeniedError）在
// internal/middleware 包；本包是这些 hook 的具体实现，无状态可并发（ADR-026），
// 由 cmd/harness 装配进共享链。
//
// 工具审批策略设计参照 codex approval + opencode permission 的简化版：
//   - 三档全局模式（readonly / acceptedit / bypass），决策粒度 = 工具分类
//   - shell 命令黑白名单前缀/子串匹配（opencode 的 bash 命令粒度简化）
//   - 会话级记忆（AgentState.Permission.Approved）：用户批准过的操作
//     （key = 工具名 / 规范化命令前缀）本会话不再询问（codex
//     ApprovedForSession 对位）
//   - 拒绝 ≠ Fatal（ADR-006）：审批拒绝作为失败结果回填模型，模型换思路
//
// 策略判定是纯函数（Decide），便于单测；人机交互（Approver）与策略解耦，
// CLI 层注入实现。级联拒绝 / 全局 allowlist / 拒绝反馈留增强。
package impl

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/agent-project/harness/internal/messages"
	"github.com/agent-project/harness/internal/tools"
) // 审批模式（AgentState.Permission.Mode；config approval.mode 默认值）。
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
	classRead    toolClass = iota // 只读（read_file/list_dir/glob）
	classEdit                     // 编辑（write_file/apply_patch）
	classTodo                     // 低风险状态工具（update_todo）
	classShell                    // shell_command
	classPlan                     // plan 工具（plan_enter/write_plan/plan_done，ADR-036）
	classAsk                      // 提问工具（ask_user，低风险）
	classControl                  // 子 agent 控制/查询工具（spawn/send/interrupt/resume/list/wait_task，ADR-045——无文件系统副作用，只读放行）
	classUnknown
)

var (
	readTools    = map[string]bool{"read_file": true, "list_dir": true, "glob": true, "skill": true}
	editTools    = map[string]bool{"write_file": true, "apply_patch": true}
	planTools    = map[string]bool{"plan_enter": true, "write_plan": true, "plan_done": true}
	controlTools = map[string]bool{
		"spawn_agent": true, "send_message": true, "interrupt_agent": true,
		"resume_agent": true, "list_agents": true, "wait_task": true,
	}
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
	case planTools[name]:
		return classPlan
	case name == "ask_user":
		return classAsk
	case controlTools[name]:
		return classControl
	default:
		return classUnknown
	}
}

// action 是一次工具调用的策略输入：class 是"这是什么工具"，targets 是
// "这次调用要碰什么"（对齐 opencode 多 pattern——参数对策略有发言权，
// 而非只看工具名，Bug03）。targets 是原始路径（未解析），范围判定由
// Decide 用 workspace 根解析后进行。
type action struct {
	class   toolClass
	targets []string
}

// actionOf 组合分类与目标路径提取。
func actionOf(call *messages.ToolCall) action {
	return action{class: classify(call.Name), targets: targetsOf(call)}
}

// targetsOf 提取一次调用的目标路径（原始，未解析；取不到返回 nil）：
// read_file/write_file/list_dir → path（list_dir 空 → "."，即 workspace 根）；
// glob → pattern 的静态前缀（到第一个 glob 元字符，范围判定用）；
// apply_patch → patch 内全部 *** Add/Update/Delete File 路径（去重）。
func targetsOf(call *messages.ToolCall) []string {
	switch call.Name {
	case "read_file", "write_file", "list_dir":
		var p struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(call.Args, &p); err != nil {
			return nil
		}
		if p.Path == "" {
			return []string{"."}
		}
		return []string{p.Path}
	case "glob":
		var p struct {
			Pattern string `json:"pattern"`
		}
		if err := json.Unmarshal(call.Args, &p); err != nil {
			return nil
		}
		return []string{globStaticPrefix(p.Pattern)}
	case "apply_patch":
		var p struct {
			Patch string `json:"patch"`
		}
		if err := json.Unmarshal(call.Args, &p); err != nil {
			return nil
		}
		return patchPaths(p.Patch)
	}
	return nil
}

// globStaticPrefix 取 glob pattern 的静态前缀（到第一个 * ? [ 前）作范围判定
// 目标；全元字符或空 → "."。
func globStaticPrefix(pattern string) string {
	if i := strings.IndexAny(pattern, "*?["); i >= 0 {
		pattern = pattern[:i]
	}
	if pattern == "" {
		return "."
	}
	return pattern
}

// patchPathHeader 匹配补丁文件操作头（Add/Update/Delete File: <path>）。
var patchPathHeader = regexp.MustCompile(`^\*\*\* (Add|Update|Delete) File: (.+)$`)

// patchPaths 提取补丁涉及的全部文件路径（去重）。只扫操作头，不做格式校验
// （工具层 parsePatch 负责严格校验）。
func patchPaths(patch string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(patch, "\n") {
		m := patchPathHeader.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		p := strings.TrimSpace(m[2])
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
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
//
// 只对"单一简单命令"放行：含 shell 元字符（; | & > < $( ` 等）的命令行可能
// 是管道/重定向/命令组合，前缀匹配会误放行破坏性命令（如 echo pwned > key、
// ls && curl ... | sh，Bug02）；find 携带 -delete/-exec 等危险参数也排除
// （find / -delete 无元字符，元字符过滤堵不住）。
func isSafe(cmd string) bool {
	if hasShellMeta(cmd) {
		return false
	}
	if findIsDangerous(cmd) {
		return false
	}
	lc := strings.ToLower(strings.TrimSpace(cmd))
	for _, p := range safeCommandPrefixes {
		if strings.HasPrefix(lc, p) {
			return true
		}
	}
	return false
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

// findDangerArgs 是 find 命令的危险参数（删除/执行/交互确认）。find 在白名单
// 里按前缀放行，但 -delete / -exec / -ok 可破坏或执行任意内容，必须排除
// （Bug02：find / -delete 无元字符，元字符过滤堵不住）。
var findDangerArgs = map[string]bool{"-delete": true, "-exec": true, "-execdir": true, "-ok": true}

// findIsDangerous 判定 find 命令是否携带危险参数。仅当命令首 token 是 find 时
// 检查（白名单语义），词边界匹配避免误命中 -executive 之类。
func findIsDangerous(cmd string) bool {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(cmd)))
	if len(fields) == 0 || fields[0] != "find" {
		return false
	}
	for _, f := range fields[1:] {
		if findDangerArgs[f] {
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

// killPIDOf 提取 shell_command 的 kill_pid 参数（>0 = kill 模式，ADR-038）。
func killPIDOf(call *messages.ToolCall) int {
	if call.Name != "shell_command" {
		return 0
	}
	var p struct {
		KillPID int `json:"kill_pid"`
	}
	if err := json.Unmarshal(call.Args, &p); err != nil {
		return 0
	}
	return p.KillPID
}

// ApprovalKey 返回工具调用的单 key 审批记忆 key（shell → 规范化命令前缀；
// 其它工具 → 工具名）。文件工具不再用它——多路径粒度见 approvalKeys
// （Bug03：批准一次 write_file 记住整个工具会让后续任何路径免审）。
// kill 模式（ADR-038）显式派生 "kill <pid>"：command 为空时
// NormalizeCommand("")="" 空 key 若被记住会命中任意空命令调用，且无语义。
func ApprovalKey(call *messages.ToolCall) string {
	if call.Name == "shell_command" {
		if pid := killPIDOf(call); pid > 0 {
			return "kill " + strconv.Itoa(pid)
		}
		return NormalizeCommand(cmdOf(call))
	}
	return call.Name
}

// approvalKeys 返回一次调用的审批记忆匹配 key（多 key，对齐 opencode 多
// pattern）：
//   - 文件工具（classRead/classEdit）→ 每个目标路径一条 `<tool>:<绝对路径>`
//     （解析基于 workspace 根 ws；批准"本会话记住"时全部记入 approved，
//     apply_patch 记住其每个文件路径）
//   - 其它 → ApprovalKey 单 key（shell 命令 / 工具名）
func approvalKeys(call *messages.ToolCall, ws string) []string {
	switch classify(call.Name) {
	case classRead, classEdit:
		var keys []string
		for _, t := range targetsOf(call) {
			keys = append(keys, call.Name+":"+tools.ResolvePath(ws, t))
		}
		return keys
	default:
		return []string{ApprovalKey(call)}
	}
}

// allApproved 判定 keys 里每个都已在 approved 中（多 key 全命中才 Allow）。
func allApproved(approved, keys []string) bool {
	for _, k := range keys {
		found := false
		for _, a := range approved {
			if a == k {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// anyOutside 判定工具的任一目标路径解析后在 workspace 外（软边界：越界 →
// 询问，让用户判断；范围内按 class 规则）。
func anyOutside(call *messages.ToolCall, ws string) bool {
	for _, t := range targetsOf(call) {
		if !tools.InWorkspace(ws, tools.ResolvePath(ws, t)) {
			return true
		}
	}
	return false
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
// approved：会话级已批准 key 列表（AgentState.Permission.Approved）；
// ws：workspace 根（会话启动目录 state.CWD；空 = 进程 cwd）——文件工具的
// 目标路径相对它解析并判定越界（Bug03）；
// plan：是否处于 plan 模式（AgentState.PlanMode，ADR-036）——plan 分支在
// bypass 之前（plan 强只读优先于权限模式）。
// 返回 Outcome + 拒绝理由（仅 OutcomeDeny 时非空）。
//
// 判定顺序（非 plan）：
//  1. bypass → Allow（完全信任，含越界；用户显式选择不审批）
//  2. 会话记忆命中（全部目标路径 key 已批准）→ Allow
//  3. 文件工具越界（任一目标在 workspace 外）→ Ask（软边界：范围内按 class
//     规则，范围外交给人判断）
//  4. 只读 / todo → Allow
//  5. 编辑 → acceptedit Allow；readonly Ask
//  6. shell → 危险 Ask / 安全 Allow / 其它 Ask
//  7. 未知工具 → Ask（保守）
//
// plan=true（强只读，工具全量可见不做过滤）：
//   - read/todo/ask → Allow
//   - write_plan/plan_done → Allow（Handle 内 HITL）；plan_enter → Deny
//   - shell → isPlanReadonlyShell Allow 否则 Deny
//   - edit → Deny；未知 → Deny
func Decide(call *messages.ToolCall, mode string, approved []string, ws string, plan bool) (Outcome, string) {
	if call == nil {
		return OutcomeDeny, "空工具调用"
	}
	if plan {
		switch classify(call.Name) {
		case classRead, classTodo, classAsk, classControl:
			return OutcomeAllow, ""
		case classPlan:
			if call.Name == "plan_enter" {
				return OutcomeDeny, "已在 plan 模式，无需再次进入"
			}
			return OutcomeAllow, "" // write_plan/plan_done：Handle 内 HITL
		case classShell:
			switch classifyPlanShell(cmdOf(call)) {
			case planShellReadonly:
				return OutcomeAllow, ""
			case planShellWrite:
				return OutcomeDeny, "plan 模式下 shell 仅允许只读命令（检测到写命令/写参数/重定向）"
			default: // planShellUnknown：纯 Deny 失败模式，理由提示换只读探查方式
				return OutcomeDeny, "plan 模式下 shell 仅允许只读命令（无法确认该命令只读，已拒绝；可改用只读探查 flag 如 --version）"
			}
		case classEdit:
			return OutcomeDeny, "plan 模式下禁止写文件"
		default:
			return OutcomeDeny, "plan 模式下未知工具已拒绝"
		}
	}
	if mode == ModeBypass {
		return OutcomeAllow, ""
	}
	keys := approvalKeys(call, ws)
	if len(keys) > 0 && allApproved(approved, keys) {
		return OutcomeAllow, ""
	}
	switch classify(call.Name) {
	case classRead:
		if anyOutside(call, ws) {
			return OutcomeAsk, "" // 越界读 → 询问（范围内放行）
		}
		return OutcomeAllow, ""
	case classTodo:
		return OutcomeAllow, ""
	case classAsk:
		return OutcomeAllow, "" // ask_user 低风险放行（两模式）
	case classControl:
		return OutcomeAllow, "" // 子 agent 控制工具无文件系统副作用（ADR-045）
	case classPlan:
		if call.Name == "plan_enter" {
			return OutcomeAllow, "" // plan_enter 放行（Handle 内 HITL 确认）
		}
		return OutcomeDeny, "write_plan/plan_done 仅在 plan 模式下可用"
	case classEdit:
		if anyOutside(call, ws) {
			return OutcomeAsk, "" // 越界写 → 询问（软边界优先）
		}
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
