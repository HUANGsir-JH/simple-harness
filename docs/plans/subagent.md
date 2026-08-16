## subagent实现功能规划

> 2026-08-16 更新：原标注"待讨论"的点已逐项定案（用户决策，见各条 ✅）；具体实现方案另见实施计划与 ADR，不写入本文。

1. 目标：实现一个可被主 agent 调用的子 agent，支持并行执行和独立状态管理。
2. 子agent完整生命周期的控制 ✅
   - 创建：主 agent 通过 `spawn_agent` 工具创建子 agent 实例，传初始化参数（message 任务；agent_type / model / thinking_effort 可选覆盖）。
   - 执行：子 agent 以后台 goroutine 独立执行任务，支持并行处理（不设信号量，靠嵌套深度上限约束规模）。
   - 状态管理：子 agent 维护自己的状态（独立 session：会话信息、工具使用记录、AgentState 等）。
   - 销毁：单 turn 结束返回结果 → goroutine 自然结束（session 已完整落盘）；`interrupt_agent` = 中断即销毁；进程退出统一清理。
3. subagent一样是无状态引擎。✅（复用 ADR-026：无状态 agent + 每子 agent 独立 RuntimeContext）
4. subagent的会话记录需要落盘 ✅ 定案：**嵌套在父会话目录下**——`<父会话目录>/subagents/<子id>/{historys, agentstate.json, plans, evictions}`；子 agentstate 记录 ParentID 等血缘字段。
5. 支持主agent消息通知subagent，subagent可以向主agent发送消息 ✅ 定案：**复用现有 completion 队列**（澄清：不新增队列，每个会话本就自带一个）：子 agent 完成 → 最终答案自动注入父对话（子→主）；`send_message` 单向主→子（消息进子会话队列，子下一轮采样注入）。子 agent 运行中主动发多条中途消息不做。
6. 支持subagent的中断，resume ✅ 定案：`interrupt_agent` = 停止子 agent 本次 turn（Esc 同款语义）；`resume_agent(id, message?)` = 磁盘加载已落盘的子会话继续新任务（仅直属子）；父 Esc 不级联子 agent。
7. subagent的实现方式 ✅ 定案：**goroutine + 共享无状态 agent**（不用 fork 进程 / 线程池）；不设信号量；**允许嵌套**，深度上限（config `subagents.max_depth`，默认 2）。
8. subagent的工具系统 ✅ 定案：**与主 agent 工具集不一致**——内置多个 subagent 类型、按类型配置工具集。v1 初版两个类型：`general-purpose`（内置工具 − ask_user + subagent 控制工具 + `wait_task`）、`explore`（只读：read_file / list_dir / glob / skill）。嵌套 spawn 受深度上限约束。
9. subagent的权限系统 ✅ 定案：**继承父 agent 的 Permission**（Mode + Approved 记忆快照播种进子 AgentState）；**审批不忽略**——审批/提问请求转发给用户（带"【子 agent <id>】"归属标识），不自动静默拒绝。
10. subagent的配置系统 ✅ 定案：**共享进程级配置**（config / provider client 同源）；spawn 可选 model / thinking_effort 覆盖（默认继承父）。
11. subagent的状态要挂在哪里？✅ 定案：**运行态**（pending / running / completed / failed）挂**进程内 Manager 注册表**（进程内子 agent 管理器）；**持久态**在子会话目录（见第 4 条）。
12. tui上的subagent管理界面 ✅ 定案：**轻量**——spawn/wait/list 工具块展示 + 完成系统行；新增 `/subagents` 命令列出子 agent；**支持切换查看子 agent 会话**（参考 codex / opencode / dsh 的 subagent 查看能力）。
13. 是否支持用户主动给subagent发送消息？✅ 定案：v1 不做（用户经主 agent 转达）。
14. 如何处理subagent的工具调用？子agent允许停下本次agent run来等待shell后台完成提醒吗？✅ 定案：子 agent **允许 background shell，但不允许结束本次 turn 等待**；提供子 agent 专属 `wait_task(pid, timeout_ms)` 轮询工具（阻塞本轮至进程退出或超时，返回退出码 + 日志尾部）；不做子 agent 唤醒循环。

---

## 2026-08-16 实现讨论新增定案（用户逐点拍板，实施见 ADR-045）

1. **spawn 纯异步**：立即返回 agent_id，完成自动注入父对话（completion 队列复用 + TUI 唤醒），无 wait_agent 工具。
2. **允许嵌套**：general-purpose 有全套控制工具可再 spawn；深度上限**硬编码 2**（不做 config 段）。
3. **fork 过滤**：子会话起点 = 仅 spawn 的 message（不继承父历史）。
4. **结果不截断**：完整注入（最后一条 assistant 文本）；失败/中断通知带已产出文本。
5. **send_message 仅运行中**；已结束报错引导 resume_agent。
6. **加 list_agents 工具**（配合 resume_agent）。
7. **interrupt_agent 任意后代**（不能中断自己/父）；中断通知父 + 带中断前结果。
8. **run 模式记录局限**（父 turn 结束未完成的子被清理）；e2e 走 mock 内容路由。
9. **/subagents 只读查看 + 实时滚动**（输入禁用，/switch 返回；运行中的子复用 Manager 会话实例）。
10. **子 agent 带 name**（可选，默认 `<type>-<短id>`；通知/TUI/list 显示）。
11. **按 kind 装配 + 实例缓存共享**：装配逻辑在 subagent 包内——初版经 BuildOptions 调 agent.Build；**2026-08-16 修订为 `buildSubagent` 完全独立**（不复用 agent.Build，agent.Build 回归纯主装配）；提示词 = general-purpose uniform 主 persona + 独立 `DelegationInstructionsMiddleware` 委托段（deepseek 同款）/ explore 专属简短提示词（opencode 同款）；控制工具实现在 subagent 包（无接口无工厂，依赖无环）。
12. **审批**：控制工具归 classControl 放行；子审批转发用户带【子 agent <id>】归属标识。
