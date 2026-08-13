# shell 进程树生命周期 + 系统提示通道重构代码审查报告

> 审查对象：`a83d8e5`（用量/压缩审查修复，ADR-037 勘误）及其后的全部提交：
> `24eb196`（进程树生命周期，ADR-038）、`513b543`（超时转后台托管，ADR-038 勘误）、
> `da3448b`（系统提示通道重构，ADR-039）、`661a303`（移除 shell 工具内截断，ADR-028 勘误）
> 审查日期：2026-08-13
> 范围：52 文件，+1786 / -350 行；tools 包进程树管理（background*.go / shell.go）、
> middleware 系统提示管道（rc.SystemPrompt / BaseInstructionsMiddleware）、
> compact 判定实时化、provider signature_delta、AgentState 用量覆盖语义
> 方法：结论经可执行探针与**对照实验**证实（探针跑完即删，未入库）；
> `go build ./...` 与 `GOOS=windows/linux` 交叉编译全绿；核心包
> （agent / middleware / middleware-impl / compact / provider / ui / ui-tui）
> 测试全绿，重点路径 `-race` 全绿
> 审查平台：**darwin (macOS)** —— 与本批功能的开发/验证平台（Windows）不同，
> 这一差异本身暴露了下述 03/04 两项

## 摘要

这批提交的设计质量是高的：ADR-038 用 `context.AfterFunc` 把杀树时机提到 ctx 取消
**瞬间**，绕开了"`cmd.Run()` 因孙进程继承管道句柄永不返回 → 杀树分支执行不到"的真实
死锁；超时转后台托管（前台输出写临时日志文件、rename 无缝续写、句柄移交注册表）是
超出 opencode/codex 参考源的自创语义，且把 `f.Close()` 放进 Wait goroutine 而非 defer
这个关键细节做对了。ADR-039 的"内容通道分类原则"（对话历史 = Messages / 稳定配置 =
系统提示 / 工具定义 = toolspec / 即时信号 = 临时消息副本）把 `Agent.instructions`
彻底移出 agent，为子 agent 换链换提示词铺好了路。`661a303` 消除双重截断修正了
ADR-028 内部两条款的自相矛盾。注释密度与"为什么这样做"的记录水平在同类项目中少见。

但发现 **1 项严重缺陷**，另有 3 项应处理问题：

| 编号 | 缺陷 | 级别 | 证据 |
|---|---|---|---|
| 01 | `context.AfterFunc` 返回的 `stop` 被丢弃 → `defer cancel()` 在**每条前台命令正常返回时**触发杀树，连带杀掉命令自己派生的后台进程（`npm run dev &` 起的服务命令一退出就被杀）。ADR-038 决策第 2 点已写明"成功路径 `stop()` 防 PID 复用误杀"，实现漏了 | 严重 | **对照实验**（plain exec 孙进程存活 vs 经工具后被杀） |
| 02 | background 进程自然退出后注册表条目不注销 → POSIX PID 复用后，`kill_pid` 可杀掉无关进程（"仅杀注册表内 PID"这条安全边界失效） | 中等 | 探针（自然退出后注册表残留 1 条） |
| 03 | POSIX 判活用 `kill -0`，把**僵尸态**当存活 → macOS 上 3 个测试假失败，掩盖真实回归的能力 | 中等（测试缺陷） | 探针（kill 后立刻判活 true，400ms 后 false） |
| 04 | `TestShellTimeoutKillsProcessGroup` 仍断言"超时杀进程组"，与 ADR-038 勘误的新语义（超时不杀、转后台）直接矛盾——Windows 侧测试已按勘误反转，POSIX 侧漏了 | 中等（测试语义过期） | 实测失败 + 勘误原文对照 |
| 05 | `cmd.Start()` 失败时 `startForeground` 已 close 句柄却仍返回它，调用方错误分支再 close 一次（Windows 双重 `CloseHandle`） | 低 | 探针（坏 workdir 确认路径可达） |

---

## 已验证缺陷

### 01. 成功路径误杀命令派生的后台进程（严重）

`internal/tools/background.go:72` 注册的取消回调**从未被取消**：

```go
context.AfterFunc(ctx, func() {
    if ctx.Err() == context.Canceled {
        killProcessTree(tree, pid)
    }
})
return tree, nil   // ← AfterFunc 返回的 stop func() bool 被丢弃
```

而 `internal/tools/shell.go:92` 的 `defer cancel()` 在**每条**前台命令返回时执行。
`cancel()` 使 `ctx.Err()` 成为 `context.Canceled`——正是回调里的放行条件——于是
命令正常完成后杀树照样发生。POSIX 上 `killProcessTree` 是 `kill(-pid, SIGKILL)`，
杀的是整个进程组，命令自己派生的后台进程与 `sh` 同组，一并被杀。

`ctx.Err() == context.Canceled` 这个判断的本意是"只杀 Esc、不杀超时"，它确实区分开了
`DeadlineExceeded`，但区分不出 `cancel()` 的两个来源：用户 Esc 与 `defer cancel()`
正常收尾在 `ctx.Err()` 上完全一致。

**对照实验**（同一命令 `nohup sleep 60 & echo $! > pidfile; sleep 0.1; echo done`）：

| 组 | 执行方式 | 命令退出后孙进程 |
|---|---|---|
| 对照 | `exec.Command("sh","-c",…)` + `Setpgid`（无 harness 杀树逻辑） | 存活 ✅ |
| 实验 | `ShellCommandTool{}.Handle(...)` 正常成功返回 | **被杀** ❌ |

对照组存活证明孙进程本身不会自行退出，实验组的死亡确由 `AfterFunc` 造成。

用户可见症状：模型执行 `npm run dev &`、`nohup ./server &` 一类命令，工具报成功，
但服务在命令返回瞬间就没了——表现为"起了又没了"，且日志里找不到失败原因（进程收到
SIGKILL，无任何输出）。这与 ADR-038 想解决的痛点（前台起服务卡死会话）相邻，用户在
被引导用 `background: true` 之前很可能先撞上这个。

**关键判断**：ADR-038 决策第 2 点原文写了「成功路径 `stop()` 防 PID 复用误杀」。
所以这不是设计缺陷，而是**实现漏掉了已定案的设计**。修复即照 ADR 执行：

```go
func startForeground(...) (processTreeHandle, func() bool, error) {
    ...
    stop := context.AfterFunc(ctx, func() { ... })
    return tree, stop, nil
}
```

Handle 在**完成分支**与**超时转后台分支**调用 `stop()`（这两条路径都不该杀树），
Esc 分支不调用。顺带也关闭了 ADR 提到的 PID 复用误杀窗口——回调若在进程退出后
才触发，`pid` 可能已被系统复用给无关进程。

### 02. background 进程自然退出后注册表条目残留（中等）

`internal/tools/background.go:135`：

```go
go func() { _ = cmd.Wait() }()   // 回收进程资源，但未 unregisterBackground(pid)
```

探针：`background: true` 跑 `true`（立即退出），700ms 后进程已死，
`backgroundProcesses` 仍残留该 PID 条目。

ADR-038 把这个记为"开销可忽略"的已知边界。就内存占用而言确实如此，但它有一个
**超出"开销"的安全含意**：`handleKill` 的安全边界是"仅允许杀注册表内 PID——不向模型
开放任意 PID 击杀（防误杀系统进程）"。POSIX 上 PID 会复用，残留条目意味着某个已死
PID 被系统复用给无关进程后，模型的 `kill_pid` 会**通过**这道检查，然后
`kill(-pid, SIGKILL)` 掉那个无关进程组。

Windows 侧因 job 句柄精确定位无此风险（ADR 已正确指出），但 POSIX 侧这条边界目前
名不副实。修复很小——在 Wait goroutine 里补 `unregisterBackground(pid)`：

```go
go func() { _ = cmd.Wait(); unregisterBackground(pid) }()
```

同时也顺手消除了 ADR 记录的"注册表条目残留至 kill/退出清理"这条边界。

### 03. POSIX 判活把僵尸态当存活，掩盖回归（中等，测试缺陷）

`internal/tools/background_unix_test.go`：

```go
func processAliveUnix(pid int) bool { return syscall.Kill(pid, 0) == nil }
```

被杀的子进程在被 `cmd.Wait()` 回收前处于**僵尸态**，此时 `kill -0` 仍返回成功——
它探测的是"PID 表项是否存在"，而非"进程是否在运行"。

探针数据：

| 时点 | `kill -0` 结果 |
|---|---|
| `kill_pid` 返回后立刻 | `true`（僵尸） |
| 400ms 后 | `false` |

由此在 macOS 上产生 3 个**假失败**：`TestShellCommandKillBackground`、
`TestCleanupBackgroundKillsAll`、`TestShellCommandTimeoutKeepsProcessAlive`
（后者的 kill 后断言部分）。杀树逻辑本身是正确的。

这项本身不是产品缺陷，但它**掩盖了真实回归的能力**——`internal/tools/` 在 POSIX 上
无法全绿，真实缺陷（如 01）就藏在噪音里。建议 `processAliveUnix` 改为短超时内轮询，
或用 `ps -o stat=` 排除 `Z` 状态：

```go
func processAliveUnix(pid int) bool {
    deadline := time.Now().Add(500 * time.Millisecond)
    for time.Now().Before(deadline) {
        if syscall.Kill(pid, 0) != nil {
            return false
        }
        time.Sleep(50 * time.Millisecond)
    }
    return true
}
```

另：`docs/tasks/PROGRESS.md` 的 ADR-038 两条记录（0.9.0 / 0.9.1）均称"全量
`go build/vet/test ./...` 全绿"，但验证在 Windows 上完成。建议补记平台状态，
避免后续误以为 POSIX 已覆盖。

### 04. 超时杀进程组的测试语义已过期（中等，测试语义）

`internal/tools/shell_process_unix_test.go` 的 `TestShellTimeoutKillsProcessGroup`
断言：超时（300ms）后，命令派生的 `nohup sleep 100 &` 应被杀。

但 ADR-038 勘误（2026-08-13 扩展）已把超时语义从"杀树"改为**"不杀树、自动转后台
托管"**。该测试与新语义直接矛盾，因此在 POSIX 上失败——**这是真实失败，不是判活
问题**（与 03 不同）。

PROGRESS.md 记录 "Windows 测试语义反转（超时后孙进程存活 → CleanupBackground 杀）"，
说明勘误时已识别到需要反转，但只改了 Windows 侧，POSIX 侧漏了。

修复方向与 Windows 侧一致：断言超时后进程组**仍存活**（转后台语义），再经
`CleanupBackground` 或 `kill_pid` 确认可终止。注意该测试目前用
`middleware.NewRuntimeContext()`（`StatePath` 空 → 日志落 `os.TempDir()`），
改写时若要断言日志路径需补 StatePath。

### 05. Start 失败时 Windows 上双重 CloseHandle（低）

`internal/tools/background.go:60-63`：

```go
if err := cmd.Start(); err != nil {
    closeProcessTree(tree)
    return tree, err        // ← 已关闭的句柄仍被返回
}
```

调用方 `internal/tools/shell.go:122` 的错误分支再 close 一次。

探针确认路径可达：坏 `workdir` → `shell_command: fork/exec /bin/sh: no such file or
directory`。POSIX 上 `closeProcessTree` 是 no-op 故无害；Windows 上是对已关闭句柄的
二次 `CloseHandle`，通常返回错误码被 `_ =` 忽略，但若该句柄值已被内核复用给其它资源，
就成了关闭无关句柄的未定义行为。

修复：失败时返回零值句柄（`var zero processTreeHandle; return zero, err`），
与超时转后台分支"tree 置零跳过 defer close"的既有手法一致。

---

## 确认无误的部分

以下几处特意验证过，结论是正确的，记录以免后续重复审查：

**`signature_delta` 修复（`661a303` 前的 `da3448b` 批次）确实补上了一个功能失效。**
此前 DeepSeek 流式的 thinking 签名全部丢弃（`content_block_start` 处签名为空串，
真实签名走独立的 `signature_delta` 事件），ADR-025 的 thinking 完整回传形同虚设。
核对 `pendingBlock` 在 `blocks map[int64]*pendingBlock` 中是指针，`pb.signature`
赋值能正确传播到 `thinking_done`。

**compact 的两处改动都对。** 先 `Segment` 落盘后重写内存，保证落盘失败时双轨一致
（都还是压缩前），下轮干净重试；`in.Messages = rc.Messages.Messages` 修掉了
"压缩后仍用压缩前快照采样"。核对 `agent.Run` 的 `conversation := rc.Messages`
是同一指针，压缩重写能被后续循环读到。

**`build.go` 把 `CompactMiddleware` 挪到 `TodoReminderMiddleware` 之前是必要的。**
`TodoReminder` 在 `in.Messages` 上做 copy-append 注入临时提醒；若它在洋葱外层，
内层压缩重写 `in.Messages` 会把提醒覆盖丢弃。当前顺序（压缩在外、提醒在内）正确。

**`Usage` 从累计改覆盖语义正确。** `cache_read` 是"当前历史全量"而非增量，
跨轮累加必然虚高。`SetUsage` + 压缩后归零 + footer/` /usage` 同源，口径自洽。

**`ApprovalKey` 对 kill 模式派生 `"kill <pid>"` 堵住了真实风险。**
kill 模式下 command 为空，`NormalizeCommand("")` 得空 key，一旦被"本会话记住"
（`s` 决策）会命中任意空命令调用。派生显式 key 后该风险消除。plan 模式下
`classifyPlanShell("")` → unknown → Deny，强只读语义正确（plan 不该杀进程）。

**`661a303` 移除 shell 工具内截断后，错误路径仍被截断。**
`ToolOutputMiddleware.OnToolCall` 的 after 对本批全部 `tool_result` 无条件
`EvictContent`（仅 read_file 豁免），而 agent 的 `RespondToModel` 分支把
`te.Message` 作为 `ToolResult.Content` 回填——错误文本走同一条截断路径，
无遗漏。

---

## 建议处理顺序

1. **01**（严重，产品行为）——照 ADR-038 原文补 `stop()`，改动约 5 行
2. **02**（安全边界）——Wait goroutine 补 `unregisterBackground`，改动 1 行
3. **03 + 04**（测试）——修判活方式 + 反转超时语义断言，让 `internal/tools/`
   在 POSIX 上全绿，恢复回归检测能力
4. **05**（低）——失败时返回零值句柄

01 与 02 同属"POSIX 上杀错进程"这一类，建议同批修复并补回归锚点：
一个"命令派生后台进程 + 命令正常退出 → 派生进程应存活"的测试，
正是当前缺失、且能锁住 01 的锚点。

---

## 附：验证方式说明

01 的结论用**对照实验**而非单侧探针得出——先证明 plain `exec` 下孙进程会存活
（排除"孙进程本来就会死"这一替代解释），再证明经工具后被杀，两组唯一差异是
harness 的杀树逻辑。03 的僵尸态结论用同一 PID 在两个时点的判活差异得出
（立刻 true / 400ms 后 false），排除了"杀树没生效"这一解释。05 用坏 `workdir`
确认失败路径可达，而非仅从代码推断。

所有探针文件跑完即删，`git status` 已确认工作区干净；临时对照 worktree
（用于确认 e2e `TestSessionPersistenceE2E` 失败为既存问题、非本批引入）已移除。
