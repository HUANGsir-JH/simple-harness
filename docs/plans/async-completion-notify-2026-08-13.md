# 后台任务完成自动反向通知 + 唤醒器（通用 async 通道，shell 先接）

> 计划日期：2026-08-13 ｜ 状态：已实施 ✅（2026-08-13，版本 0.9.3，ADR-040）
> 参照：AgentScope Java v2（`AsyncToolMiddleware` + `AsyncToolRegistry` + `MessageBus.inbox` + `InboxMiddleware` + `WakeupDispatcher`）

## Context

shell 后台进程（`background: true` 与超时转后台）完成时，harness 不主动通知模型——模型被要求"轮询日志"，浪费 token/回合，且 `cmd.Wait()` 的退出码被直接丢弃。参照 AgentScope Java v2 的机制，实现：后台任务完成 → **落盘完成事件 → 下一次推理开始前注入对话末尾**；会话空闲时 → **唤醒 run 自动继续**。

**用户逐点拍板**：
1. 保留轮询能力，提示词强调"可等通知"，模型自行选择。
2. **通用 async 通道**（独立 `internal/completion` 包），阶段 5 子 agent 复用同一链路。
3. 完成事件落盘（独立 `completions.json`，**不挂 AgentState**——完成通知是"一次性事件"不是"会话状态"）。
4. 通知角色 = **RoleUser**（复用 LineTypeUser，transcript/load 零改动）。
5. 唤醒器**本轮做**：决策逻辑收敛在 `Controller.MaybeWake()`，Model 只薄转发；agent 核心零耦合（"何时启动 run"本就是编排层职责，唤醒只是第二个触发源）。
6. TUI 事件桥复用（`completionWakeMsg` 走既有 `program.Send`；bubbletea 单线程串行吃消息，但 tea.Cmd 异步执行——防并发 run 仍需 `MaybeWake` 返回前同步抢占 cancel，见 §6）。

## 数据链路图

```
┌──────────────────────────────────────────────────────────────────────┐
│ ① 生产端（异步：Wait goroutine，后台进程自然退出时）                      │
│   err := cmd.Wait()                                                    │
│   entry := unregisterBackground(pid)                                   │
│     ├─ nil → kill_pid/CleanupBackground 已注销 → 不通知（模型已知/退出中）│
│     └─ 自然退出 → queue.Append(Event{...Result=拼好的通知全文})          │
│          [Queue 内锁 + 原子落盘 completions.json + 锁外调 OnAppend]     │
│          ⚠ 只写 Queue，不碰 conversation（避开主循环 data race）         │
└──────────────────────────────────────────────────────────────────────┘
            │                                        │
            ▼  completions.json（落盘）                ▼ OnAppend() → program.Send(completionWakeMsg{})
┌──────────────────────────────┐        ┌─────────────────────────────────────────────┐
│ ② 注入（每次采样前，主循环）      │        │ ④ 唤醒（bubbletea Update 串行）                │
│  BackgroundCompletionMiddleware │        │  case completionWakeMsg:                  │
│  onReasoning before：           │        │    cmd := c.MaybeWake()                    │
│   for ev := range Drain():      │        │      ├─ active nil → nil（懒加载未触发）      │
│     rc.AppendUser(ev.Result)    │        │      ├─ cancel != nil（在途 run）→ nil      │
│   in.Messages = rc.Messages.…   │        │      └─ PendingCount()==0 → nil（防空跑）   │
│                                 │        │    cmd 非 nil → appendSystem + running=true │
│   rc.AppendUser = session 注入   │        │      + c.RunWakeup()（Run 去 AddUser 变体）  │
│   = AddUser：conversation.Add   │        │  → agent.Run → 采样前 middleware drain      │
│     + writer.Write(user 行)     │        │    → 注入通知 → 模型回应                     │
└──────────────────────────────┘        └─────────────────────────────────────────────┘
            ▼
┌──────────────────────────────────────────────────────────────────────┐
│ ③ resume 恢复（两条路径，互不丢）                                       │
│   已注入的 → transcript user 行重建 conversation（天然含通知）          │
│   已完成未注入 → completions.json 加载 → 下次采样前仍注入               │
└──────────────────────────────────────────────────────────────────────┘
```

## 信号分流（一个事件 → 两个下游，按会话状态自然分流）

`queue.Append` 同时做两件事：**落盘** `completions.json`（持久化事实，幂等不丢）+ **锁外调 `OnAppend`**（运行时信号，只用来唤起空闲会话）。两个消费者读同一个 Queue，由"会话当时是否在采样"自然分流，互斥不重叠——**无需判断"信号该给中间件还是 TUI"**：

```
                     queue.Append(Event)
                  ┌──────────┴──────────┐
                  ▼                     ▼
        落盘 completions.json    OnAppend → completionWakeMsg
        （持久化事实）            （运行时唤起信号）
                  │                     │
                  ▼                     ▼
    ┌─────────────────────┐   ┌──────────────────────────┐
    │ 路径 A：Middleware   │   │ 路径 B：TUI 唤醒器         │
    │ onReasoning before  │   │ MaybeWake 决策            │
    │ 每次采样前 Drain()   │   │  active nil / isRunning  │
    │ → AppendUser 注入   │   │  / PendingCount==0 → 丢弃 │
    └─────────────────────┘   │  否则 RunWakeup() 拉起 run│
                  ▲           └────────────┬─────────────┘
                  │                        │ 启动的新 run
                  └────────────────────────┘
           新 run 第一次采样前，路径 A 又把 pending 注入
```

| 会话状态 | 路径 A（注入内容） | 路径 B（启动 run） | 结果 |
|---|---|---|---|
| **在途**（run 进行中） | ✅ 下一次采样前 Drain 注入 | ❌ `isRunning` → 丢弃信号 | 模型本轮就收到 |
| **空闲**（run 结束、后台仍在跑） | ❌ 无采样，没机会跑 | ✅ `RunWakeup` 拉起新 run | 新 run 首采样前 A 注入 |

**关键澄清——唤醒器不注入内容**：路径 B 只负责"启动一个 run"这一个动作，不往对话写任何东西（`RunWakeup` = `Run` 去 `AddUser` 的变体）。要注入的通知全文由路径 A 在采样前塞进对话。因此两条路径不会重复注入——`Drain` 清空后 `PendingCount()==0` 天然防重。

**竞态窗口（两路径唯一需手动衔接处）**：在途 run 的**最后一次采样已过**、模型正吐最终答案时后台恰好完成——路径 A 不会再 Drain（采样已结束），路径 B 又因 `isRunning` 丢弃信号 → pending 残留无人取。修复：`handleRunDone` 末尾（消费完用户队列后、**且 `err == nil`**）再补一次 `MaybeWake` 重新评估（见 §6 Model）。`err != nil` 时补唤醒会形成"唤醒失败 → pending 未清 → 再唤醒"热循环（唤醒 run 首采样前失败即属此列）；成功 run 必跑过首采样、Drain 必已清空 pending，故 `err == nil && pending > 0` 恰好只对应此竞态窗口——失败时 pending 留待下一条用户消息/下一次完成信号注入，不丢。

> 不用借用用户消息队列：用户队列语义是"在途缓存 → 结束重放"（消费入口 `handleInput` 会 `AddUser`），与唤醒的"空闲启动 → 在途丢弃"（消费入口 `RunWakeup` 不 `AddUser`）时机相反、入口不同，且用户队列无"空闲自动出队"机制。⚠ 但**不能只靠** bubbletea 单线程防并发：tea.Cmd 由 bubbletea 在 Update 返回后异步执行，`cancel` 若在 cmd 内才 `setCancel`，连续两条 wake 消息会在间隙双双通过 `isRunning` 检查 → 两个 run 并发跑同一 conversation（data race + 双倍采样）。串行化 = `MaybeWake` 返回 cmd **之前同步** `setCancel` 抢占 + Model 层 `m.running` 同步闸（见 §6）。

## 组件改动

### 1. `internal/completion`（新包，只依赖 stdlib）
- `event.go`：`Event{ToolName, ToolCallID string, Result string, ExitCode *int, DoneAt, SessionID string}`。`Result` = 生产端拼好的通知全文；`SessionID` 为阶段 5 跨会话预留。
- `queue.go`：`Queue`——`mu sync.Mutex` + `events []Event` + `path string` + `onAppend func()`。
  - `New(path string) *Queue`（文件不存在 = 空队列）
  - `Append(ev)`：锁内 append + 全量原子落盘（`agentstate.SaveFile` 同款 pid 临时名 + fsync + rename）+ **锁外**调 `onAppend`（防回调重入死锁）
  - `Drain() []Event`：锁内取出并清空 + 落盘
  - `PendingCount() int`、`SetOnAppend(fn func())`

### 2. `internal/middleware` — rc 两个注入（rc.Segment 同款防环模式）
- `RuntimeContext` 加：
  - `Completions *completion.Queue`（session 注入；nil = 无异步通知能力，非会话/测试）
  - `AppendUser func(content string)`（session 注入 = `AddUser`；让 middleware 能写 conversation + transcript，middleware 拿不到 writer）

### 3. `internal/session` — Queue 生命周期 + 注入
- `Session` 加 `completions *completion.Queue`；`Create`/`Resume` 时 `completion.New(filepath.Join(dir, "completions.json"))`（Resume 自动恢复未注入事件）。
- 访问器 `Completions()`。
- `RuntimeContext()`（session.go:131）：`rc.Completions = s.completions`；`rc.AppendUser = func(c string){ s.AddUser(c) }`。
- transcript.go / load.go **零改动**（通知复用 LineTypeUser）。

### 4. `internal/middleware/impl` — 注入中间件
- 新 `background_completion.go`：`BackgroundCompletionMiddleware`（onReasoning before）：
  ```go
  if rc.Completions != nil {
      for _, ev := range rc.Completions.Drain() { rc.AppendUser(ev.Result) }
      in.Messages = rc.Messages.Messages  // 注入后同步采样输入（CompactMiddleware 同款）
  }
  return next(ctx, rc, in)
  ```
- **路径 A 可见性（rc.Emit 系统行）**：`AppendUser` 只写 conversation + transcript，TUI 时间线是事件驱动的、看不到注入——模型会突然回应一条界面上从未出现过的通知。`internal/events` 新增 `EventNotice` 类型（`Text` = 通知全文）；注入后 `rc.Emit != nil` 时逐条 `rc.Emit(events.Event{Type: events.EventNotice, Text: ev.Result})`。transcript writer 的 default 分支不落盘未知类型（user 行已由 AddUser 写入，不重复）；text/json 渲染器 default 忽略（可选加 case，run 模式便于观察）；TUI `handleAgentEvent` 加 case 渲染系统行（见 §6）。
- `internal/agent/build.go:64` 挂载在 `CompactMiddleware` 之后、`TodoReminderMiddleware` 之前。

### 5. `internal/tools` — shell 生产端
- `bgProcess` 条目加 `queue *completion.Queue` + `sessionID` + `logPath`（启动时从 rc 捕获，Wait goroutine 用）。
- `startBackground` / `transferToBackground`：从 rc 捕获上述字段存进条目（`rc.Completions == nil` = 非会话，跳过）。
- 抽 `notifyCompletion(pid int, exitErr error)`：`entry := unregisterBackground(pid)`；entry 非 nil 且 queue 非 nil → 拼 `Result`（"（系统通知：后台进程 <pid> 已退出（exit <code>）。日志：<path>，可用 read_file 查看输出）"）+ `queue.Append`。
- 两处 Wait goroutine 完成时调它：`startBackground` 的 `go cmd.Wait()`（background.go）；前台 `go func(){ err := cmd.Wait(); f.Close(); notifyCompletion(pid, err); done <- err }()`（shell.go:130——前台正常完成 pid 不在 registry → no-op 天然正确；转后台后进程死 → 通知）。
- **超时转后台竞态窗口**：进程恰在超时瞬间已死时，Wait goroutine 的 notify 先于 `transferToBackground` 注册执行（no-op）→ 无人再通知 + 死条目残留注册表。DeadlineExceeded 分支在 `transferToBackground` 之后对 `done` 做一次**非阻塞 receive**：已有结果 → 补 `unregisterBackground(pid)` + `notifyCompletion(pid, err)`；无结果 → goroutine 仍在等、进程死后自会通知。两路恰好一个拿到 entry，天然不会双通知。
- 退出码：`exec.ExitError.ExitCode()`（signal 杀 = -1）。
- `shell.go` 文案：超时转后台与 background:true 返回消息改为"已在后台运行，PID/日志…；**完成会自动通知**，可继续其它任务等通知，也可用 read_file/grep 轮询日志观察进度"。
  > **已知局限（记录，不本轮处理）**：`harness run` 单轮模式（cmd/harness/run.go，主要测试用）无 TUI 唤醒器——"完成会自动通知"只在回合采样期间成立，模型若"结束回合等通知"则通知不会到达（进程最终由 CleanupBackground 清理）。run 模式文案按"建议轮询日志"口径即可（ADR 里记录差异），完整承诺仅对 TUI 会话成立。

### 6. `internal/ui/tui` — 唤醒器（决策收敛在 Controller）
- `Controller`（controller.go）：
  - `RunWakeup(runCtx context.Context, cancel context.CancelFunc) tea.Cmd`：`Run`（controller.go:89）去 `AddUser` 的变体（rc 注入、defer clearCancel、agent.Run；active 已由 MaybeWake 判定非 nil，无需 ensureActive）。
  - `isRunning() bool`：锁内读 `cancel != nil`（**复用现有状态，零新增字段**）。
  - `MaybeWake() tea.Cmd`：`active == nil || isRunning() || PendingCount() == 0` → nil；否则**先同步抢占再返回 cmd**：`runCtx, cancel := context.WithCancel(c.ctx); c.setCancel(cancel)`，然后 `RunWakeup(runCtx, cancel)`。⚠ 抢占必须同步——tea.Cmd 由 bubbletea 在 Update 返回后异步执行，`cancel` 若在 cmd 内才设置，连续两条 wake 消息会在间隙双双通过 `isRunning` → 两个 run 并发跑同一 conversation（data race + 双倍采样）。
  - wake 回调注入：`setSend` 后生成 `wakeSignal = func(){ send(completionWakeMsg{}) }`，并**遍历 `c.open` 登记**（resume 传入的初始 sess 在 `NewController` 已进 open，早于 wakeSignal 生成）；此后 `ensureActive` / `SwitchTo` 打开新会话处同样 `s.Completions().SetOnAppend(wakeSignal)`。非 active 会话的事件也发信号，但 MaybeWake 查 active 的 pending → 空 → 忽略（不打扰当前会话）。
- `Model`（model.go）：
  - `case completionWakeMsg:` → **先 `if m.running { return m, nil }`**（第二道同步闸，兜底 handleRunDone 补唤醒与 wake 消息的间隙）；再 `cmd := m.c.MaybeWake()`；非 nil → `m.appendSystem("后台进程完成，继续执行…", false)` + `m.running = true` + refresh + 返回 cmd。
  - `handleRunDone` 消费完用户输入队列后、**`err == nil` 时末尾再补一次 `MaybeWake`**（补竞态窗口，见"信号分流"节；`err != nil` 跳过防热循环）：在途 run 最后一次采样已过后后台完成，唤醒信号被 `isRunning` 丢弃、pending 残留，run 结束重新评估——pending 还在则补启动 run，已被本轮采样吃掉则 `PendingCount()==0` 跳过（防空跑）。
  - `handleAgentEvent` 加 `case events.EventNotice: m.appendSystem(ev.Text, false)`（路径 A 注入可见性，见 §4）。
- 消息类型 `completionWakeMsg struct{}`（events.go）。

### 7. 版本 + 文档
- `cmd/harness/main.go`：0.9.2 → 0.9.3。
- DECISIONS.md：**ADR-040**（后台任务完成自动反向通知 + 唤醒器，含与 AgentScope 机制对照 + 单进程简化理由 + 唤醒决策收敛 Controller 的分层论证 + run 单轮模式无唤醒器的已知局限记录）。
- PROGRESS.md 新条目；IMPLEMENTATION_PLAN.md 现状行追加。

## 测试

- `internal/completion`：Append/Drain/PendingCount + Save/Load 往返 + OnAppend 触发 + 并发 `-race`。
- `internal/middleware/impl`：drain 后 `AppendUser` 收到每条 Result、`in.Messages` 同步、空队列/`Completions` nil 透传、注入后 `rc.Emit` 收到 `EventNotice`（Emit 非 nil 时）。
- `internal/session`：RuntimeContext 注入两钩子；Resume 后 pending 从 completions.json 恢复。
- `internal/tools`（Windows 可跑）：`background: true` 起短命令 → 自然退出 → queue 收到事件；`kill_pid` 杀 → 不收到；前台正常完成 → 不收到；超时转后台 → 进程死 → 收到；**超时瞬间进程已死（竞态窗口）→ 非阻塞 done 补通知仍收到**。
- `internal/ui/tui`：`MaybeWake` 三分支（active nil / 在途 run / pending 空）+ `completionWakeMsg` 触发 run（controller 已有测试模式）+ **连续两条 wake 消息不并发启动两个 run**（同步抢占 + m.running 闸）+ **唤醒 run 失败（err != nil）不补唤醒**（无失败热循环）。
- 全量 `go build/vet/test ./...` + tools `-race` + linux/darwin 交叉编译；POSIX 实测（Mac）后续补。

## 实施顺序

1. completion 包 + 测试
2. middleware rc 字段 + session 注入 + 测试
3. BackgroundCompletionMiddleware + build.go 挂载 + 测试
4. tools shell 生产端 + 文案 + 集成测试
5. TUI 唤醒器 + 测试
6. 文档（ADR-040 / PROGRESS / 计划现状）+ 版本 bump + 全量验证 + `go install`
7. ~~计划落盘 + 提交 push~~（本文件即第 7 步）
