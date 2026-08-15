# 后台任务完成自动反向通知 + 唤醒器代码审查报告

> 审查对象：commit `650c0d2`（feat: 后台任务完成自动反向通知 + 唤醒器，ADR-040，版本 0.9.3）
> 审查日期：2026-08-14 ｜ 审查者：独立审查（未参与该提交的实现）
> 范围：25 文件，+1487 / -28；`internal/completion`（新包）、rc 注入（runtime_context/session）、
> `BackgroundCompletionMiddleware`、shell 生产端（notifyCompletion/compensateTransferNotify）、
> TUI 唤醒器（MaybeWake/RunWakeup/wakeSignal/Model 双闸）、`events.EventNotice`、4 个新测试文件
> 方法：计划文档逐条对照 + 全量读码 + **临时探针**（跑完即删，未入库）+ 全量测试/-race/交叉编译
> 审查平台：**darwin (macOS)** —— 与开发/验证平台（Windows）不同，POSIX 侧行为（竞态压测、探针）本轮实测

## 摘要

设计质量总体高：三处评审轮修复（同步抢占防双唤醒 / 超时瞬间竞态补偿 / 失败不热循环）**全部按计划落地且各有回归锚点**；生产端 no-op 分支语义自洽，补偿机制在 40 次竞态压测下零双通知、零注册表残留；anthropic 邻接约束、resume 双路径、wake 登记覆盖、退出后 `Send` 安全性均核实无误。锁语义与崩溃窗口记录符合计划。

但发现 **1 项应处理的并发缺陷**（与计划已修的"双唤醒"竞态同类，但计划未覆盖 compact×wake 组合），另有 4 项低级别问题与 1 项测试覆盖缺口：

| 编号 | 缺陷 | 级别 | 证据 |
|---|---|---|---|
| 01 | `/compact` 分派后、cmd 执行前的间隙，`completionWakeMsg` 可穿过双闸并发启动唤醒 run → 与 `RunCompact` 并发读写同一 conversation（data race） | 中等 | **探针**（compact 分派后两道闸放行唤醒 cmd）+ 读码（RunCompact 的 setCancel 在异步 cmd 内） |
| 02 | 超时转后台消息"它仍在运行"在"进程恰在超时瞬间已完成"时不准（补偿通知会立即纠正，但文案误导"不要重试"） | 低 | **探针量化**（1ms 超时压测 40/40 命中补偿路径） |
| 03 | 注入中间件缺 `rc.Messages != nil` 守卫（非会话构造 rc 挂 Completions 时注入非空 drain 会 panic） | 低（防御性） | 读码 |
| 04 | PID 复用窗口下前台 `notifyCompletion` 可能命中"刚死未注销"的旧后台条目，发一条错的前台通知 | 低（理论） | 读码；概率可忽略 |
| 05 | Esc 打断"已创建但未真正开跑"的唤醒 run → 写入伪中断提示用户消息，污染 conversation | 低 | 读码 |
| 06 | 测试覆盖缺口：01 无回归锚点；无 Esc 打断唤醒 run / 非 active 会话事件 / 退出后 Send 安全 / EventNotice 渲染器忽略行为测试 | 低（测试） | 对照测试清单 |

---

## 一、计划三处评审轮修复的落地核实（全部落地 ✓）

### 修复 1：同步抢占防双唤醒 ✓
- `MaybeWake` 在返回 cmd **之前**同步 `setCancel`（controller.go:180-182），`RunWakeup` 复用该 ctx/cancel（controller.go:154-165）——tea.Cmd 异步执行间隙的第二条 wake 消息会被 `isRunning()` 拦截。
- Model 层 `m.running` 第二道闸（model.go:299-301）。
- 锚点：`TestMaybeWakePreemptsSynchronously`、`TestDoubleWakeNoConcurrentRuns`（确定性，非时序依赖）。
- **但**：该修复只覆盖"wake×wake"与"wake×用户 run"，未覆盖"compact×wake"——见缺陷 01。

### 修复 2：超时转后台竞态补偿 ✓
- `compensateTransferNotify`（background.go:164-171）：DeadlineExceeded 分支注册后对 `done` 做非阻塞 receive（shell.go:199），已有结果则补注销+通知。
- 两路恰好一个拿到 entry（`unregisterBackground` 原子返回一次非 nil），无双通知；无结果则 goroutine 之后自会通知。
- 锚点：`TestCompensateTransferNotify`（两分支确定性）+ `TestShellCommandTimeoutRaceCompensation`（集成）。
- **探针加测**：并发 16 goroutine 同时 `notifyCompletion(同一 pid)` → 恰好 1 条事件、条目注销；`exit 0`+1ms 超时压测 40 次 → **零双通知、零注册表残留**（40/40 命中补偿路径）。

### 修复 3：失败不热循环 ✓
- `handleRunDone` 末尾补唤醒仅当 `err == nil`（model.go:755-764），`err != nil` 时 pending 留待下一次信号/用户消息。
- 推理自洽：成功 run 必跑过首采样、Drain 必已清空 pending，故 `err==nil && pending>0` 恰好只对应竞态窗口。
- 锚点：`TestRunDoneFailedNoRetry`（FakeClient 返回错误 → 不补唤醒、pending 保留）。

## 二、已确认缺陷

### 01. `/compact` 分派后、cmd 执行前间隙可并发启动唤醒 run（中等）

- **现象**：`/compact` 命令分派（popup.go:220-229）只设 `m.toast` 并返回 `m.c.RunCompact()`，**没有**像 `startRun`/`maybeStartWake` 那样同步置 `m.running = true`；而 `RunCompact` 的 `setCancel` 在 **cmd 体内**异步执行（controller.go:306，`runCtx, cancel := ...; c.setCancel(cancel)` 在 `func() tea.Msg {...}` 里）。于是存在一个窗口：Update 返回 compact cmd 之后、bubbletea 执行它之前，`cancel == nil` 且 `m.running == false`。此时若后台进程完成、`completionWakeMsg` 到达：
  - `case completionWakeMsg: if m.running {...}`（model.go:299）——放行（m.running 为 false）；
  - `maybeStartWake` → `MaybeWake`（controller.go:176-183）：active 非 nil、`isRunning()` 为 false（cancel 未设置）、`PendingCount() > 0` → **同步抢占 + 返回唤醒 cmd**。
  - bubbletea 对多个待执行 cmd 各起 goroutine **并行**执行（代码库自身即依赖此语义：`runs.WaitGroup` + `WaitRuns` 正是为"Run 与 RunCompact 可同时在途"而存在，Bug09）→ `RunCompact`（`compactor.Run` 读并重写 `conversation.Messages`、`rc.Segment` 切段）与 `RunWakeup`（Drain → `AddUser` append、`repairDanglingToolUse` 重建切片、采样读切片）**并发读写同一 session conversation**——data race，可能丢消息/快照错乱。
- **根因**：与计划已修的双唤醒竞态同类，但同步置位只覆盖了用户 run（`startRun`）与唤醒 run（`maybeStartWake`）两个入口，漏了 `/compact` 这一个同样返回异步 run 类 cmd 的入口。
- **探针**（跑完即删）：`TestProbeCompactDispatchWakeWindow` —— `c.RunCompact()` 分派后 `c.isRunning()` 为 false、`m.Update(completionWakeMsg{})` 返回**非 nil 唤醒 cmd**（两道闸放行），窗口确认；对照 `TestProbeCompactRunningBlocksWake` —— compact 执行中（cancel 已设置）唤醒被正确拦截，即窗口仅限 cmd 执行前间隙。
- **建议修法**（二选一，推荐前者，1-2 行）：
  1. `/compact` 分派时同步置 `m.running = true`（与 startRun 同款），`handleCompactDone` 末尾复位；并顺手在 compact 完成后补一次 `MaybeWake`（压缩期间到达的 pending 可被取走）。
  2. `RunCompact` 在创建 cmd 时同步 `setCancel`（同 MaybeWake 抢占模式），cmd 复用。
- **建议**：立即修，并补回归测试（`compact 分派后 completionWakeMsg 不产生唤醒 cmd`）。

### 02. 超时转后台消息"它仍在运行"在补偿路径不准确（低）

- **现象**：超时转后台消息固定称"不要重试该命令——它仍在运行"（shell.go:201）。当 select 在 `done` 与 `ctx.Done` 同时就绪时随机选到超时分支、或进程恰在超时瞬间已死时，`transferToBackground` 注册的是**已死**进程，`compensateTransferNotify` 立即补发完成通知——模型同时收到"它仍在运行"与"已退出（exit N）"。通知内容正确、会立即纠正误解，但工具返回文案在那一刻是错的。
- **探针量化**：`exit 0` + `timeout_ms=1` 压测 40 次，**40/40 命中补偿路径**（本机 sh 启动 > 1ms，进程必然先于超时分支完成）——即 40 次消息均不准确。真实默认 30s 超时下此窗口极罕见（需进程恰在超时边界完成），但测试与紧超时场景下可复现。
- **建议修法**（可选）：补偿分支若 `done` 已有结果，返回消息改为"命令已在超时前完成（exit N）"或直接走正常完成分支；或接受现状（通知会纠正）。低优先级。

### 03. 注入中间件缺 `rc.Messages` nil 守卫（低，防御性）

- **现象**：`background_completion.go:31` 守卫为 `rc.Completions != nil && rc.AppendUser != nil`，drain 非空时 `in.Messages = rc.Messages.Messages`（:40）——若构造非会话 rc 时挂了 Completions 而未设 Messages（如未来子 agent/测试复用通道时漏配），注入即 nil 解引用 panic。当前生产路径（`Session.RuntimeContext()` 恒设 Messages）不受影响。
- **建议修法**：守卫补 `rc.Messages != nil`（或注释声明前置条件）。低优先级。

### 04. PID 复用窗口下前台 notify 的交叉通知（低，理论）

- **现象**：前台 Wait goroutine 无条件调 `notifyCompletion(cmd.Process.Pid, err)`（shell.go:135）。若一个后台进程恰好刚死、其自身 Wait goroutine 尚未注销条目，而 OS 把同一 PID 复用于一条前台命令，前台 notify 会命中旧条目、用旧条目的 queue/logPath 发一条"前台命令完成"通知（内容指向旧进程日志）。窗口 = 后台死后到其 goroutine 注销的微秒级间隙 × 同一瞬间的 PID 复用，概率可忽略；且 pre-ADR-040 已存在同类注销竞态（前台 pid 命中刚死后台条目会误注销）。
- **建议**：不修，记录即可（如需彻底，可在前台路径校验 entry.logPath 与本次 tmpLog 的关系——过度设计）。

### 05. Esc 打断"未真正开跑"的唤醒 run 产生伪中断提示（低）

- **现象**：`maybeStartWake` 同步抢占 cancel 后，若用户恰在唤醒 cmd 执行前按 Esc → `cancelRun` 取消 runCtx → 唤醒 run 启动即返回 `context.Canceled` → `handleRunDone`（model.go:753-756）写入 `"(System: the previous agent turn was interrupted...)"` 用户消息——对一个从未采样、从未产生输出的唤醒 run 打中断标记，污染 conversation（多一条 user 行，模型后续会看到）。
- **建议修法**（可选）：`RunWakeup` 的 cmd 开头若 `runCtx.Err() != nil` 直接返回空 `runDoneMsg{}`（不写中断提示）；或 handleRunDone 仅当 `m.interrupted` 时写提示。低优先级。

### 06. 测试覆盖缺口（低，测试）

- ① 缺陷 01 无回归锚点（建议补）；② 无"Esc 打断唤醒 run"测试（05）；③ 无"非 active 会话事件不打扰当前会话"测试；④ 无"退出后 OnAppend→Send 安全"测试（bubbletea v1.3.10 `Send` 的 `ctx.Done()` 守卫仅读源码确认：tea.go:774-779，程序停止后为 no-op 不 panic）；⑤ `EventNotice` 在 text/json 渲染器的忽略行为无测试（读码确认 switch default 忽略，安全）。
- 另外 `TestShellCommandTimeoutRaceCompensation` 在本机 40/40 走补偿路径从不 skip；在进程 spawn 快于 1ms 的机器上可能 `err == nil` 触发 `t.Skip`——锚点会静默跳过而非失败（可接受，但建议留意）。

## 三、计划外核实通过项（未发现缺陷）

- **锁语义**：`Append`（queue.go:46）锁内 append+持久化、`OnAppend` 锁外快照调用；`Drain`（:60）锁内取出+清空+落盘；`SetOnAppend`（:80）锁内读写——与 Wait goroutine 并发安全（-race 覆盖）。
- **原子落盘**：pid 临时名 + fsync + rename（agentstate.SaveFile 同款）；同路径多 Queue 实例仅存在于测试，生产单实例。
- **崩溃窗口**：Drain 落盘清空先于逐条 AppendUser——崩溃丢事件不重复（文档已记录）；磁盘写失败边缘可能反向重复（drain 清空失败、盘上仍是旧内容 → resume 重注入），属"尽力而为"边界，未记入文档，建议 ADR 一句话补充。
- **no-op 分支**：kill_pid（handleKill 先注销）✓、CleanupBackground（LoadAndDelete 先于杀树，Wait goroutine notify 见 nil）✓、前台正常完成（pid 从未注册）✓、Esc（pid 从未注册）✓——探针确认前台路径零事件零残留。
- **退出码**：`exec.ExitError.ExitCode()`，signal 杀 = -1（POSIX 信号终止 ProcessState.ExitCode 返回 -1，符合计划语义）。
- **条目字段捕获**：startBackground 与 transferToBackground 两处均经 `captureCompletion` 捕获 queue/sessionID/logPath；`rc.Completions == nil`（非会话）跳过，条目仍正常管理进程。
- **anthropic 邻接约束**：注入只发生在 reasoning 开始（onReasoning before），此时 conversation 尾部必为 user/assistant 文本或完整 tool_results（`repairDanglingToolUse` 已在采样前兜底、`runToolBatch` 恒补全结果块）——user 通知不会插进 tool_use/tool_result 之间；连续 user 消息 anthropic 会合并，合法。
- **洋葱顺序**：BackgroundCompletion 挂 Compact 之后（外层先压缩重写、内层再注入同步 in.Messages）、TodoReminder 之前（提醒装饰不被覆盖）——符合计划；TodoReminder 的临时提醒追加在注入之后，采样输入含两者，正确。
- **resume 双路径**：Drain 落盘清空先于 AppendUser → 事件要么在 transcript（已注入）要么在 completions.json（未注入），不会两处并存；resume 队列加载 + 首次采样注入，无重复。
- **wake 登记覆盖**：setSend 遍历 open（初始 sess 在 NewController 已入表）✓、ensureActive ✓、SwitchTo ✓——全部会话来源覆盖；且新会话不可能先有后台进程（进程必然由其所属会话首次打开时启动），无"Append 先于登记"的漏信号窗口。
- **Send 退出后安全**：bubbletea v1.3.10 `Program.Send` 有 `ctx.Done()` 守卫（tea.go:774-779），退出竞态下 Wait goroutine 的 Append→Send 为 no-op 不 panic；且 CleanupBackground 先 LoadAndDelete，绝大多数场景 entry 已空根本不通知。
- **EventNotice 消费端**：TUI 渲染系统行（model.go:713）；transcript writer default 跳过（不重复，user 行已由 AddUser 写）；text/json 渲染器 default 忽略（run 模式无 UI 副作用）。

## 四、验证记录

```
go build ./... && go vet ./...                        → 绿
go test ./... -count=1 -skip TestSessionPersistenceE2E → 全包绿
go test -race ./internal/completion/ ./internal/tools/ ./internal/ui/tui/ ./internal/session/ → 全绿
GOOS=linux/darwin/windows go build ./cmd/harness      → 交叉编译绿
临时探针（跑完即删，未入库）：
  - 并发 16 goroutine notifyCompletion(同 pid) → 恰好 1 条事件、条目注销
  - exit 0 + timeout_ms=1 压测 40 次 → 零双通知、零注册表残留；40/40 命中补偿路径（量化缺陷 02）
  - 前台正常完成 → 零事件、零残留
  - completions.json 往返 → 恢复 1 条、内容一致
  - /compact 分派后 completionWakeMsg → 唤醒 cmd 非 nil（确认缺陷 01 窗口）
  - compact 执行中（cancel 已设）→ 唤醒被拦截（窗口边界）
e2e：TestSessionPersistenceE2E 在 macOS 既存失败（与 650c0d2 无关——HEAD 复现；
    /var→/private/var 符号链接使子进程 cwd 与父进程 t.TempDir() 路径转义错位，
    工作区桶目录不一致），其余 e2e 全绿
```

## 五、结论

计划的三处评审轮修复全部落地且锚点充分，生产端在竞态压测下表现正确（零双通知、零泄漏），注入链路与邻接约束、resume 双路径、登记覆盖均无问题。**建议立即修缺陷 01**（`/compact` 分派同步置 `m.running`，1-2 行 + 1 个回归测试）——它与计划已修的"双唤醒"是同一类竞态，属于修复范围的遗漏而非新方向。02-06 可记录后随阶段 7 架构整理一并处理，或按优先级挑选（02 建议顺手把文案口径修正）。整体可以认为 0.9.3 达到可发布质量，但 01 修正后建议补跑全量 + `-race`。

---

## 修复记录（2026-08-14）

用户拍板：修 01 + 02 文案 + 01 回归锚点；03/04/05 记录待阶段 7 处理。

- **01 已修**：`popup.go` /compact 分派同步置 `m.running/turnDone/eventError`（RunCompact 的 setCancel 在异步 cmd 内，闸须更早落下）；`model.go handleCompactDone` 复位 `running/interrupted`。副作用（均为收益）：compact 期间用户输入进队列、Esc 可中断压缩、spinner 转动。回归锚点 `TestCompactDispatchBlocksWake`（分派后 wake 消息被闸丢弃、pending 保留、完成复位、零 agent.Run）。被闸丢弃的 pending 留待下一次完成信号/用户消息注入（不丢，延迟）；是否在 handleCompactDone 末尾补 MaybeWake 留待阶段 7 一并看。
- **02 已修**：超时转后台文案"它仍在运行"→"它**可能**仍在后台运行"（shell.go 返回消息 + 工具注释），消除进程恰已死时的硬断言误导；"不要重试"保留（原命令可能仍在跑，重试语义不变）。
- **03/04/05**：未修，记录进 `docs/plans/architecture-cleanup-2026-08-13.md` 阶段 7 待办（03 rc.Messages nil 守卫；04 PID 复用理论窗口；05 Esc 打断未开跑唤醒 run 的伪中断提示）。
- **验证**：tui + tools 全量测试绿；新增锚点绿。
