# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目概览

一个参照 OpenAI Codex CLI（`../codex/codex-rs`，Rust 源码）+ AgentScope Java v2 架构、用 Go 构建的**可真实使用**的极简 agent harness（命令行形式）。定位为通用框架，未来可被其它项目（如 resume-agent）引用。

**当前状态**：阶段 1（骨架+统一消息模型+Provider+最小 loop）✅ 2026-08-04；阶段 2（工具系统 + 并发执行 + 终端渲染 + middleware 骨架 + 交互式 CLI）✅ 2026-08-07；阶段 2.5（Workspace + AgentState + 会话落盘/resume）✅ 2026-08-08；**架构重构（ADR-026 无状态 agent + 运行时切换 + 配置统一 init）**✅ 2026-08-08；**todo 工具（update_todo 全量替换 + 跨轮偏离提醒，ADR-027）**✅ 2026-08-08；**工具结果截断中间件 + Esc 用户中断 + shell 长任务缓解 + state.CWD 修正（ADR-028）**✅ 2026-08-09；**工具审批（三档权限 + onActing middleware + 会话级记忆，ADR-029）**✅ 2026-08-09。**规划文档 `IMPLEMENTATION_PLAN.md` 是权威来源**（含已确认决策表与实施阶段状态，**已完成/待办严格区分**）；架构决策在 `docs/tasks/DECISIONS.md`（ADR-021~029 为核心）；任务跟踪在 `docs/tasks/{TASKS,PROGRESS}.md`。实现前先读 `IMPLEMENTATION_PLAN.md`。

**实施顺序**：~~阶段 3 审批~~ → **todo 工具**（挂 state，已完）→ **工具结果落盘/中断/shell 缓解**（ADR-028，已完）→ **工具审批（ADR-029，已完）**→ 阶段 4 剩余（AGENTS.md 注入/压缩）→ 阶段 5（子 agent，并行已由无状态 agent 架构支撑）。

## 常用命令

```bash
# 构建到当前目录 ./harness[.exe]（gitignore 已忽略）
go build ./cmd/harness

# ★ 更新全局 harness 命令（go install 到 C:\Users\86131\go\bin，已在 PATH）
go install ./cmd/harness

# 运行（go run 直接跑最新源码）
go run ./cmd/harness version

# 测试（全部 / 单包 / 单用例）
go test ./...
go test ./internal/session/ -v
go test ./internal/messages/ -run TestMessageJSONL

# e2e（进程外，termtest + mock HTTP；用 HARNESS_HOME 隔离 workspace）
go test ./internal/e2e/ -count=1
```

REPL（`harness` 无子命令进入）命令：`/switch <id>|--last` 切换会话（进程内 resume）、`/model <name>` 切模型、`/effort <low|high|max>` 切推理档位、`/permission <readonly|acceptedit|bypass>` 切审批模式（会话级，落盘 AgentState）。**Esc/Ctrl+C 中断当前回合**（raw mode 事件循环，中断提示落盘，resume 可见，ADR-028）。**工具审批**（ADR-029）：config `approval.mode` 为默认权限（会话创建时播种进 AgentState.Permission.Mode）；危险操作按模式询问，`y` 允许本次 / `s` 本会话记住（落盘 AgentState）/ `n` 拒绝（回填模型换思路）；非 TTY 自动拒绝。

## 代码架构

```
cmd/harness/          # CLI 应用层：main（dispatch）/ runtime（统一配置 init）/ build（共享无状态 agent）/ run+resume+repl+session_mgr（子命令与 REPL 编排）
internal/
  ui/                 # ★ 用户交互层：终端输入（raw mode 单一读方事件循环）+ 渲染（text/json）+ 审批交互（ChannelApprover/ApprovalPrompt）
  agent/              # ★ 无状态 ReAct loop（采样→工具→回填，消息序列经 rc.Messages；ADR-026）+ 回合级事件（turn_done 为测试锚点）
  middleware/         # ★ 框架 core：6 hook 扩展机制（onAgent/onReasoning/onToolCall/onActing/onModelCall onion + onSystemPrompt 管道）+ RuntimeContext（承载会话）+ 契约（Approver/ApprovalRequest/DeniedError，ADR-029）
  middleware/impl/    # ★ 内置中间件实现：工具说明注入 / 会话状态 load-save / todo 跨轮提醒（ADR-027）/ 工具结果截断 head/tail + evictions 落盘（ADR-028）/ 工具审批三档 + 黑白名单 + 会话记忆（ADR-029）
  provider/           # 单 anthropic wire（ADR-022）+ 块事件适配 + per-call 覆盖（Request.Model/ThinkingEnabled/Effort，ADR-026）
  messages/           # 统一 Message 模型（含 Thinking）+ JSON 序列化
  tools/              # Tool 接口（Handle 带 rc）+ 注册表 + 7 内置工具（含 update_todo，ADR-027）
  agentstate/         # AgentState 快照（模型/thinking 档位/todo/权限/plan/摘要）+ 原子落盘
  session/            # workspace 项目分桶 + 块级 transcript 异步 writer + resume
  e2e/                # 进程外端到端测试（termtest）
  # 规划中（未实现）：compact（压缩）/ agentsmd（AGENTS.md 注入）/ hooks（子进程，远期）；TUI（internal/ui 扩展，规划）
```

## 核心架构约束（来自 ADR，见 docs/tasks/DECISIONS.md）

这些是已定案的设计，实现时**遵循而非重新讨论**：

1. **统一消息模型 + 事件分层**：核心层只操作统一 `Message`（role/content/thinking/tool_calls/tool_results），provider 适配层负责 ↔ 原生格式。事件分层：provider 采样级（text/thinking delta + **块完成** thinking_done/text_done + tool_call）+ agent 回合级（turn_start/turn_done 等，**带 MsgID** 关联块归属）。
2. **Provider 单 anthropic wire**（ADR-022）：多后端 = 多 anthropic 兼容端点（base_url 覆盖即可），无多 wire 抽象。
3. **进程内 middleware**（ADR-021/024/025/026/027/028）：6 hook（onion 前四 + onSystemPrompt 管道），贯穿 `ctx` + `*RuntimeContext`。**注入机制**：rc 承载会话（`Messages/State/StatePath/Model/ThinkingEffort`，`Session.RuntimeContext()` 每轮新建）；`impl.SessionMiddleware` **无状态**挂 onAgent（从 rc.StatePath 读写，共享链可并发）；**工具 `Handle(ctx, rc, callID, args)` 带 rc**（todo 经 rc.State 读写）；`TodoReminderMiddleware` 挂 onReasoning（todo 非空且模型连续 ≥10 次 model call 未更新 → 请求消息尾部注入提醒临时副本，不写 conversation）；`ToolOutputMiddleware` 挂 onToolCall（after 改写本批 tool_result：超 20K 截断 head/tail 各 10K + 落盘 evictions/ + 路径提示；transcript 记完整、conversation 记 preview，ADR-028）；`ApprovalMiddleware` 挂 onActing（before 审批：三档模式 + shell 黑白名单，决策纯函数 `Decide`；模式 = 会话 `AgentState.Permission.Mode`（config 播种 + /permission 切换）；Ask 经 `rc.Approver`（CLI 注入 channelApprover，单一读方 channel 协调）询问 y/s/n；拒绝返回 `DeniedError` 回填模型；会话级记忆 `AgentState.Permission.Approved`，ADR-029）。
4. **错误二分类 + 审批拒绝**：工具错误 `RespondToModel`（结果回填、循环继续）/ `Fatal`（终止 turn）。**审批拒绝 = 独立 `middleware.DeniedError`**（非工具错误）：agent 调用层捕获后作为失败结果回填、**不取消整批**、循环继续（ADR-029，拒绝 ≠ Fatal）。
5. **并行工具**：errgroup 并发执行全部 tool_call，结果按 call_id 合并成**一条** tool_result 消息回填（anthropic 紧邻要求，ADR-024）。
6. **会话双轨**（ADR-025 项目分桶）：`~/.harness/workspaces/<项目转义>/<session-id>/{historys, agentstate.json, plans, evictions}`；transcript = **块级事件 + 异步 writer**（单 goroutine FIFO + ordinal，压缩切新文件 `NewSegment`）；AgentState = todo/权限/plan 指针/摘要（含 `CWD` = **会话启动目录**，ADR-028）；evictions/ = 超长工具结果落盘（模型 read_file/grep 读全量）。resume 只读最大序号文件。
7. **thinking 存但不重放**（ADR-025）：`Message.Thinking` 存审计，provider 重放 assistant 时忽略（免 anthropic 格式适配）。
8. **UI 抽象**：`output` 接口（text 渲染器 + `--json` JSONL 事件）；事件回调双转发（渲染 + session 落盘）。
9. **子 agent = 独立 session**（远期）：fork 只继承 user 消息 + 最终答案。并行已由无状态 agent + 共享 chain 并发安全支撑（ADR-026）。
10. **无状态 agent + 运行时切换**（ADR-026）：agent 不持有会话，`Run(ctx, rc, onEvent)` 消息序列经 `rc.Messages`；**每 Run 新建 rc**，切换会话 = 换 active（REPL `/switch`）、并行 = 每 goroutine 一个 rc（共享 agent/chain 并发安全）。模型/thinking 档位 per-call 经 `Request.Model/ThinkingEnabled/ThinkingEffort` 覆盖（nil/空 = client 默认），会话级持久化在 AgentState（resume 恢复）；`/model`、`/effort` 运行时切换。配置统一 `defaultApp()` 惰性单例（`provider.LoadConfig` + `App{Config, Resolved}`）。

## 工作流约定

- **沟通用中文**；提交信息用 conventional commits（`chore:`/`feat:`/`docs:` 等），**git 身份必须用用户真实身份**（HUANGsir-JH <huangsirjh2005@gmail.com>）。
- **任务跟踪**：每阶段在 `docs/tasks/TASKS.md` 建条目；每工作单元完成后 `PROGRESS.md` 记一笔（含日期）；重要设计决策写 `DECISIONS.md`（ADR）；阶段完成同步更新 `IMPLEMENTATION_PLAN.md` 状态。
- 时间戳统一 `YYYY-MM-DD`；状态变更必须带日期。
- **测试隔离**：涉及 workspace 的测试/进程用 `HARNESS_HOME=<临时目录>`，避免污染 `~/.harness/`。
- 真实 API key 只在 `config.local.yaml`（gitignored），**永不写入对话/记忆/提交明文**。
