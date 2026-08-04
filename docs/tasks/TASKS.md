# 任务列表

> 阶段定义见 `IMPLEMENTATION_PLAN.md`。状态：`未开始 | 进行中 | 已完成`

## 阶段 1：骨架 + 统一消息模型 + Provider + 最小 loop

- **目标**：项目初始化、`messages` 包（统一 Message 模型 + JSONL 序列化）、`provider` 包（Provider/LLMClient 接口 + OpenAI/Anthropic 适配 + 重试）、最小 agent loop（单次采样，无工具）
- **成功标准**：`harness run "你好"` 能从真实 API 拿到流式回复
- **测试**：provider 单测（mock HTTP）；loop 单测（mock LLMClient）
- **状态**：进行中（2026-08-04 项目骨架已初始化）

### 任务单元

| # | 单元 | 状态 |
|---|---|---|
| 1.1 | 项目初始化（go.mod + cmd/harness + git） | ✅ 已完成 2026-08-04 |
| 1.2 | 文档跟踪目录（docs/tasks） | ✅ 已完成 2026-08-04 |
| 1.3 | `messages` 包：统一 Message 模型 + JSONL 序列化 | ✅ 已完成 2026-08-04 |
| 1.4 | `provider` 包：Provider/LLMClient 接口 + OpenAI 适配 | 未开始 |
| 1.5 | `provider` 包：Anthropic 适配 | 未开始 |
| 1.6 | 错误分类 + 重试（指数退避 + 抖动） | 未开始 |
| 1.7 | 最小 agent loop（单次采样）+ CLI run 子命令 | 未开始 |

## 阶段 2：工具系统 + 并发执行

- **目标**：`tools` 包（Tool 接口 + 注册表 + 错误二分类）、内置工具（read_file/list_dir/glob/shell_command/apply_patch）、并行执行 + call_id 回填
- **成功标准**：`harness run "读取当前目录文件列表并告诉我"` 能触发工具调用并正确回填
- **状态**：未开始

## 阶段 3：审批 + Hooks + 错误重试

- **目标**：`approval` 包（三态策略 + 黑白名单 + TTY 交互 + allowlist）、`hooks` 包（PreToolUse/PermissionRequest/Stop）、错误重试完善
- **成功标准**：危险命令触发确认（TTY）/ 自动拒绝（非 TTY）；hook 能拦截工具执行；429 重试生效
- **状态**：未开始

## 阶段 4：会话 + AGENTS.md + 压缩

- **目标**：`session` 包（JSONL 持久化 + resume）、`agentsmd` 包、`compact` 包（TokenBudget v1）
- **成功标准**：`harness resume --last` 能恢复历史继续对话；AGENTS.md 注入生效；长会话自动压缩
- **状态**：未开始

## 阶段 5：子 Agent + CLI 完善 + 文档

- **目标**：`spawn_agent` + `send_message` 单向通信、Renderer 接口完成（simple 渲染器 + --json 模式）、config 包（YAML 加载）、CLI 子命令完善、docs/ 设计文档
- **成功标准**：`harness run "用子 agent 分析这个目录结构"` 端到端跑通；`--json` 输出结构化事件；config 文件可配置多后端
- **状态**：未开始

## 阶段 6（后续可选）：TUI / 摘要式压缩 / grep 工具 / 双向通信

- **状态**：未开始
