# 任务列表

> 阶段定义见 `IMPLEMENTATION_PLAN.md`。状态：`未开始 | 进行中 | 已完成`

## 阶段 1：骨架 + 统一消息模型 + Provider + 最小 loop

- **目标**：项目初始化、`messages` 包（统一 Message 模型 + JSONL 序列化）、`provider` 包（Provider/LLMClient 接口 + OpenAI/Anthropic 适配 + 重试）、最小 agent loop（单次采样，无工具）
- **成功标准**：`harness run "你好"` 能从真实 API 拿到流式回复
- **测试**：provider 单测（mock HTTP）；loop 单测（mock LLMClient）
- **状态**：✅ 已完成（2026-08-04）

### 任务单元

| # | 单元 | 状态 |
|---|---|---|
| 1.1 | 项目初始化（go.mod + cmd/harness + git） | ✅ 已完成 2026-08-04 |
| 1.2 | 文档跟踪目录（docs/tasks） | ✅ 已完成 2026-08-04 |
| 1.3 | `messages` 包：统一 Message 模型 + JSONL 序列化 | ✅ 已完成 2026-08-04 |
| 1.4 | `provider` 包：Provider/LLMClient 接口 + OpenAI 适配 | ✅ 已完成 2026-08-04 |
| 1.5 | `provider` 包：Anthropic 适配 | ✅ 已完成 2026-08-04 |
| 1.6 | 错误分类 + 重试（指数退避 + 抖动） | ✅ 已完成 2026-08-04（SDK 内置，无需自定义） |
| 1.7 | 最小 agent loop（单次采样）+ CLI run 子命令 | ✅ 已完成 2026-08-04 |
| 1.8 | 真实 API 端到端验证（DeepSeek 兼容端点） | ✅ 已完成 2026-08-04 |
| 1.9 | thinking 推理模式（模型级配置 + 双 wire + CLI 运行时覆盖） | ✅ 已完成 2026-08-06 |

## 阶段 2：工具系统 + 并发执行 + 终端渲染

- **目标**：**移除 openai wire**（删 openai.go/相关测试，config/枚举/校验调整，provider 只留 anthropic，真实 API 复验 deepseek-claude）；`tools` 包（Tool 接口 + 注册表 + 错误二分类）、内置工具（read_file/list_dir/glob/shell_command/apply_patch）、并行执行 + call_id 回填、**完整简单的终端渲染**（文本流式 + 工具调用展示）；**agent 重构为纯 ReAct loop + middleware 挂载点骨架**（onActing 即预留的工具权限扩展点）
- **成功标准**：`harness run "读取当前目录文件列表并告诉我"` 能触发工具调用并正确回填；**多轮工具调用闭环在终端渲染下完整可跑 —— 阶段二完成 = 一个可用的简单终端 CLI agent 循环**
- **测试**：进程内 FakeClient 单测（主力）+ **termtest** 进程外端到端（LLM 端点 mock HTTP server，确定性）+ **CI 末尾真实 API 冒烟 2-3 条**（宽松断言，验证链路）+ `turn_done` 回合边界事件作断言锚点（详见 IMPLEMENTATION_PLAN 阶段 2）
- **状态**：进行中（2026-08-07 移除 openai wire ✅；其余工具系统/渲染/middleware 骨架未开始）

## todo 工具阶段（阶段 2 之后单开，编号待定）

- **目标**：todo 工具（任务清单拆解/跟踪）
- **状态**：未开始（2026-08-06 用户指定单独做，不进工具系统阶段）

## 阶段 3：权限/审批（三档）+ Hooks + 错误重试

- **目标**：三档权限（`readonly` / `acceptedit` / `bypass`）**以 onActing middleware 挂载** + 黑白名单 + TTY 交互 + allowlist（规则匹配引擎保留扩展点，复杂匹配不强做）；错误重试完善
- **成功标准**：危险命令按权限档位放行/确认/拒绝；middleware 链能拦截工具执行；429 重试生效
- **状态**：未开始（2026-08-06 调整为三档权限；2026-08-07 确认通过 middleware 挂载）

## 阶段 4：Workspace（~/.harness/）+ 会话 + 系统提示词拼接 + 压缩

- **目标**：**`~/.harness/` 统一 workspace**（sessions/快照、subagents/*.md 预留、tools.json、memory/）；`session` 包（JSONL 消息流 + **轻量 AgentState 快照** + resume，落 workspace）；`agentsmd` 包（**作为 onSystemPrompt middleware** 注入 + 系统提示词动态拼接，AGENTS.md 项目级向上搜索保留）；`compact` 包（TokenBudget v1 + 摘要式 + **大工具结果 eviction**，作为 onReasoning middleware，**不做 overflow 安全网**）
- **成功标准**：`harness resume --last` 能完整恢复（含权限/todo 等非消息状态）；AGENTS.md 注入生效；系统提示词随上下文动态组装；长会话自动压缩；超大工具结果落盘 + read_file 指针
- **状态**：未开始（2026-08-06 增补系统提示词动态拼接；2026-08-07 确认 workspace/compaction 范围）

## 阶段 5：子 Agent（内置 + 并行 + 状态）+ CLI 完善 + 文档

- **目标**：**内置几个子 agent**（general-purpose 等）+ **并行执行** + **状态跟踪**（pending/running/completed）+ `send_message` 单向（fork 过滤保留）；自定义声明式（subagents/*.md）预留扩展点；Renderer 接口完成（simple 渲染器 + --json 模式）、config 包（YAML 加载）、CLI 子命令完善、docs/ 设计文档
- **成功标准**：`harness run "用子 agent 分析这个目录结构"` 端到端跑通；并行子 agent 状态可查；`--json` 输出结构化事件；config 文件可配置
- **状态**：未开始（2026-08-07 确认子 agent 形态：内置 + 并行 + 状态，细节阶段五探讨）

## 阶段 6（后续可选）：TUI / 摘要式压缩 / grep 工具 / 双向通信

- **状态**：未开始
