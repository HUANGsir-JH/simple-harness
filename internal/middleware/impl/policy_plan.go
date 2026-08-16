package impl

// plan 模式 shell 只读判定（ADR-036 修订，2026-08-12）：
//
// 用「写黑名单反向判定」替代旧 isPlanReadonlyShell 的只读白名单前缀匹配
// （plan-mode-review-2026-08-12 缺陷 01/02 同根因：命令名前缀猜只读，既过松到
// 放行 sed -i/sort -o/env sh，又过严到误拒 python --version 等 38/56）。
//
// 判定思路（三个参考源都不用命令白名单，这里选「反向」——不查是否只读，
// 而查是否写）：
//   - 命中写黑名单（写命令名/解释器跳板/包管理/系统服务 + 写子命令 + 写参数
//     双面命令 + 写形态拦截）→ planShellWrite
//   - 明确只读（只读 allowlist / 只读 flag 豁免 / 写子命令只读例外表）
//     → planShellReadonly
//   - 未命中写黑名单也非明确只读（未知命令）→ planShellUnknown
//     （纯 Deny 失败模式：unknown 由 Decide 归 Deny，理由回填模型换思路）
//
// 纯函数（无 I/O），与 Decide 解耦；policy_test.go 锁定的非 plan 判定
// （IsSafeCommand/isDangerous——白名单已下沉 tools 包，2026-08-16）不受影响。

import (
	"regexp"
	"strings"
)

// planShellClass 是 plan 模式下 shell 命令的判定类别。
type planShellClass int

const (
	// planShellReadonly 明确只读 → Allow。
	planShellReadonly planShellClass = iota
	// planShellWrite 命中写黑名单/写形态 → Deny。
	planShellWrite
	// planShellUnknown 未命中写黑名单也非明确只读 → Deny（纯 Deny 失败模式，
	// 理由可与 write 区分，提示模型换只读探查方式）。
	planShellUnknown
)

// planSegmentSplit 拆分 shell 管道/组合分隔符（| && || ; &），每段逐段判定。
// 保留管道与组合（git log | head -5、git status && git log 逐段放行）。
var planSegmentSplit = regexp.MustCompile(`[\|;&]`)

// --- 写黑名单 -------------------------------------------------------------

// planWriteCommands 是写命令名（首 token 精确匹配）。这些命令的主用途就是
// 修改文件/系统状态，plan 模式下直接拒绝。
var planWriteCommands = map[string]bool{
	"rm": true, "mv": true, "cp": true, "touch": true, "mkdir": true,
	"ln": true, "chmod": true, "chown": true, "truncate": true, "dd": true,
	"tee": true, "install": true, "rsync": true, "shred": true,
	"unlink": true, "trash": true, "unzip": true, "zip": true, "7z": true,
	"7za": true, "patch": true, "ed": true, "mktemp": true, "mkfs": true,
	"mkfifo": true, "mknod": true, "gzip": true, "gunzip": true, "bzip2": true,
	"xz": true, "sort": false, /* sort 只读（-o 写由写参数表拦） */
}

// planInterpreterCommands 是解释器/执行跳板（首 token 精确匹配）。plan 模式下
// 解释器可能执行任意脚本（env sh evil.sh 真实写盘，缺陷 01），一律拒绝；
// env 因此从原 safeCommandPrefixes 的只读白名单移出。
var planInterpreterCommands = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "python": true, "python3": true,
	"node": true, "ruby": true, "perl": true, "env": true, "eval": true,
	"exec": true, "xargs": true, "nohup": true, "pwsh": true, "powershell": true,
	"cmd": true, "fish": true, "ksh": true, "csh": true, "php": true, "lua": true,
}

// planPkgCommands 是包管理/构建（首 token 精确匹配）。装依赖/构建产物都是
// 系统状态变更。go/cargo 首 token 不在此表（子命令表精确区分读写子命令）。
var planPkgCommands = map[string]bool{
	"pip": true, "pip3": true, "npm": true, "yarn": true, "npx": true,
	"pnpm": true, "brew": true, "apt": true, "apt-get": true, "make": true,
	"gradle": true, "mvn": true, "poetry": true, "uv": true, "bun": true,
	"conda": true, "nix-env": true, "gem": true, "composer": true,
	"dotnet": true, "stack": true, "cabal": true,
}

// planSysCommands 是系统/服务/网络/容器（首 token 精确匹配）。
var planSysCommands = map[string]bool{
	"sudo": true, "systemctl": true, "service": true, "kill": true,
	"killall": true, "pkill": true, "mount": true, "umount": true,
	"crontab": true, "at": true, "curl": true, "wget": true, "scp": true,
	"sftp": true, "ssh": true, "telnet": true, "nc": true, "iptables": true,
	"docker": true, "kubectl": true, "helm": true, "terraform": true,
	"podman": true, "nix": true, "rpm": true, "dpkg": true, "shutdown": true,
	"reboot": true, "poweroff": true, "fdisk": true, "parted": true,
	"defaults": true, "launchctl": true,
}

// planWriteSubcommands 是写子命令表：<首 token> → 写子命令集合。git/go/cargo
// 是双面命令（读写都有子命令），只对写子命令拒绝（读子命令走 planReadonlySubs
// 例外表放行）。
var planWriteSubcommands = map[string]map[string]bool{
	"git": {
		"add": true, "commit": true, "push": true, "reset": true,
		"checkout": true, "stash": true, "clean": true, "config": true,
		"init": true, "clone": true, "merge": true, "rebase": true,
		"cherry-pick": true, "revert": true, "mv": true, "rm": true,
		"switch": true, "restore": true, "apply": true, "branch": true,
		"tag": true, "remote": true, "fetch": true, "pull": true,
		"prune": true, "am": true, "update-ref": true,
	},
	"go": {
		"build": true, "install": true, "get": true, "mod": true,
		"generate": true, "run": true, "test": true, "work": true, "fix": true,
		"env": true, // go env 写形态（-w）由 planReadonlySubs rejects 拦后落到这里
	},
	"cargo": {
		"build": true, "install": true, "publish": true, "update": true,
		"new": true, "init": true, "add": true, "remove": true, "run": true,
		"test": true,
	},
}

// planWriteArgs 是写参数表（双面命令的写模式）。这些命令本身可只读（在
// planReadonlyCommands），但携带这些参数/子串时是写操作。用子串匹配：
// sed -i 的前缀形态（-i.bak/-i”）与 sed w 写命令（前面可能带单引号/分号）
// 都要命中，故不用词边界。
var planWriteArgs = map[string][]string{
	"sed":   {"-i", "--in-place", "-I", "w "},
	"sort":  {"-o"},
	"find":  {"-delete", "-exec", "-execdir", "-ok"},
	"gofmt": {"-w"},
	"awk":   {"system("},
	"tar":   {"-x", "-c", "--extract", "--create"},
}

// anyPlanWriteArg 判定参数串是否命中写 flag 表（子串匹配，见 planWriteArgs）。
func anyPlanWriteArg(args string, flags []string) bool {
	for _, f := range flags {
		if strings.Contains(args, f) {
			return true
		}
	}
	return false
}

// --- 只读豁免 -------------------------------------------------------------

// planProbeFlags 是只读 flag 通用规则（纯探查）。命令后若只跟这些 flag
// （或首参是 help/--help），无论命令是否在写黑名单都放行——解决
// python --version / docker --help / npm ls 等整类假阴性（缺陷 02）。
var planProbeFlags = map[string]bool{
	"--version": true, "-version": true, "-v": true, "-V": true,
	"--help": true, "-help": true, "-h": true, "version": true, "help": true,
}

// isPlanPureProbe 判定命令段是否"纯探查"：所有参数都是只读 flag，或首参是
// help/--help（带主题的帮助文本也只读）。要求至少一个参数（裸命令不算）。
func isPlanPureProbe(fields []string) bool {
	if len(fields) < 2 {
		return false
	}
	if fields[1] == "help" || fields[1] == "--help" {
		return true
	}
	for _, a := range fields[1:] {
		if !planProbeFlags[a] {
			return false
		}
	}
	return true
}

// planReadonlySub 是写子命令只读例外表的一条：cmd 的 sub 子命令可只读。
// flag 语义：
//   - "*"：该子命令任意后续参数都只读（subject to rejects）；
//   - ""：恰好两 token（cmd sub，无后续参数）；
//   - 其它：第三 token 前缀匹配（如 --get、graph、list）。
//
// rejects：后续参数命中任一前缀 → 不豁免（即该调用是写形态）。
type planReadonlySub struct {
	cmd     string
	sub     string
	flag    string
	rejects []string
}

// planReadonlySubs 覆盖 review 语料的只读 git/构建/容器查询（缺陷 02）：
// git status/log/diff 等只读子命令、go list/vet/doc/env、npm ls、cargo tree、
// make -n、docker ps 等。写子命令（git add/go build 等）不在表中，走
// planWriteSubcommands 拒绝。
var planReadonlySubs = []planReadonlySub{
	{"git", "status", "*", nil}, {"git", "log", "*", nil}, {"git", "diff", "*", nil},
	{"git", "show", "*", nil}, {"git", "ls-files", "*", nil}, {"git", "rev-parse", "*", nil},
	{"git", "blame", "*", nil}, {"git", "grep", "*", nil}, {"git", "cat-file", "*", nil},
	{"git", "ls-tree", "*", nil}, {"git", "shortlog", "*", nil}, {"git", "describe", "*", nil},
	{"git", "branch", "*", []string{"-d", "-D", "-m", "-M", "-c", "-f"}},
	{"git", "tag", "*", []string{"-d", "-D", "-a", "-s", "-f", "-c", "-m"}},
	{"git", "remote", "*", []string{"add", "remove", "rename", "set-url", "set-head", "prune", "delete"}},
	{"git", "config", "", nil}, {"git", "config", "--get", nil},
	{"git", "config", "--get-regexp", nil}, {"git", "config", "--list", nil},
	{"git", "config", "-l", nil},
	{"git", "stash", "list", nil}, {"git", "stash", "show", nil},
	{"git", "apply", "--check", nil}, {"git", "apply", "--stat", nil},
	{"go", "list", "*", []string{"-m", "all"}}, {"go", "vet", "*", nil},
	{"go", "doc", "*", nil}, {"go", "env", "*", []string{"-w"}},
	{"go", "version", "*", nil}, {"go", "mod", "graph", nil},
	{"go", "test", "-list", nil},
	{"cargo", "tree", "*", nil}, {"cargo", "metadata", "*", nil},
	{"npm", "ls", "*", nil}, {"npm", "list", "*", nil},
	{"npm", "view", "*", nil}, {"npm", "search", "*", nil},
	{"make", "-n", "*", nil}, {"make", "--dry-run", "*", nil},
	{"docker", "ps", "*", nil}, {"docker", "images", "*", nil},
	{"docker", "inspect", "*", nil}, {"docker", "info", "*", nil},
	{"docker", "version", "*", nil},
	{"kubectl", "get", "*", nil}, {"kubectl", "describe", "*", nil},
	{"terraform", "plan", "*", nil}, {"terraform", "show", "*", nil},
	{"terraform", "fmt", "-check", nil},
}

// planReadonlySubMatches 命中"写子命令只读例外表"。命中 → 明确只读。
func planReadonlySubMatches(fields []string) bool {
	if len(fields) < 2 {
		return false
	}
	cmd := strings.ToLower(fields[0])
	sub := strings.ToLower(fields[1])
	for _, rs := range planReadonlySubs {
		if rs.cmd != cmd || rs.sub != sub {
			continue
		}
		rejected := false
		for _, r := range rs.rejects {
			for _, f := range fields[2:] {
				if strings.HasPrefix(f, r) {
					rejected = true
					break
				}
			}
			if rejected {
				break
			}
		}
		if rejected {
			continue // 该调用命中写形态（如 git tag -d），不豁免
		}
		switch rs.flag {
		case "*":
			return true
		case "":
			if len(fields) == 2 {
				return true
			}
		default:
			if len(fields) >= 3 && strings.HasPrefix(strings.ToLower(fields[2]), rs.flag) {
				return true
			}
		}
	}
	return false
}

// planReadonlyCommands 是明确只读命令 allowlist（首 token 精确匹配）。只放
// 纯只读、无写模式（或写模式已被 planWriteArgs 先行拦截）的命令。双面命令
// （sed/sort/awk/gofmt/tar）的写形态由 planWriteArgs 在 allowlist 之前拦截。
var planReadonlyCommands = map[string]bool{
	"ls": true, "dir": true, "cat": true, "type": true, "pwd": true,
	"echo": true, "printenv": true, "which": true, "whoami": true,
	"grep": true, "head": true, "tail": true, "wc": true, "sort": true,
	"uniq": true, "cut": true, "tr": true, "jq": true, "rg": true, "fd": true,
	"sed": true, "awk": true, "gofmt": true,
	"stat": true, "file": true, "du": true, "df": true, "realpath": true,
	"basename": true, "dirname": true, "ps": true, "date": true, "uname": true,
	"id": true, "hostname": true, "uptime": true,
	"get-content": true, "select-string": true, "get-childitem": true,
	"get-item": true, "get-location": true, "test-path": true,
	"measure-object": true,
	"less":           true, "more": true, "nl": true, "od": true, "xxd": true,
	"strings": true, "hexdump": true, "ldd": true, "nproc": true,
	"lsof": true, "sw_vers": true, "nm": true, "objdump": true, "yq": true,
	"bat": true, "exa": true, "delta": true,
	"sha256sum": true, "md5sum": true, "column": true, "paste": true,
	"join": true, "comm": true, "history": true,
	"git": true, /* 裸 git（无子命令）= 打印用法，只读 */
}

// --- 判定入口 -------------------------------------------------------------

// classifyPlanShell 判定 plan 模式下 shell 命令类别（纯函数）。
//
// 顺序：顶层写形态整串拦截 → 按 | && ; & 拆段 → 每段 classifyPlanShellSegment；
// 任一写段 → 整串 write；有未知段 → unknown（全部段看完再定）。
func classifyPlanShell(cmd string) planShellClass {
	if strings.TrimSpace(cmd) == "" {
		return planShellUnknown // 空命令无意义
	}
	// 顶层写形态拦截（整串级，拆段前）：换行多命令（ls\ntouch evil）、反引号/
	// $() 命令替换、写重定向（> 覆盖 >>、2>&1、&>）。这些形态出现在任意管道段
	// 都必须整串拒绝——拆段拆不掉，段内前缀匹配会误放行（缺陷 01 根因）。
	if strings.ContainsAny(cmd, "\n`") {
		return planShellWrite
	}
	if strings.Contains(cmd, "$(") {
		return planShellWrite
	}
	if strings.Contains(cmd, ">") {
		return planShellWrite
	}
	verdict := planShellReadonly
	for _, seg := range planSegmentSplit.Split(cmd, -1) {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue // || 等产生的空段
		}
		switch c := classifyPlanShellSegment(seg); c {
		case planShellWrite:
			return planShellWrite // 任一写段 → 整串写
		case planShellUnknown:
			verdict = planShellUnknown // 未知段先记下，全部段看完再定
		}
	}
	return verdict
}

// classifyPlanShellSegment 判定单个管道段的只读类别。顺序即安全边界：
// 只读豁免在前（解决假阴性），写黑名单在后（解决放行绕过）。
func classifyPlanShellSegment(seg string) planShellClass {
	fields := strings.Fields(seg)
	if len(fields) == 0 {
		return planShellUnknown
	}
	first := strings.ToLower(fields[0])

	// 1. 只读 flag 豁免（写命令带纯探查 flag：python --version、npm -v、
	//    docker --help、go help build）。
	if isPlanPureProbe(fields) {
		return planShellReadonly
	}

	// 2. 写子命令只读例外表（git status / go list / npm ls / make -n /
	//    git config --get 等）。
	if planReadonlySubMatches(fields) {
		return planShellReadonly
	}

	// 3. 四类写黑名单（首 token 精确匹配）。
	switch {
	case planWriteCommands[first]:
		return planShellWrite
	case planInterpreterCommands[first]:
		return planShellWrite
	case planPkgCommands[first]:
		return planShellWrite
	case planSysCommands[first]:
		return planShellWrite
	}

	// 4. 写子命令表（git/go/cargo 第二 token 是写子命令）。
	if subs := planWriteSubcommands[first]; subs != nil && len(fields) >= 2 && subs[strings.ToLower(fields[1])] {
		return planShellWrite
	}

	// 5. 写参数表（双面命令：sed -i / sort -o / find -delete / gofmt -w /
	//    awk system( / tar -x）。
	if flags := planWriteArgs[first]; flags != nil && anyPlanWriteArg(strings.Join(fields[1:], " "), flags) {
		return planShellWrite
	}

	// 6. 明确只读 allowlist（首 token 精确匹配）。
	if planReadonlyCommands[first] {
		return planShellReadonly
	}

	// 7. 未知 → unknown（纯 Deny 失败模式：Decide 归 Deny，理由提示换只读方式）。
	return planShellUnknown
}
