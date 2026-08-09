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

## 阶段 2：工具系统 + 并发执行 + 终端渲染 + middleware 骨架

- **目标**：**移除 openai wire** ✅；`tools` 包（Tool 接口 + 注册表 + 错误二分类）、内置工具（5 个）、并行执行 + call_id 回填、完整简单的终端渲染（thinking + 工具调用 + --json）、**agent 纯 ReAct loop + middleware 挂载点**（onActing 即权限扩展点）、**交互式 REPL**
- **成功标准**：`harness run "读取当前目录文件列表并告诉我"` 触发工具调用并正确回填 ✅；多轮工具调用闭环在终端渲染下完整可跑 ✅；`harness` 交互式多轮 ✅ —— **阶段二完成 = 一个可用的简单终端 CLI agent 循环**
- **测试**：tools 单测（临时目录 + shell 真跑 + apply_patch）；agent loop 单测（FakeClient：无工具/tool 闭环/并行/错误二分类/middleware 链/thinking）；**termtest 进程外 e2e（mock HTTP：单轮 turn_done 锚点 + 交互式）**；真实 API 冒烟 ✅
- **状态**：✅ 已完成（2026-08-07）

### 单元表

| # | 单元 | 状态 |
|---|---|---|
| 2.1 | tools 包（Tool 接口 + 注册表 + 错误二分类） | ✅ 2026-08-07 |
| 2.2 | 内置工具（read_file/list_dir/glob/write_file/shell_command/apply_patch） | ✅ 2026-08-07 |
| 2.3 | middleware 链（RuntimeContext + 6 hook + ToolInstructions） | ✅ 2026-08-07 |
| 2.4 | agent 纯 ReAct loop + 回合级事件（thinking/turn_done） | ✅ 2026-08-07 |
| 2.5 | 终端渲染（thinking + 工具调用展示 + --json 事件） | ✅ 2026-08-07 |
| 2.6 | CLI 装配 + 交互式 REPL | ✅ 2026-08-07 |
| 2.7 | termtest 进程外端到端 + 真实冒烟 | ✅ 2026-08-07 |

## todo 工具阶段（阶段 2 之后单开，编号待定）

- **目标**：todo 工具（任务清单拆解/跟踪）
- **状态**：未开始（2026-08-06 用户指定单独做，不进工具系统阶段）

## 阶段 3：权限/审批（三档）

- **目标**：三档权限（`readonly` / `acceptedit` / `bypass`）**以 onActing middleware 挂载** + 黑白名单 + TTY 交互 + **会话级记忆**（用户决策替代 allowlist，ADR-029；规则匹配引擎保留扩展点，复杂匹配不强做）
- **成功标准**：危险命令按权限档位放行/确认/拒绝；middleware 链能拦截工具执行
- **状态**：**已完成（2026-08-09，ADR-029）**。交付：`internal/middleware/impl/`（Policy 三档 + shell 黑白名单 + ApprovalMiddleware 挂 onActing + `middleware.DeniedError` 拒绝回填）+ channelApprover（REPL/runCmd 单一读方协调，y/s/n）+ `AgentState.Permission`（Mode 会话创建播种 + `/permission` 切换 + Approved 会话级记忆）+ e2e 真实 TTY 审批交互。
- **错误重试（非本阶段交付）**：429 依赖 SDK 内置退避（ADR-012，已承担）；流中断恢复未做，独立待办。
- **Hooks（PreToolUse 子进程）**：降级远期，由 middleware 承载。

## 阶段 4：Workspace（~/.harness/）+ 会话 + 系统提示词拼接 + 压缩

- **目标**：**`~/.harness/` 统一 workspace**（sessions/快照、subagents/*.md 预留、tools.json、memory/）；`session` 包（JSONL 消息流 + **轻量 AgentState 快照** + resume，落 workspace）；`agentsmd` 包（**作为 onSystemPrompt middleware** 注入 + 系统提示词动态拼接，AGENTS.md 项目级向上搜索保留）；`compact` 包（TokenBudget v1 + 摘要式 + **大工具结果 eviction**，作为 onReasoning middleware，**不做 overflow 安全网**）
- **成功标准**：`harness resume --last` 能完整恢复（含权限/todo 等非消息状态）；AGENTS.md 注入生效；系统提示词随上下文动态组装；长会话自动压缩；超大工具结果落盘 + read_file 指针
- **状态**：未开始（2026-08-06 增补系统提示词动态拼接；2026-08-07 确认 workspace/compaction 范围；**2026-08-09 TUI 阶段优先，本阶段剩余 AGENTS.md 注入 + 压缩后置**）

## 阶段 5：子 Agent（内置 + 并行 + 状态）+ CLI 完善 + 文档

- **目标**：**内置几个子 agent**（general-purpose 等）+ **并行执行** + **状态跟踪**（pending/running/completed）+ `send_message` 单向（fork 过滤保留）；自定义声明式（subagents/*.md）预留扩展点；Renderer 接口完成（simple 渲染器 + --json 模式）、config 包（YAML 加载）、CLI 子命令完善、docs/ 设计文档
- **成功标准**：`harness run "用子 agent 分析这个目录结构"` 端到端跑通；并行子 agent 状态可查；`--json` 输出结构化事件；config 文件可配置
- **状态**：未开始（2026-08-07 确认子 agent 形态：内置 + 并行 + 状态，细节阶段五探讨；**2026-08-09 TUI 阶段优先，本阶段后置**）

## 阶段 TUI：bubbletea 全屏交互 UI（提前自阶段 6，子 agent 之前）

- **目标**：bubbletea + bubbles 全屏聊天式 TUI **替代 REPL**（消息列表流式 + md 渲染、底部多行输入 + 队列、工具折叠块 + diff、审批弹窗、斜杠命令弹窗选择器 + 自动补全、thinking 折叠、todo 常驻条、切换全量替换、命令落盘）；TUI 上线后 **REPL 删除**（`repl()` 留薄壳调 `tui.RunTUI`）；`run` 保留流式非交互。
- **成功标准**：`harness` 进 TUI 完整交互（流式回复 / 工具展示 / 审批 y/s/n / 队列连跑 / 斜杠命令 / 切换）；`resume` 历史首屏 + 历史 thinking 折叠；TUI 交互全部无 TTY 单测覆盖；termtest e2e 全面覆盖；版本 0.6.0。
- **测试**：T1 Model.Update 无 TTY 单测（消息流/工具状态机/审批/队列/切换/历史/命令消费）；T2 View 关键内容断言；T3 事件桥 + T4 审批桥单测；T5 termtest e2e 尽量全面 + 人工测试清单（鼠标/IME/剪贴板/resize）。
- **状态**：✅ **已完成（2026-08-09，ADR-030）**——bubbletea TUI 替代 REPL（消息流式 + md 渲染 + 工具折叠块 + 审批弹窗 + 斜杠命令弹窗 + 队列 + todo 常驻条 + 切换全量替换 + 命令落盘 command 行）；REPL 删除（runREPL + SessionManager 移除）；`run` 保留流式非交互；版本 0.6.0。

### 任务单元

| # | 单元 | 状态 |
|---|---|---|
| W0 | 决策落盘（ADR-030 + TASKS 条目 + 任务拆分） | ✅ 2026-08-09 |
| W1 | 依赖 + TUI 骨架（bubbletea/bubbles/lipgloss/glamour/gotextdiff + `internal/ui/tui` 空 Model + RunTUI） | ✅ 2026-08-09 |
| W2 | 消息区 + 输入区 + md 渲染（glamour 块完成渲染；textarea + 提交 + 启动回合） | ✅ 2026-08-09 |
| W3 | 回合生命周期（Esc 中断）+ 工具折叠块（分派表 + write/apply diff）+ 状态栏 | ✅ 2026-08-09 |
| W4 | 审批弹窗 + 斜杠命令弹窗选择器 + 自动补全 + 队列 + 命令落盘 command 行 | ✅ 2026-08-09 |
| W5 | 删 REPL + run 整理 + e2e 全面覆盖 + 收尾（版本 0.6.0 + 人工测试清单交付） | ✅ 2026-08-09 |

## 阶段 6（TUI 后后续可选）：摘要式压缩 / grep 工具 / 双向通信

- **状态**：未开始（TUI 已提前为独立阶段；本阶段为压缩/grep/双向通信剩余项）
