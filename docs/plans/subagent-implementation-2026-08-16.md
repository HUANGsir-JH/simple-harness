# 子 agent（阶段 5）实施计划

> 计划日期：2026-08-16 ｜ 状态：✅ 已实施（2026-08-16，版本 0.13.0，ADR-045）
> 前置文档：`docs/plans/subagent.md`（功能规划 14 条定案 + 2026-08-16 新增 12 条）
> 参考实现：codex（spawn_agent 异步 + mailbox 注入）/ opencode（task 前台 + task_id resume）/ AgentScope Java v2（inbox + wakeup + spawn 深度 3）/ deepseek-harness（send_message + interrupt_agent + 深度 3）

## 核心机制

**复用 completion 队列（ADR-040）**：子完成事件 Append 进父会话 Queue → 父 BackgroundCompletionMiddleware 路径 A 注入（在途采样前）/ TUI 路径 B 唤醒（空闲）——主 agent 无需 wait_agent。嵌套同构：孙完成 Append 进子的队列（逐层归并）。

## 关键决策（详见 subagent.md 新增定案）

1. spawn 纯异步（立即返回 agent_id）
2. 允许嵌套（general-purpose 有全套控制工具），深度硬编码 2
3. fork 过滤 = 仅 spawn message
4. 结果完整注入（最后一条 assistant 文本；失败/中断带已产出文本）
5. send_message 仅运行中；list_agents 配合 resume_agent（仅直属子）
6. interrupt_agent 任意后代（不能中断自己/父），中断通知带中断前结果
7. run 模式局限（父 turn 结束未完成子被清理）；e2e 走 mock 内容路由
8. /subagents 只读查看 + 实时滚动（输入禁用，/switch 返回）
9. 子 agent 带 name（可选，默认 `<type>-<短id>`）
10. 按 kind 装配 + 实例缓存共享：装配逻辑在 subagent 包内（BuildOptions +Tools/BaseInstructions/Client 可选字段），控制工具实现在 subagent 包——无接口无工厂，依赖 subagent→agent→tools 无环
11. 审批：控制工具 classControl 放行；子审批转发用户带【子 agent <id>】归属
12. 持久化分层：子会话内容（transcript）/ 血缘状态（agentstate.json）/ 完成通知（父 completions.json）全落盘；Manager 只存运行态（进程退出即清，无需持久化）

## 组件改动

- **`internal/subagent`（新包）**：`Manager`（entries/bySess/agents 缓存/subs 订阅/opts）+ `buildAgent(kind)`（agent.Build + toolset + BaseInstruction）+ 子 goroutine 生命周期（runChild/finish 三分支 + formatNotice + extractAnswer）+ `subagentApprover` + 5 控制工具（SpawnAgentTool/SendMessageTool/InterruptAgentTool/ResumeAgentTool/ListAgentsTool）。
- **`internal/agent`**：BuildOptions +`Tools`/`BaseInstructions`/`Client`（空 = 默认；agent 不感知 subagent）。
- **`internal/tools`**：`WaitTaskTool`（bgProcess 注册表轮询，done/exitCode 完成信号）；bgProcess +done/exitCode + markDone（notifyCompletion/handleKill/CleanupBackground 置位）。
- **`internal/session`**：`CreateIn(dir, st)`（Create 重构委托）+ `ResumeAt(dir)`（resume 续接原 jsonl 段）+ `NewID(prefix)`（sub- 前缀）。
- **`internal/agentstate`**：血缘字段 `ParentID/AgentType/Depth/Status`（omitempty 兼容）+ SetSubagent/SetStatus。
- **`internal/middleware`**：`ApprovalRequest`/`AskRequest` +`AgentID`；`impl` policy +classControl（spawn/send/interrupt/resume/list/wait_task 全模式放行）。
- **`internal/app`**：buildAgent 返回 (agent, Manager)；Teardown +`Manager.Shutdown()`；runTUI 传 Manager。
- **`internal/ui/tui`**：`/subagents` 弹窗 + 只读查看（Controller.ViewSubagent/ExitSubagentView + 订阅桥 + viewOwned Close；运行中的子复用 Manager 会话实例避免双 writer）；Model.viewingSubagent（输入禁用 + composer 提示行）；AssembleWith 全参版本。

## 测试

- subagent 包 11 项（生命周期/深度/通知三分支/实例缓存/退订/审批包装，-race）
- session CreateIn/ResumeAt；wait_task 4 项；TUI 查看/输入禁用；policy classControl
- `TestSubagentE2E`（进程外 mock 内容路由确定性："已完成。结果："判定——spawn 结果与 update_todo 描述均含"系统通知"/"已完成"字样故需精确格式）
- 全量 `go build/vet/test ./... -count=1` 全绿（19 包含 e2e + -race）；版本 0.13.0

## 实施顺序（已按此执行）

1. agentstate 血缘字段 + session CreateIn/ResumeAt/NewID
2. subagent 包（Manager + 装配 + 控制工具 + 通知）
3. agent.BuildOptions 参数化 + wait_task
4. middleware 审批归属
5. app 装配 + Teardown
6. TUI /subagents + 只读查看
7. e2e + 文档（ADR-045 / 计划现状 / PROGRESS / TASKS）+ 版本 0.13.0 + go install
