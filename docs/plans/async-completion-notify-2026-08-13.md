# 后台任务完成自动反向通知 + 唤醒器（通用 async 通道，shell 先接）

> 计划日期：2026-08-13 ｜ 状态：已批准待实施 ｜ 版本目标：0.9.3
> 参照：AgentScope Java v2（`AsyncToolMiddleware` + `AsyncToolRegistry` + `MessageBus.inbox` + `InboxMiddleware` + `WakeupDispatcher`）

## Context

shell 后台进程（`background: true` 与超时转后台）完成时，harness 不主动通知模型——模型被要求"轮询日志"，浪费 token/回合，且 `cmd.Wait()` 的退出码被直接丢弃。参照 AgentScope Java v2 的机制，实现：后台任务完成 → **落盘完成事件 → 下一次推理开始前注入对话末尾**；会话空闲时 → **唤醒 run 自动继续**。

**用户逐点拍板**：
1. 保留轮询能力，提示词强调"可等通知"，模型自行选择。
2. **通用 async 通道**（独立 `internal/completion` 包），阶段 5 子 agent 复用同一链路。
3. 完成事件落盘（独立 `completions.json`，**不挂 AgentState**——完成通知是"一次性事件"不是"会话状态"）。
4. 通知角色 = **RoleUser**（复用 LineTypeUser，transcript/load 零改动）。
5. 唤醒器**本轮做**：决策逻辑收敛在 `Controller.MaybeWake()`，Model 只薄转发；agent 核心零耦合（"何时启动 run"本就是编排层职责，唤醒只是第二个触发源）。
6. TUI 事件桥复用（`completionWakeMsg` 走既有 `program.Send`，bubbletea 单线程串行吃掉竞态）。

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
- `internal/agent/build.go:64` 挂载在 `CompactMiddleware` 之后、`TodoReminderMiddleware` 之前。

### 5. `internal/tools` — shell 生产端
- `bgProcess` 条目加 `queue *completion.Queue` + `sessionID` + `logPath`（启动时从 rc 捕获，Wait goroutine 用）。
- `startBackground` / `transferToBackground`：从 rc 捕获上述字段存进条目（`rc.Completions == nil` = 非会话，跳过）。
- 抽 `notifyCompletion(pid int, exitErr error)`：`entry := unregisterBackground(pid)`；entry 非 nil 且 queue 非 nil → 拼 `Result`（"（系统通知：后台进程 <pid> 已退出（exit <code>）。日志：<path>，可用 read_file 查看输出）"）+ `queue.Append`。
- 两处 Wait goroutine 完成时调它：`startBackground` 的 `go cmd.Wait()`（background.go）；前台 `go func(){ err := cmd.Wait(); f.Close(); done <- err }()`（shell.go:130——前台正常完成 pid 不在 registry → no-op 天然正确；转后台后进程死 → 通知）。
- 退出码：`exec.ExitError.ExitCode()`（signal 杀 = -1）。
- `shell.go` 文案：超时转后台与 background:true 返回消息改为"已在后台运行，PID/日志…；**完成会自动通知**，可继续其它任务等通知，也可用 read_file/grep 轮询日志观察进度"。

### 6. `internal/ui/tui` — 唤醒器（决策收敛在 Controller）
- `Controller`（controller.go）：
  - `RunWakeup() tea.Cmd`：`Run`（controller.go:89）去 `AddUser` 的变体（其余一致：ensureActive、rc 注入、setCancel/clearCancel、agent.Run）。
  - `isRunning() bool`：锁内读 `cancel != nil`（**复用现有状态，零新增字段**）。
  - `MaybeWake() tea.Cmd`：`active == nil || isRunning() || PendingCount() == 0` → nil；否则 `RunWakeup()`。
  - wake 回调注入：`setSend` 后生成 `wakeSignal = func(){ send(completionWakeMsg{}) }`；会话登记点（resume/ensureActive/switch 打开会话处）统一 `s.Completions().SetOnAppend(wakeSignal)`。非 active 会话的事件也发信号，但 MaybeWake 查 active 的 pending → 空 → 忽略（不打扰当前会话）。
- `Model`（model.go）：`case completionWakeMsg:` → `cmd := m.c.MaybeWake()`；非 nil → `m.appendSystem("后台进程完成，继续执行…", false)` + `m.running = true` + refresh + 返回 cmd。run 结束后 `handleRunDone` 消费用户输入队列（衔接不变）。
- 消息类型 `completionWakeMsg struct{}`（events.go）。

### 7. 版本 + 文档
- `cmd/harness/main.go`：0.9.2 → 0.9.3。
- DECISIONS.md：**ADR-040**（后台任务完成自动反向通知 + 唤醒器，含与 AgentScope 机制对照 + 单进程简化理由 + 唤醒决策收敛 Controller 的分层论证）。
- PROGRESS.md 新条目；IMPLEMENTATION_PLAN.md 现状行追加。

## 测试

- `internal/completion`：Append/Drain/PendingCount + Save/Load 往返 + OnAppend 触发 + 并发 `-race`。
- `internal/middleware/impl`：drain 后 `AppendUser` 收到每条 Result、`in.Messages` 同步、空队列/`Completions` nil 透传。
- `internal/session`：RuntimeContext 注入两钩子；Resume 后 pending 从 completions.json 恢复。
- `internal/tools`（Windows 可跑）：`background: true` 起短命令 → 自然退出 → queue 收到事件；`kill_pid` 杀 → 不收到；前台正常完成 → 不收到；超时转后台 → 进程死 → 收到。
- `internal/ui/tui`：`MaybeWake` 三分支（active nil / 在途 run / pending 空）+ `completionWakeMsg` 触发 run（controller 已有测试模式）。
- 全量 `go build/vet/test ./...` + tools `-race` + linux/darwin 交叉编译；POSIX 实测（Mac）后续补。

## 实施顺序

1. completion 包 + 测试
2. middleware rc 字段 + session 注入 + 测试
3. BackgroundCompletionMiddleware + build.go 挂载 + 测试
4. tools shell 生产端 + 文案 + 集成测试
5. TUI 唤醒器 + 测试
6. 文档（ADR-040 / PROGRESS / 计划现状）+ 版本 bump + 全量验证 + `go install`
7. ~~计划落盘 + 提交 push~~（本文件即第 7 步）
