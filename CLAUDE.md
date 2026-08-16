# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目概览

一个参照 OpenAI Codex CLI（`../codex/codex-rs`，Rust 源码）+ AgentScope Java v2 架构、用 Go 构建的**可真实使用**的极简 agent harness（命令行形式）。定位为通用框架，未来可被其它项目（如 resume-agent）引用。

**当前状态**：阶段 1（骨架+统一消息模型+Provider+最小 loop）✅ 2026-08-04；阶段 2（工具系统 + 并发执行 + 终端渲染 + middleware 骨架 + 交互式 CLI）✅ 2026-08-07；阶段 2.5（Workspace + AgentState + 会话落盘/resume）✅ 2026-08-08；**架构重构（ADR-026 无状态 agent + 运行时切换 + 配置统一 init）**✅ 2026-08-08；**todo 工具（update_todo 全量替换 + 跨轮偏离提醒，ADR-027）**✅ 2026-08-08；**工具结果截断中间件 + Esc 用户中断 + shell 长任务缓解 + state.CWD 修正（ADR-028）**✅ 2026-08-09；**工具审批（三档权限 + onActing middleware + 会话级记忆，ADR-029）**✅ 2026-08-09；**TUI（bubbletea 全屏交互 UI 替代 REPL，ADR-030）**🔨 2026-08-09；**用量展示（ADR-037 第一段，footer ctx + /usage）✅ 2026-08-12**；**thinking 完整回传（ADR-025 修订，ADR-037 第二段）✅ 2026-08-12**；**LLM 摘要压缩（ADR-037 第三段，compact 包 + /compact）✅ 2026-08-12（版本 0.8.0）**；**阶段 5 子 agent（ADR-045，版本 0.13.0，2026-08-16：`internal/subagent` 包 + spawn 纯异步 + completion 队列复用 + 按类型装配）**。**规划文档 `IMPLEMENTATION_PLAN.md` 是权威来源**（含已确认决策表与实施阶段状态，**已完成/待办严格区分**）；架构决策在 `docs/tasks/DECISIONS.md`（ADR-021~045 为核心）；任务跟踪在 `docs/tasks/{TASKS,PROGRESS}.md`。实现前先读 `IMPLEMENTATION_PLAN.md`。

**实施顺序**：~~阶段 3 审批~~ → **todo 工具**（挂 state，已完）→ **工具结果落盘/中断/shell 缓解**（ADR-028，已完）→ **工具审批（ADR-029，已完）**→ **TUI（bubbletea 替代 REPL，ADR-030，进行中）**→ **用量展示 + thinking 回传 + LLM 摘要压缩（ADR-037，已完 0.8.0）**→ **阶段 4 剩余（AGENTS.md 注入 + 基础提示词增强，ADR-043，已完 2026-08-15）** → **全局 Skill 支持（ADR-044，已完 0.12.0 2026-08-15：`~/.harness/skills/` SKILL.md 目录 + 系统提示目录注入 + `skill` 工具渐进式披露；`agent.Build` 签名重构为 BuildOptions）** → **阶段 5 子 agent（ADR-045，已完 0.13.0 2026-08-16：spawn_agent 纯异步 + completion 队列复用 + 嵌套深度 2 + 按类型装配 + /subagents 只读查看）** → 阶段 6 剩余（grep/双向通信）。

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

交互模式（`harness` 无子命令进 TUI，bubbletea 全屏）命令：`/switch` `/model` `/effort` `/thinking` `/permission` 弹窗选择器（选项实时从配置获取，右侧显示说明）、`/rename <名称>`（会话改名）、`/subagents`（子 agent 列表 → 只读查看，/switch 返回）、`/help`、`/exit`（退出仅此命令）。**会话懒加载**（2026-08-11）：进入不创建 session，首条消息/状态命令才建（避免 /exit 或 /switch 残留空会话）；**首消息自动命名**（codex first_user_message 同款，前 40 字，/switch 弹窗与 header 展示 name）。**Esc 中断当前回合**（中断提示落盘，resume 可见，ADR-028）；**Ctrl+C 复制**（非中断非退出）；run 期间输入进**队列**（输入框上方队列条，回合完成逐条连跑）。`run`（单轮流式非交互）保留。**thinking 默认开启**（ADR-034，2026-08-10 删配置 enabled 项）：开关是会话级偏好，`/thinking` 或 `--thinking/--no-thinking` 切换，持久化 AgentState，resume 恢复。**工具审批**（ADR-029）：config `approval.mode` 为默认权限（会话创建时播种进 AgentState.Permission.Mode）；危险操作按模式询问，`y` 允许本次 / `s` 本会话记住（落盘 AgentState）/ `n` 拒绝（回填模型换思路）；非 TTY 自动拒绝。

## 代码架构

```
cmd/harness/          # CLI 应用层：main（dispatch）/ run+resume+repl（命令编排，经 app.Load + agent.Build 装配，config/app 下沉 internal）
internal/
  config/             # ★ 配置域（最底层，只依赖 yaml+stdlib）：Config/ProviderSpec/Model/ProviderConfig 类型 + YAML 加载 + 解析 + 校验（provider 拆出，2026-08-09）
  app/                # ★ 进程级装配根：App{Config, Provider} 惰性单例 + flags 校验 + 审批默认模式（未来扩展 client/agent/subagent 字段）
  ui/                 # ★ 用户交互层：渲染（text/json，run 单轮用）+ 审批解析（ParseApprovalDecision）+ **tui/**（bubbletea 全屏交互 UI 替代 REPL：Model/View/Update 纯函数 + 事件桥 + 审批桥，ADR-030）
  agent/              # ★ 无状态 ReAct loop（采样→工具→回填，消息序列经 rc.Messages；ADR-026）+ 回合级事件（turn_done 为测试锚点）+ Build 装配工厂（client+工具+标准中间件链）
  middleware/         # ★ 框架 core：6 hook 扩展机制（onAgent/onReasoning/onToolCall/onActing/onModelCall onion + onSystemPrompt 管道）+ RuntimeContext（承载会话）+ 契约（Approver/ApprovalRequest/DeniedError，ADR-029）
  middleware/impl/    # ★ 内置中间件实现：基础提示词注入（链首，含 {{cwd}}/{{model}}）/ AGENTS.md 注入（AgentsMd，ADR-043）/ 技能目录注入（SkillsCatalog，ADR-044）/ 工具说明注入 / 会话状态 load-save / todo 跨轮提醒（ADR-027）/ 工具结果截断 head/tail + evictions 落盘（ADR-028）/ 工具审批三档 + 黑白名单 + 会话记忆（ADR-029）
  provider/           # 单 anthropic wire（ADR-022）+ 块事件适配 + per-call 覆盖（Request.Model/ThinkingEnabled/Effort，ADR-026）；配置已拆至 config
  messages/           # 统一 Message 模型（含 Thinking）+ JSON 序列化
  tools/              # Tool 接口（Handle 带 rc）+ 注册表 + 12 内置工具（含 update_todo ADR-027、skill ADR-044）
  agentstate/         # AgentState 快照（模型/thinking 档位/todo/权限/plan/摘要/用量）+ 原子落盘
  compact/            # ★ 上下文压缩（ADR-037 第三段）：EstimateTokens/ShouldCompact/Summarizer/Runner.Run
  agentsmd/           # ★ AGENTS.md/CLAUDE.md 发现与拼接（ADR-043）：.git 项目根向上搜索 + 200KB 截断 + 读失败非致命
  skills/             # ★ 全局技能（ADR-044）：SKILL.md 目录包/平铺发现 + frontmatter 校验 + 200KB 预算 + <skill_content> 渲染（叶子包，只依赖 stdlib+yaml）
  session/            # workspace 项目分桶 + 块级 transcript 异步 writer + resume
  subagent/           # ★ 子 agent（阶段 5，ADR-045）：Manager 注册表 + 5 控制工具（spawn/send/interrupt/resume/list）+ 按类型装配（直接调 agent.Build，无接口无工厂）+ 完成通知复用 completion 队列
  e2e/                # 进程外端到端测试（termtest）
  # 规划中（未实现）：hooks（子进程，远期）
```

## 核心架构约束（来自 ADR，见 docs/tasks/DECISIONS.md）

这些是已定案的设计，实现时**遵循而非重新讨论**：

1. **统一消息模型 + 事件分层**：核心层只操作统一 `Message`（role/content/thinking/tool_calls/tool_results），provider 适配层负责 ↔ 原生格式。事件分层：provider 采样级（text/thinking delta + **块完成** thinking_done/text_done + tool_call）+ agent 回合级（turn_start/turn_done 等，**带 MsgID** 关联块归属）。
2. **Provider 单 anthropic wire**（ADR-022）：多后端 = 多 anthropic 兼容端点（base_url 覆盖即可），无多 wire 抽象。
3. **进程内 middleware**（ADR-021/024/025/026/027/028）：6 hook（onion 前四 + onSystemPrompt 管道），贯穿 `ctx` + `*RuntimeContext`。**注入机制**：rc 承载会话（`Messages/SystemPrompt/State/StatePath/Model/ThinkingEffort`，`Session.RuntimeContext()` 每轮新建；系统提示经 onSystemPrompt 管道组合后回写 rc.SystemPrompt——内容通道分类：对话历史=Messages / 稳定配置=系统提示（基础提示词=链首 BaseInstructionsMiddleware）/ 工具定义=toolspec，ADR-039）；`impl.SessionMiddleware` **无状态**挂 onAgent（从 rc.StatePath 读写，共享链可并发）；**工具 `Handle(ctx, rc, callID, args)` 带 rc**（todo 经 rc.State 读写）；`TodoReminderMiddleware` 挂 onReasoning（todo 非空且模型连续 ≥10 次 model call 未更新 → 请求消息尾部注入提醒临时副本，不写 conversation）；`ToolOutputMiddleware` 挂 onToolCall（after 改写本批 tool_result：超 20K 截断 head/tail 各 10K + 落盘 evictions/ + 路径提示；transcript 记完整、conversation 记 preview，ADR-028）；`ApprovalMiddleware` 挂 onActing（before 审批：三档模式 + shell 黑白名单，决策纯函数 `Decide`；模式 = 会话 `AgentState.Permission.Mode`（config 播种 + /permission 切换）；Ask 经 `rc.Approver`（CLI 注入 channelApprover，单一读方 channel 协调）询问 y/s/n；拒绝返回 `DeniedError` 回填模型；会话级记忆 `AgentState.Permission.Approved`，ADR-029）。
4. **错误二分类 + 审批拒绝**：工具错误 `RespondToModel`（结果回填、循环继续）/ `Fatal`（终止 turn）。**审批拒绝 = 独立 `middleware.DeniedError`**（非工具错误）：agent 调用层捕获后作为失败结果回填、**不取消整批**、循环继续（ADR-029，拒绝 ≠ Fatal）。
5. **并行工具**：errgroup 并发执行全部 tool_call，结果按 call_id 合并成**一条** tool_result 消息回填（anthropic 紧邻要求，ADR-024）。
6. **会话双轨**（ADR-025 项目分桶）：`~/.harness/workspaces/<项目转义>/<session-id>/{historys, agentstate.json, plans, evictions}`；transcript = **块级事件 + 异步 writer**（单 goroutine FIFO + ordinal，压缩切新文件 `NewSegment`）；AgentState = todo/权限/plan 指针/摘要（含 `CWD` = **会话启动目录**，ADR-028）；evictions/ = 超长工具结果落盘（模型 read_file/grep 读全量）。resume 只读最大序号文件。
7. **thinking 完整回传**（ADR-025 修订，2026-08-12）：`Message.Thinking` + `Message.ThinkingSignature`（数字签名）存审计；provider 重放 assistant 时**仅签名非空才重放** `ThinkingBlockParam`（严格端点校验签名；DeepSeek 兼容端点恒返回签名，实测回传 200）。thinking-only assistant 带签名不再跳过。
8. **UI 抽象**：`output` 接口（text 渲染器 + `--json` JSONL 事件）；事件回调双转发（渲染 + session 落盘）。
9. **子 agent = 独立 session**（远期）：fork 只继承 user 消息 + 最终答案。并行已由无状态 agent + 共享 chain 并发安全支撑（ADR-026）。
10. **无状态 agent + 运行时切换**（ADR-026）：agent 不持有会话，`Run(ctx, rc, onEvent)` 消息序列经 `rc.Messages`；**每 Run 新建 rc**，切换会话 = 换 active（REPL `/switch`）、并行 = 每 goroutine 一个 rc（共享 agent/chain 并发安全）。模型/thinking 档位 per-call 经 `Request.Model/ThinkingEnabled/ThinkingEffort` 覆盖（nil/空 = client 默认），会话级持久化在 AgentState（resume 恢复）；`/model`、`/effort` 运行时切换。配置统一 `app.Load()` 惰性单例（`config.LoadConfig` + `config.Resolve` → `app.App{Config, Provider}`；agent 经 `agent.Build(agent.BuildOptions{...})` 装配——Provider/DefaultMode/GlobalAgentsMD/GlobalSkillsDir，ADR-044 起结构体收拢）。
11. **TUI 交互**（ADR-030）：`internal/ui/tui`（bubbletea elm：Model/Update/View 纯函数可测）是唯一交互入口（`repl()` 留薄壳，REPL 已删）；agent 事件经 `onEvent → program.Send` 桥接（agent 核心零冲击，ADR-026 前提）；审批 `rc.Approver` 换 `TUIApprover`（接口不变，run 继续用 channelApprover）；**队列 = 用户输入**（prompt + `/` 命令统一排队，消费按前缀分派命令/发 agent）；命令落盘 transcript `command` 行但不进 conversation（模型不可见）；工具块按工具分派折叠展示；无 emoji 风格（`[OK]/[ERR]/[RUN]` + 颜色）；测试单测为主 + e2e 全面。

## 工作流约定

- **沟通用中文**；提交信息用 conventional commits（`chore:`/`feat:`/`docs:` 等），**git 身份必须用用户真实身份**（HUANGsir-JH <huangsirjh2005@gmail.com>）。
- **任务跟踪**：每阶段在 `docs/tasks/TASKS.md` 建条目；每工作单元完成后 `PROGRESS.md` 记一笔（含日期）；重要设计决策写 `DECISIONS.md`（ADR）；阶段完成同步更新 `IMPLEMENTATION_PLAN.md` 状态。
- 时间戳统一 `YYYY-MM-DD`；状态变更必须带日期。
- **测试隔离**：涉及 workspace 的测试/进程用 `HARNESS_HOME=<临时目录>`，避免污染 `~/.harness/`。
- 真实 API key 只在 `config.local.yaml`（gitignored），**永不写入对话/记忆/提交明文**。
- 当用户需要对方案设计进行讨论的时候，对具体功能了解参考源实现，并且针对歧义点或者实现思路等不停地和用户讨论，直到达成一致，形成最终的方案设计。


## 五个参考源
1. D:\agent-project\harness\codex：codex开源仓库
2. D:\agent-project\harness\opencode：opencode开源仓库
3. D:\agent-project\harness\simple-harness\agent-scope-llms.txt：AgentScope LLMs 相关信息
4. D:\agent-project\harness\agentscope-java： AgentScope Java v2 架构源码
5. D:\agent-project\harness\deepseek-harness：DeepSeek harness 源码