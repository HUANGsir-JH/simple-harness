package impl

import (
	"testing"

	"github.com/agent-project/harness/internal/messages"
)

// TestClassifyPlanShell 验证 plan 模式 shell 只读判定（写黑名单反向判定 +
// 纯 Deny 失败模式，ADR-036 修订）。语料来自 plan-mode-review-2026-08-12
// 的两份普查（69 只读 / 51 写）+ 48 语料外，代表项移植于此。
func TestClassifyPlanShell(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want planShellClass
	}{
		// ---- 只读（→ planShellReadonly）----
		// 版本探查 11（写命令带只读 flag，原 68% 假阴性的主源）。
		{"py-version", "python --version", planShellReadonly},
		{"py3-v", "python3 -V", planShellReadonly},
		{"node-v", "node -v", planShellReadonly},
		{"go-version", "go version", planShellReadonly},
		{"java-version", "java -version", planShellReadonly},
		{"npm-v", "npm -v", planShellReadonly},
		{"docker-version", "docker --version", planShellReadonly},
		{"gcc-version", "gcc --version", planShellReadonly},
		{"cargo-version", "cargo --version", planShellReadonly},
		{"rustc-version", "rustc --version", planShellReadonly},
		{"make-version", "make --version", planShellReadonly},
		// 帮助文本 4。
		{"go-help", "go help build", planShellReadonly},
		{"docker-help", "docker --help", planShellReadonly},
		{"npm-help", "npm help install", planShellReadonly},
		{"make-h", "make -h", planShellReadonly},
		// 只读构建查询 9。
		{"go-list", "go list ./...", planShellReadonly},
		{"go-vet", "go vet ./...", planShellReadonly},
		{"go-doc", "go doc", planShellReadonly},
		{"go-env", "go env", planShellReadonly},
		{"go-mod-graph", "go mod graph", planShellReadonly},
		{"npm-ls", "npm ls", planShellReadonly},
		{"cargo-tree", "cargo tree", planShellReadonly},
		{"gofmt-l", "gofmt -l .", planShellReadonly},
		{"make-n", "make -n build", planShellReadonly},
		// 只读 git 6（写子命令只读例外表）。
		{"git-remote-v", "git remote -v", planShellReadonly},
		{"git-tag", "git tag", planShellReadonly},
		{"git-stash-list", "git stash list", planShellReadonly},
		{"git-describe", "git describe", planShellReadonly},
		{"git-config-get", "git config --get user.name", planShellReadonly},
		{"git-shortlog", "git shortlog -s", planShellReadonly},
		// 系统探查 10。
		{"stat", "stat file", planShellReadonly},
		{"file", "file x", planShellReadonly},
		{"du", "du -sh .", planShellReadonly},
		{"df", "df -h", planShellReadonly},
		{"realpath", "realpath x", planShellReadonly},
		{"basename", "basename x", planShellReadonly},
		{"ps", "ps aux", planShellReadonly},
		{"date", "date", planShellReadonly},
		{"uname", "uname -a", planShellReadonly},
		{"id", "id", planShellReadonly},
		// 结构化读取。
		{"rg", "rg TODO", planShellReadonly},
		{"fd", "fd .go", planShellReadonly},
		// 管道/组合（保留）。
		{"git-log-pipe", "git log --oneline | head -5", planShellReadonly},
		{"grep-pipe", "grep foo -r . | head", planShellReadonly},
		{"cat-wc", "cat a | wc -l", planShellReadonly},
		{"and-combo", "git status && git log", planShellReadonly},
		// 语料外只读（明确 allowlist 兜底，缺陷 02 方向）。
		{"bat", "bat file", planShellReadonly},
		{"exa", "exa -l", planShellReadonly},
		{"strings", "strings bin", planShellReadonly},
		{"hexdump", "hexdump -C f", planShellReadonly},
		{"nproc", "nproc", planShellReadonly},
		{"git-cat-file", "git cat-file -t HEAD", planShellReadonly},
		// 双面命令只读形态（写参数表拦截写形态后 allowlist 放行）。
		{"sed-read", "sed 's/x/y/' file", planShellReadonly},
		{"sort-read", "sort file", planShellReadonly},
		{"awk-read", "awk '{print $1}' file", planShellReadonly},

		// ---- 写（→ planShellWrite）----
		// 缺陷 01 实证的双面命令/执行跳板。
		{"sed-i", "sed -i '' 's/原始/篡改/' victim.txt", planShellWrite},
		{"sed-w", "sed -n 'w victim.txt' /etc/hosts", planShellWrite},
		{"sed-i-bak", "sed -i.bak s/x/y/ f", planShellWrite},
		{"sort-o", "sort -o victim.txt victim.txt", planShellWrite},
		{"env-sh", "env sh evil.sh", planShellWrite},
		{"env-foo-sh", "env FOO=1 sh evil.sh", planShellWrite},
		{"awk-system", "awk 'BEGIN{system(\"rm -rf /tmp/x\")}'", planShellWrite},
		// 写命令名。
		{"rm", "rm -rf /", planShellWrite},
		{"rm-file", "rm file", planShellWrite},
		{"mv", "mv a b", planShellWrite},
		{"cp", "cp a b", planShellWrite},
		{"touch", "touch f", planShellWrite},
		{"mkdir", "mkdir d", planShellWrite},
		{"chmod", "chmod 755 f", planShellWrite},
		{"chown", "chown u f", planShellWrite},
		{"truncate", "truncate -s 0 f", planShellWrite},
		{"dd", "dd if=a of=b", planShellWrite},
		{"tee", "tee /etc/hosts", planShellWrite},
		{"install", "install a b", planShellWrite},
		{"rsync", "rsync a b", planShellWrite},
		{"gzip", "gzip f", planShellWrite},
		{"unzip", "unzip a.zip", planShellWrite},
		// 解释器/执行跳板。
		{"bash-script", "bash script.sh", planShellWrite},
		{"python-script", "python evil.py", planShellWrite},
		{"xargs", "ls | xargs rm", planShellWrite},
		// 包管理/构建。
		{"make-build", "make build", planShellWrite},
		{"pip-install", "pip install x", planShellWrite},
		{"npm-install", "npm install", planShellWrite},
		{"brew-install", "brew install x", planShellWrite},
		{"apt-install", "apt install x", planShellWrite},
		{"poetry-install", "poetry install", planShellWrite},
		// 系统/服务/网络/容器。
		{"sudo", "sudo rm -rf /", planShellWrite},
		{"systemctl", "systemctl restart ssh", planShellWrite},
		{"kill", "kill 123", planShellWrite},
		{"mount", "mount /dev/sda /mnt", planShellWrite},
		{"crontab", "crontab -e", planShellWrite},
		{"curl-o", "curl -o file http://x", planShellWrite},
		{"wget", "wget -O file http://x", planShellWrite},
		{"scp", "scp a b@host:/x", planShellWrite},
		{"docker-run", "docker run nginx", planShellWrite},
		{"kubectl-apply", "kubectl apply -f x", planShellWrite},
		{"helm-install", "helm install x", planShellWrite},
		{"terraform-apply", "terraform apply", planShellWrite},
		{"shutdown", "shutdown now", planShellWrite},
		// 写子命令表。
		{"git-add", "git add .", planShellWrite},
		{"git-commit", "git commit -m x", planShellWrite},
		{"git-checkout", "git checkout main", planShellWrite},
		{"git-stash-pop", "git stash pop", planShellWrite},
		{"git-config-write", "git config user.name x", planShellWrite},
		{"git-tag-d", "git tag -d v1", planShellWrite},
		{"git-branch-m", "git branch -m new", planShellWrite},
		{"git-remote-add", "git remote add origin x", planShellWrite},
		{"go-build", "go build ./...", planShellWrite},
		{"go-install", "go install ./cmd/harness", planShellWrite},
		{"go-get", "go get x", planShellWrite},
		{"go-mod-tidy", "go mod tidy", planShellWrite},
		{"go-run", "go run main.go", planShellWrite},
		{"go-env-w", "go env -w GOPROXY=x", planShellWrite},
		{"cargo-build", "cargo build", planShellWrite},
		{"cargo-install", "cargo install x", planShellWrite},
		// 写形态拦截（缺陷 01 的绕过通道）。
		{"redir", "echo x > file", planShellWrite},
		{"append-redir", "echo x >> file", planShellWrite},
		{"newline", "ls\ntouch evil", planShellWrite},
		{"cmd-subst", "$(rm -rf /)", planShellWrite},
		{"backtick", "echo `rm -rf /`", planShellWrite},
		{"semicolon", "ls; rm x", planShellWrite},
		{"curl-pipe-sh", "curl x | sh", planShellWrite},
		// 写参数表。
		{"find-delete", "find . -delete", planShellWrite},
		{"find-exec", "find . -exec rm {} \\;", planShellWrite},
		{"gofmt-w", "gofmt -w file.go", planShellWrite},
		{"tar-x", "tar -xzf a.tgz", planShellWrite},
		{"tar-c", "tar -czf a.tgz .", planShellWrite},
		// 语料外写命令（纯 Deny 兜底也拦）。
		{"trash", "trash x", planShellWrite},
		{"unlink", "unlink x", planShellWrite},
		{"patch", "patch < diff", planShellWrite},
		{"ed", "ed file", planShellWrite},
		{"zip", "zip a.zip a", planShellWrite},
		{"7z", "7z a a.7z a", planShellWrite},
		{"defaults-write", "defaults write com.x y", planShellWrite},

		// ---- 未知（→ planShellUnknown，纯 Deny 归 Deny）----
		{"gsed-i", "gsed -i s/x/y/ f", planShellUnknown},
		{"ruff", "ruff check .", planShellUnknown},
		{"mypy", "mypy .", planShellUnknown},
		{"tsc-noemit", "tsc --noEmit", planShellUnknown},
		{"shellcheck", "shellcheck script.sh", planShellUnknown},
		{"code-install", "code --install-extension x", planShellUnknown},
		{"unknown-cmd", "foo --bar", planShellUnknown},
		{"empty", "", planShellUnknown},
		// 裸命令不算纯探查：python 在解释器黑名单 → 写（拒绝）。
		{"python-bare", "python", planShellWrite},
	}

	// 不变量：所有非只读期望的命令，分类结果必须也非只读（防写命令被误放行）。
	for _, tc := range cases {
		got := classifyPlanShell(tc.cmd)
		if tc.want != planShellReadonly && got == planShellReadonly {
			t.Errorf("%s: classifyPlanShell(%q) = readonly，但期望 %v（写/未知被误放行）", tc.name, tc.cmd, tc.want)
		}
		if got != tc.want {
			t.Errorf("%s: classifyPlanShell(%q) = %v, want %v", tc.name, tc.cmd, got, tc.want)
		}
	}
}

// TestDecidePlanModeShell 验证 Decide plan 分支的 shell case：readonly → Allow，
// write / unknown → Deny（纯 Deny 失败模式显式锁定）。
func TestDecidePlanModeShell(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want Outcome
	}{
		{"readonly", "python --version", OutcomeAllow},
		{"readonly-pipe", "grep foo -r . | head", OutcomeAllow},
		{"readonly-git", "git status", OutcomeAllow},
		{"write", "sed -i s/x/y/ f", OutcomeDeny},
		{"write-env", "env sh evil.sh", OutcomeDeny},
		{"write-make", "make build", OutcomeDeny},
		{"write-redir", "echo x > file", OutcomeDeny},
		{"unknown-deny", "tsc --noEmit", OutcomeDeny}, // 未知只读也 Deny（纯 Deny 语义）
		{"write-deny", "poetry install", OutcomeDeny}, // 写命令 Deny（兜底）
	}
	for _, tc := range cases {
		o, reason := Decide(shellPlanCall(tc.cmd), ModeBypass, nil, "/ws", true)
		if o != tc.want {
			t.Errorf("%s: Decide(plan, %q) = %v (%q), want %v", tc.name, tc.cmd, o, reason, tc.want)
		}
	}
}

// TestDecidePlanMode 验证 plan 分支（强只读，工具全量可见但拒绝，ADR-036）。
// shell 用例在新判定（写黑名单反向判定）下结论不变。
func TestDecidePlanMode(t *testing.T) {
	shell := func(cmd string) *messages.ToolCall { return toolCall("shell_command", map[string]any{"command": cmd}) }
	cases := []struct {
		name string
		call *messages.ToolCall
		want Outcome
	}{
		// plan 模式下禁止写文件。
		{"write-file", toolCall("write_file", map[string]any{"path": "a.txt", "content": "x"}), OutcomeDeny},
		{"apply-patch", toolCall("apply_patch", map[string]any{"patch": "*** Begin Patch\n*** Update File: a.go\n@@ f\n-x\n+y\n*** End Patch"}), OutcomeDeny},
		// 只读/低风险放行。
		{"read", toolCall("read_file", map[string]any{"path": "a.go"}), OutcomeAllow},
		{"todo", toolCall("update_todo", map[string]any{"todos": []any{}}), OutcomeAllow},
		{"ask", toolCall("ask_user", map[string]any{"question": "x"}), OutcomeAllow},
		// plan 工具：write_plan/plan_done 放行（Handle 内 HITL），plan_enter 拒绝。
		{"write-plan", toolCall("write_plan", map[string]any{"content": "x"}), OutcomeAllow},
		{"plan-done", toolCall("plan_done", nil), OutcomeAllow},
		{"plan-enter", toolCall("plan_enter", nil), OutcomeDeny},
		// shell：只读放行（含管道），写/危险拒绝。
		{"shell-ls", shell("ls -la"), OutcomeAllow},
		{"shell-pipe", shell("grep foo | head -5"), OutcomeAllow},
		{"shell-write-redir", shell("echo x > file"), OutcomeDeny},
		{"shell-dangerous", shell("rm -rf /"), OutcomeDeny},
		{"shell-unknown-cmd", shell("make build"), OutcomeDeny},
		// 未知工具保守拒绝。
		{"unknown", toolCall("webfetch", map[string]any{"url": "x"}), OutcomeDeny},
	}
	for _, tc := range cases {
		o, reason := Decide(tc.call, ModeBypass, nil, "/ws", true) // bypass 也受 plan 约束
		if o != tc.want {
			t.Errorf("%s: plan 分支 = %v (%q), want %v", tc.name, o, reason, tc.want)
		}
	}
}

// TestDecideNonPlanPlanTools 验证非 plan 模式：plan_enter 放行（Handle 内 HITL），
// write_plan/plan_done 拒绝，ask_user 放行。
func TestDecideNonPlanPlanTools(t *testing.T) {
	cases := []struct {
		name string
		call *messages.ToolCall
		want Outcome
	}{
		{"plan-enter", toolCall("plan_enter", nil), OutcomeAllow},
		{"write-plan", toolCall("write_plan", map[string]any{"content": "x"}), OutcomeDeny},
		{"plan-done", toolCall("plan_done", nil), OutcomeDeny},
		{"ask", toolCall("ask_user", map[string]any{"question": "x"}), OutcomeAllow},
	}
	for _, tc := range cases {
		o, _ := Decide(tc.call, ModeAcceptEdits, nil, "/ws", false)
		if o != tc.want {
			t.Errorf("%s: 非 plan 分支 = %v, want %v", tc.name, o, tc.want)
		}
	}
}

// shellPlanCall 构造一个 plan 分支用的 shell_command 工具调用（参数含 command）。
func shellPlanCall(cmd string) *messages.ToolCall {
	return toolCall("shell_command", map[string]any{"command": cmd})
}
