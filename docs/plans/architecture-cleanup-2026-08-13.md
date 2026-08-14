# 代码架构整理（功能完善后启动，待办）

> 记录日期：2026-08-13 ｜ 状态：待启动（功能阶段 4/5/6 完善后再做）｜ 来源：后台完成通知特性（ADR-040）实施后的代码可读性讨论

## 背景

ADR-040 实施后复查代码，用户提出观察：**闭包使用频繁，可读性不好，怀疑架构设计有偏差**。讨论结论：架构方向（ADR-021/025/026/030 无状态 agent + 每 call 一个 RuntimeContext + middleware 注入 + bubbletea TUI）本身成立，闭包密集是这几个既定决策叠加的自然产物；但"匿名函数满天飞"的可读性代价是真实的，值得在功能稳定后做一轮低风险整理。

## 闭包来源分类（当前结论）

| 类别 | 例子 | 性质 |
|---|---|---|
| 框架强制 | `Controller.Run/RunWakeup/RunCompact` 返回 `func() tea.Msg`（tea.Cmd 即函数类型） | 换任何架构都躲不掉，不属问题 |
| 解耦接缝（依赖倒置） | `rc.Segment` / `rc.AppendUser` / `rc.Emit` / `rc.Approver` | ADR-026 的既定代价：session↔middleware 不互相依赖；付在接缝上而非循环依赖上 |
| 时序/生命周期 | `c.wakeSignal`、`newSession` | 根因是 TUI 构造顺序（Model→Controller→Program 的鸡生蛋），轻微味道，非设计错误 |
| 普通回调 | `onEvent` 双转发、Queue.OnAppend | 任何语言/框架同款（`http.HandlerFunc`、`context.AfterFunc`） |

**既定取舍线**：多方法、多实现 → 接口（如 `Approver`）；单方法、单实现、只用一次 → 函数字段。这是 Go 社区惯例，无需推翻。

## 计划中的低风险改进（本轮不做）

1. **注入闭包改命名方法值**（行为零变化，可 grep/可跳转）：
   - `rc.AppendUser = s.AddUser`（去匿名包装，方法值即可）
   - `rc.Segment` 的 seed 落盘逻辑抽成 `session` 命名方法（如 `writeSegment`）后方法值赋值
   - `c.wakeSignal` 改命名方法值
   - 同理排查 rc 其他注入点
2. **闭包捕获变量按引用的语义**在关键处补注释/显式传参（goroutine 里已是显式传参，保持即可）。

## 功能完善后可一并审视的点（可选）

- TUI 构造顺序（Model→Controller→Program 谁先创建导致的 setSend/登记补偿）；是否值得收敛为一个 `Program` 装配函数。
- 每 call 新建 rc 的接缝成本复查（ADR-026 既定，仅审视不改方向）。
- 中间件测试辅助代码（rc.attrs 暂存断言数据）是否有更顺手的测试基架。

## 补充观察：装配逻辑散乱（2026-08-13 用户提出）

闭包之外，**装配/接线逻辑散落在多处**是更大的问题，同为阶段 7 整理方向。

**现状盘点（同一类接线任务分布在多处）**：

- **命令层三入口各自装配**：`cmd/harness/run.go` / `resume.go` / `repl.go` 各自做 app.Load + agent.Build + session 创建/resume + 渲染器/审批器接线，模式差异散在命令代码里。
- **rc 注入点两处分裂**：`Session.RuntimeContext()` 注入 Segment/Completions/AppendUser（会话域），`Controller.Run/RunWakeup/RunCompact` 再覆写 Approver/Emit（UI 域）——每加一个新接缝（Approver→Emit→Completions 已有三次），都要记得两处改。
- **TUI 装配序列隐式**：`RunTUI` 里 NewController → New(model) → loadSessionHistory → tea.NewProgram → setSend（生成 wakeSignal + 遍历 open 补登记）→ WaitRuns → SaveActiveState → CloseAll——启动/收尾顺序靠函数体顺序保证，没有显式的装配/拆除阶段。
- **agent 装配分散**：`agent.Build`（client+registry+chain+compactor）在 agent 包，`app.Load`（config+provider+审批默认值）在 app 包，`Controller` 又持有 agent/proj/cfg 三个来源的对象——装配根不唯一。

**优化方向（届时一并设计，本轮不做）**：

1. **收敛 Composition Root**：一个显式装配器（如 `app.Build(mode)` → `{Agent, UI, Teardown}`），命令层只声明模式（TUI/run/resume）与参数；启动/收尾各一段，拆除顺序（WaitRuns→SaveActiveState→CloseAll→CleanupBackground）与装配对称可见。
2. **rc 注入一次成型**：会话域与 UI 域注入合并为单一装配点（或分层：session 给全量默认、Controller 只覆写 UI 相关），新增接缝不再需要多处登记。
3. **阶段 5 子 agent 是下一个装配样本**：subagent = 换工具集/中间件/提示词的装配变体，届时与 Build 参数化（或新装配工厂）一起设计，避免再添一个散装入口。
4. 与闭包整理（方法值化）同轮做：命名接缝 + 收敛装配点，双管齐下才真正提升可读性。

## 触发时机

阶段 4 剩余（AGENTS.md 注入/系统提示词拼接）、阶段 5（子 agent）、阶段 6（grep/双向通信）完成后统一做；子 agent（阶段 5）会复用 completion 通道并新增一个装配入口，届时它的装配方式会提供新的取舍样本，一起看更划算。

## 附带待办（ADR-040 独立审查 03/04/05，2026-08-14 记录，低级别）

- **03**：`BackgroundCompletionMiddleware` 缺 `rc.Messages != nil` 守卫（非会话构造 rc 挂 Completions 且 drain 非空时会 panic；生产路径不受影响，防御性补一行 + 测试）。
- **04**：PID 复用窗口下前台 `notifyCompletion` 可能命中"刚死未注销"的旧后台条目，发一条错的前台通知（理论、概率可忽略；若修可考虑条目带进程身份校验或注销时序收紧）。
- **05**：Esc 打断"已抢占 cancel 但 cmd 尚未真正开跑"的唤醒 run → `handleRunDone` 写伪中断提示 user 消息污染 conversation（低；可考虑"run 未真正启动不写中断提示"的标记）。
- **06（测试缺口）**：Esc 打断唤醒 run / 非 active 会话事件 / 退出后 Send 安全 / EventNotice 在 text/json 渲染器忽略行为——补测试。
- 另议：`handleCompactDone` 末尾是否补 `MaybeWake`（compact 期间被 m.running 闸丢弃的 pending 目前留待下一次信号/用户消息，延迟不丢）。
