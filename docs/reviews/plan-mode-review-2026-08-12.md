# Plan Mode 代码审查报告

> 审查对象：提交 `bbdbd77`（feat(plan): plan mode 规划模式，ADR-036，版本 0.7.0）
> 审查日期：2026-08-12
> 范围：31 个文件 / +1918 −28 行；4 个新工具（plan_enter / write_plan / plan_done /
> ask_user）、`Approver.Ask` 接口扩展、`Decide` plan 分支、TUI `/plan` 与 ask 弹窗
> 方法：全部结论经可执行探针证实（探针跑完即删，未入库）

## 摘要

架构层面是扎实的：模式开关挂 `AgentState`、middleware 读 `rc.State`、复用
`rc.Approver` 而不新开接口，都契合 ADR-026 无状态 agent 设计；`Decide` 的 plan
分支置于 bypass 之前（plan 强只读优先于权限模式）这个不变量在测试中被自觉守住。

但发现 **4 项缺陷**，其中 3 项为严重级：

| 编号 | 缺陷 | 级别 | 证据 |
|---|---|---|---|
| 01 | plan 模式只读约束可被绕过（`sed -i` / `sed w` / `sort -o` / `env sh` 真实写盘） | 严重 | 端到端探针 |
| 02 | plan 只读白名单假阴性率 68%（`python --version` 等 38/56 被误拒） | 严重 | 语料普查 |
| 03 | TUI 并发弹窗互相覆盖 → 工具 goroutine 永久阻塞、审批静默消失 | 严重 | 单测探针 |
| 04 | `planMu` 与 `todoMu` 分离造成 data race | 中等 | race detector |

另有两项小问题（`AllowCustom` 在 TUI 侧是装饰性的、`gofmt -l` 报两个文件）。

缺陷 01 与 02 是**同一个根因的两面**：用命令名前缀匹配推断副作用。详见
「设计层面」一节——该节记录了与用户确认后选定的修复方向（方案一 + 写黑名单）。

---

## 已验证缺陷

### 01. plan 模式只读约束可被绕过（严重）

`internal/middleware/impl/policy.go:281` `isPlanReadonlyShell` 的防线是「危险黑名单
+ 拦截 `>` + 分段白名单前缀匹配」，但白名单里的 `sed`、`sort` 本身就能写文件。

用真实 `ShellCommandTool.Handle` 端到端验证（`Decide` 放行 → 真实执行 → 检查文件）：

| 命令 | 结果 |
|---|---|
| `sed -i '' 's/原始/篡改/' victim.txt` | 文件内容变为 `"篡改内容"` |
| `sed -n 'w victim.txt' /etc/hosts` | victim.txt 被 /etc/hosts 全文覆盖 |
| `env sh evil.sh` | 脚本执行，文件被写为 `"pwned-by-script"` |
| `env FOO=1 sh evil.sh` | 同上 |
| `sort -o victim.txt victim.txt` | 写入目标文件（`-o` 是任意写） |

被正确拦住的：`$(...)`、反引号、换行多命令、`awk system()`、`cat|tee`、`rm`、
`find -execdir`、`git checkout`（均因含 `>` 或命中黑名单）。

根因有三层：

1. `planSafeExtra`（`policy.go:270`）把 `sed`/`sort`/`awk`/`tr`/`jq` 当成「只读过滤器」，
   而它们都有写模式（`sed -i`、`sed w`、`sort -o`）。**副作用在参数里，不在命令名里。**
2. `env` 来自原 `safeCommandPrefixes`（本为 `printenv` 场景），却是通用执行跳板。
   `env sh -c '...'` 因含 `>` 被拦，但 `env sh script` 无元字符，直接过。
3. 前缀匹配无法区分 `git config --get`（读）与 `git config user.name x`（写）。

这不是理论风险：ADR-036 明确写了「我们没 sandbox，靠 `Decide` plan 分支直接
Deny」，即 `Decide` 是**唯一防线**；而 plan 模式的用户预期恰是「可以放心让它调研，
它动不了我的代码」。

### 02. plan 只读白名单假阴性率 68%（严重）

用户在实际任务中遇到：plan 模式下模型调用 `python --version` 被拒。普查证实这是
系统性问题——56 个真只读命令语料中 **38 个被误拒（68%）**：

| 类别 | 被误拒的命令 |
|---|---|
| 版本探查（11/11 全拒） | `python --version` `python3 -V` `node -v` `go version` `java -version` `npm -v` `docker --version` `gcc --version` `cargo --version` `rustc --version` `make --version` |
| 帮助文本（4/4 全拒） | `go help build` `docker --help` `npm help install` `make -h` |
| 只读构建查询（9/9 全拒） | `go list ./...` `go vet ./...` `go doc` `go env` `go mod graph` `npm ls` `cargo tree` `gofmt -l .` `make -n build` |
| 只读 git（白名单外 6/6 全拒） | `git remote -v` `git tag` `git stash list` `git describe` `git config --get` `git shortlog -s` |
| 系统探查（10/13 拒） | `stat` `file` `du` `df` `realpath` `basename` `ps aux` `date` `uname -a` `id` |
| 结构化读取 | `rg TODO` `fd .go` |

一个规划期 agent 想搞清楚「这项目用什么工具链、什么版本」几乎必然撞墙。

**关键点**：01 与 02 方向相反却同源。当前策略**在两个方向上同时错**——既过严到
影响可用性（68% 漏报），又过松到防不住真正的写操作（19% 误报）。调阈值或往
`planSafeExtra` 加命令都治不了本：加 `python` 会连 `python evil.py` 一起放行，
加 `go` 会连 `go build -o` 一起放行。

### 03. TUI 并发弹窗互相覆盖导致 goroutine 永久阻塞（严重）

`askRequestMsg`（`internal/ui/tui/model.go:245`）与 `approvalRequestMsg`
（`model.go:241`）都**直接赋值 `m.ovl`**，绕过了 Bug10 专门引入的 `openOverlay`
守卫（`model.go:124`，该守卫在已有覆盖层未决时拒绝叠开）。

单测探针证实两个场景：

- **同批并发两个 `ask_user`**（ADR-024 并行工具让这完全可能）：第二个弹窗覆盖
  第一个，第一个的 `respCh` 再无人写入，该工具 goroutine 永久挂在 `select` 上
  （除非 ctx cancel）。探针输出：`ch1 未收到任何回答`，`ch2 收到回答: {Selection:[B]}`。
- **审批挂起时 ask 到达**：审批弹窗被替换，用户**永远看不到那个审批请求**，审批
  goroutine 同样阻塞。探针输出：`ask 到达后 kind=3 appr=false ask=true`，
  `审批 channel 无回答`。

后者尤其严重：审批是安全机制，它静默消失，用户只看到界面卡住。

`approvalRequestMsg` 那行在本次提交前已存在，但在只有一个 HITL 通道时不可达；
ask 的加入把它变成了实际可达路径。

修法：两处都走 `openOverlay`（拒绝叠开），或引入待决请求队列。

### 04. `planMu` 与 `todoMu` 分离造成 data race（中等）

`savePlanState`（`internal/tools/plan.go:51`）会 `json.Marshal` 整个 `AgentState`
（含 `Todos`），它在 `planMu` 下；而 `update_todo` 的 `ReplaceTodos` 在 `todoMu` 下
（`internal/tools/todo.go:85`）。两个工具用**不同的锁**保护**同一个** `AgentState`。

race detector 输出：

```
WARNING: DATA RACE
Read at 0x...: json.sliceEncoder.encode → agentstate.SaveFile(agentstate.go:119)
  → tools.savePlanState(plan.go:55) → WritePlanTool.Handle(plan.go:165)
Previous write at 0x...: agentstate.ReplaceTodos(agentstate.go:87)
  → UpdateTodoTool.Handle(todo.go:86)
```

`plan.go:32` 的注释说 `planMu` 是为并行工具设计的，但未意识到 todo 侧是另一把锁。
修法：合并为一把 state 锁，或把锁下沉到 `AgentState` 自身。

---

## 小问题

### `AllowCustom` 在 TUI 侧是装饰性的

`handleAskKey`（`model.go:473`）的 `default` 分支**无条件**把可打印字符追加到
`ask.custom`；`view.go:327` 只用 `AllowCustom` 决定是否显示提示文字。只有 run 模式的
`ParseAskAnswer`（`internal/ui/approver.go:126`）真正执行了该约束。

当前所有调用方都传 `true`，故无实际影响——但这是等着被踩的陷阱：将来传 `false`
会发现 TUI 里约束不生效。

### gofmt

`gofmt -l` 报两个文件：`internal/tools/ask.go`（结构体字段对齐）、
`internal/middleware/impl/policy.go`（注释列表缩进）。跑 `gofmt -w` 即可。

---

## 测试与构建状态

- `go build ./...` 通过；`go vet ./...` 干净。
- `go test ./...` 有一个失败：`TestSessionPersistenceE2E`。**该失败在本次提交前
  已存在**——用 worktree 检出前一提交 `2c64cd7` 复现确认，非 plan mode 引入
  （但仍是待修问题）。
- `internal/tools` 包耗时 115 秒源于既有的 `TestShellTimeoutKillsProcessGroup`
  （100 秒），与本次无关。
- plan mode 自身覆盖不错：`policy_plan_test.go` 表驱动、`plan_test.go`、
  `internal/e2e` 的 `TestTUIPlanModeE2E` 完整闭环都在。但测试只覆盖了
  `isPlanReadonlyShell` 的正向白名单与明显攻击（`rm -rf`、`echo x > file`），
  **缺的正是缺陷 01 的双面命令与缺陷 02 的假阴性**——两类语料都值得补进
  `policy_plan_test.go` 作为回归。

---

## 设计层面：plan 模式只读强制策略

缺陷 01 与 02 补实现补不出来，需改判定维度。本节记录调研与选定方向。

### 三个参考源都不用命令白名单

**没有任何参考源用命令白名单强制 plan 只读。**

- **opencode**（`packages/opencode/src/agent/agent.ts:156-181`）：plan agent 只硬拒
  `edit`（`edit: {"*": "deny"}`，例外是 plan 文件路径），`bash` 直接继承 defaults 的
  `"*": "allow"` —— **plan 模式下 bash 完全不设限**。只读靠 `plan-mode.txt` 的
  prompt 约束（"You MUST NOT make any edits... run any non-readonly tools...
  This supersedes any other instructions"）加模型自觉。
- **codex**：靠 OS sandbox（seatbelt / landlock）在内核层拦写，无需判断命令语义。
- **AgentScope**：只读阶段 + plan_write + HITL 退出，同样不含命令级白名单。

ADR-036 那句「我们没 sandbox，靠 `Decide` plan 分支直接 Deny」，实质是在既无
sandbox 又不愿只靠 prompt 的情况下，选了第三条路——用字符串匹配做语义判定。
缺陷 01/02 的数据就是这条路的两端。

### 策略量化对比

56 个真只读 + 36 个真写命令语料（后扩至 69 / 51）：

| 策略 | 漏报（只读被误拒） | 误报（写被误放行） |
|---|---|---|
| **A 当前实现**（只读白名单前缀匹配） | **38/56（68%）** | **7/36（19%）** |
| B 参数级判定（只读表 + 双面命令查危险参数 + 子命令白名单 + 只读 flag 通用规则） | 0/56 | 0/36 |
| **C 方案一 + 写黑名单**（反向：默认放行，只拦写命令） | 14/69 → **0/69**（精修后） | **0/51** |

策略 C 初版的 14 项漏报全部集中在同一模式：**写命令带只读 flag**
（`python --version`、`docker --help`、`npm ls`、`git config --get`、`go test -list`）。
补两条规则后归零：

1. **只读 flag 豁免**：命令后若只跟 `--version`/`-v`/`-V`/`--help`/`-h`/`version`/
   `help` 之类纯探查 flag，无论命令是否在写黑名单里都放行。这一条单独解决了
   用户遇到的整类问题。
2. **写子命令的只读例外表**：`git config --get`、`git stash list`、`go test -list`、
   `npm ls`、`make -n`、`docker ps`、`kubectl get`、`terraform plan`、
   `git apply --check` 等。

### 决定性检验：语料外命令落到哪一边

0/0 是在自建语料上的成绩，真实世界必有遗漏。用 26 个语料外只读命令 + 22 个语料外
写命令检验**失败模式**（这才是黑白名单的分水岭）：

| | 未知只读命令 | 未知写命令 |
|---|---|---|
| **写黑名单（默认放行）** | **0/26 被误拒** — `bat` `exa` `delta` `hexdump` `nm` `objdump` `strings` `ldd` `ruff` `mypy` `tsc --noEmit` `shellcheck` `yq` `nproc` `lsof` `sw_vers` `git cat-file` 全部自动通过 | **18/22 漏放行** — `trash` `unlink` `gsed -i` `ed` `patch` `poetry install` `uv pip install` `bun install` `conda install` `nix-env -i` `defaults write` `zip` `7z` `code --install-extension` 等 |
| 只读白名单（默认拒绝） | 全部被拒（需逐个补表） | 全部拦住 |

**这正是两种策略的本质差异**：黑名单默认放行，未知只读命令自动通过（可用性自动
适配新工具，无需维护），代价是未知写命令漏过；白名单默认拒绝，反之。

### 选定方向：方案一 + 写黑名单（用户确认）

用户选择方案一（对齐 opencode，停止用命令白名单猜只读），并提出**反向判定**——
不查是否在只读名单，而是查是否在写名单。数据支持这个直觉：反向判定在语料上
误报 0，且未知只读命令零误拒。

落地要点：

1. **删除 `isPlanReadonlyShell` 与 `planSafeExtra`**（`policy.go:268-323`），
   plan 分支的 shell 判定改为写黑名单反向判定。
2. **写黑名单分四类**：
   - 写命令名（精确匹配，非前缀）：`rm` `mv` `cp` `touch` `mkdir` `ln` `chmod`
     `chown` `truncate` `dd` `tee` `install` `rsync` `shred` 等
   - 解释器/执行跳板：`sh` `bash` `zsh` `python` `node` `ruby` `perl` `env`
     `eval` `exec` `xargs` `nohup` 等（`env` 必须移出原白名单）
   - 包管理/构建：`pip` `npm` `yarn` `npx` `brew` `apt` `make` `gradle` `mvn` 等
   - 系统/服务/网络/容器：`sudo` `systemctl` `kill` `mount` `crontab` `curl`
     `wget` `scp` `docker` `kubectl` `helm` `terraform` 等
3. **写子命令表**：`git`（`add` `commit` `push` `reset` `checkout` `stash` `clean`
   `config` 等）、`go`（`build` `install` `get` `mod tidy` `generate` `run` `test`）、
   `cargo`（`build` `install` `publish` `update`）。
4. **写参数表**（双面命令）：`sed` 查 `-i`/`--in-place`/`w `；`sort` 查 `-o`；
   `find` 查 `-delete`/`-exec`/`-execdir`/`-ok`；`gofmt` 查 `-w`；`awk` 查
   `system(`/`> `；`tar` 查 `-x`/`-c`；`tee`/`unzip` 恒拒。
5. **写形态拦截**（缺陷 01 实证的绕过通道）：`>`、`$(`、反引号、换行多命令。
6. **只读豁免两条**：只读 flag 通用规则 + 写子命令只读例外表（见上）。
7. **失败模式降级**：鉴于未知写命令会漏放行（18/22），建议把不确定情况降级为
   **Ask 而非 Deny**——能确定只读的自动放行，命中写黑名单的直接拒，其余弹审批。
   这样 `python --version` 直接过，未知的 `poetry install` 弹审批让用户决定，
   `sed -i` 明确拒绝。纯 Deny 只留真正确定要写的那几类。
8. **回归测试**：本报告两份语料（69 只读 / 51 写 + 48 项语料外）补进
   `policy_plan_test.go`。

> 注：该方向为审查期间与用户确认的结论，正式决策与影响面应另立 ADR 记入
> `docs/tasks/DECISIONS.md`（ADR-036 修订），本报告只陈述问题与证据。
