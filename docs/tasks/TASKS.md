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

- **目标**：`tools` 包（Tool 接口 + 注册表 + 错误二分类）、内置工具（read_file/list_dir/glob/shell_command/apply_patch）、并行执行 + call_id 回填、**完整简单的终端渲染**（文本流式 + 工具调用展示）；编写时**预留工具权限框架扩展点**（为阶段三三档权限铺路）
- **成功标准**：`harness run "读取当前目录文件列表并告诉我"` 能触发工具调用并正确回填；**多轮工具调用闭环在终端渲染下完整可跑 —— 阶段二完成 = 一个可用的简单终端 CLI agent 循环**
- **状态**：未开始（2026-08-06 增补终端渲染 + 权限扩展点）

## todo 工具阶段（阶段 2 之后单开，编号待定）

- **目标**：todo 工具（任务清单拆解/跟踪）
- **状态**：未开始（2026-08-06 用户指定单独做，不进工具系统阶段）

## 阶段 3：权限/审批（三档）+ Hooks + 错误重试

- **目标**：三档权限（`readonly` / `acceptedit` / `bypass`）+ **规则匹配引擎（保留扩展点）** + `hooks` 包（PreToolUse/PermissionRequest/Stop）、错误重试完善
- **成功标准**：危险命令按权限档位放行/确认/拒绝；hook 能拦截工具执行；429 重试生效
- **状态**：未开始（2026-08-06 调整为三档权限，替代原三态）

## 阶段 4：会话 + AGENTS.md + 系统提示词动态拼接 + 压缩

- **目标**：`session` 包（JSONL 持久化 + resume）、`agentsmd` 包、**系统提示词动态拼接**（组装 developer/system 消息，替换 agent.go 硬编码 Instructions）、`compact` 包（TokenBudget v1）
- **成功标准**：`harness resume --last` 能恢复历史继续对话；AGENTS.md 注入生效；系统提示词随上下文动态组装；长会话自动压缩
- **状态**：未开始（2026-08-06 增补系统提示词动态拼接）

## 阶段 5：子 Agent + CLI 完善 + 文档

- **目标**：`spawn_agent` + `send_message` 单向通信、Renderer 接口完成（simple 渲染器 + --json 模式）、config 包（YAML 加载）、CLI 子命令完善、docs/ 设计文档
- **成功标准**：`harness run "用子 agent 分析这个目录结构"` 端到端跑通；`--json` 输出结构化事件；config 文件可配置多后端
- **状态**：未开始

## 阶段 6（后续可选）：TUI / 摘要式压缩 / grep 工具 / 双向通信

- **状态**：未开始
