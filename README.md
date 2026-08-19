# simple-harness

![架构图](https://github.com/HUANGsir-JH/simple-harness/blob/main/simple-harness-architecture.png)

![tui截图](https://github.com/HUANGsir-JH/simple-harness/blob/main/tui%E6%88%AA%E5%9B%BE.png)

用 Go 编写的**极简、可真实使用**的 agent harness（命令行工具），参照 [OpenAI Codex CLI](https://github.com/openai/codex)（Rust）与 [AgentScope Java v2](https://github.com/agentscope-ai/agentscope-java) 的架构思路，定位为通用框架，能够进行长程任务的执行。当前版本 **0.13.0**（阶段 5 子 agent 已落地）。

## 功能特性

- **完整 agent 循环**：ReAct loop（采样 → 工具执行 → 回填）、统一消息模型（含 thinking 完整回传与数字签名）、12 个内置工具、工具并发执行 + 结果合并回填
- **会话管理**：workspace 项目分桶、块级 transcript JSONL 落盘（异步 writer + ordinal + 压缩切段）、`resume` 恢复
- **工具审批**：三档权限（readonly / acceptedit / bypass）+ shell 命令黑白名单 + 会话级记忆（ADR-029）
- **TUI**：bubbletea 全屏交互 UI（实时流式渲染、弹窗选择器、输入队列、Esc 中断）
- **思考推理**：thinking 默认开启、完整回传与审计、`/thinking` 运行时切换（ADR-034）
- **上下文压缩**：85% 阈值自动 LLM 摘要压缩 + `/compact` 手动（ADR-037）
- **AGENTS.md 注入**：项目级向上搜索 + 全局 persona（ADR-043）
- **全局 Skills**：`~/.harness/skills/` SKILL.md 目录 + `skill` 工具渐进式披露（ADR-044）
- **Shell 进程树**：Windows Job Object / POSIX 进程组杀树、超时转后台托管、`background` / `kill_pid` / `wait_task`（ADR-038/040）
- **子 agent**（阶段 5，ADR-045）：`spawn_agent` 纯异步并行、独立会话落盘、嵌套（深度 ≤2）、`send_message` / `interrupt_agent` / `resume_agent` / `list_agents`、`/subagents` 只读实时查看、按类型装配（general-purpose / explore，explore 含强制只读 shell）
- **Plan Mode**：规划模式 + plan 文件 + `plan_done` 交接（ADR-036）

## 快速开始

```bash
# 构建 / 安装（安装到 $GOBIN，已在 PATH）
go build ./cmd/harness
go install ./cmd/harness

# 初始化 workspace 骨架 + 注释版配置模板
harness init

# 运行
harness                          # 进入 TUI 交互模式（默认）
harness run "分析当前目录结构"    # 单轮流式非交互
harness resume --last            # 恢复最近会话进 TUI
harness sessions                 # 查看会话列表
harness version
```

**配置**：`~/.harness/config.yaml`（可被 `HARNESS_HOME` 覆盖）声明 provider 与模型；**API key 只放 `config.local.yaml`**（gitignore），永不入库。`approval.mode` 为默认审批模式。

**TUI 常用命令**：`/switch` `/model` `/effort` `/thinking` `/permission` `/subagents` `/compact` `/rename` `/help` `/exit`；**Esc** 中断当前回合，**Ctrl+C** 复制选中文本。

## 架构

```
cmd/harness/          # CLI 应用层：main + 命令编排（经 app.Load + agent.Build 装配）
internal/
  config/             # 配置域（最底层）：Config/ProviderConfig 类型 + YAML 加载 + 校验
  app/                # 进程级装配根：App{Config, Provider} 惰性单例 + flags 校验 + 审批默认模式
  ui/                 # 渲染（text/json）+ 审批解析 + tui/（bubbletea 全屏交互 UI）
  agent/              # 无状态 ReAct loop + 回合级事件 + Build 主装配工厂（client+工具+中间件链）
  middleware/         # 框架 core：6 hook 扩展机制（onAgent/onReasoning/onToolCall/onActing/onModelCall + onSystemPrompt 管道）+ RuntimeContext
  middleware/impl/    # 内置中间件：基础提示词/AGENTS.md/技能目录/工具说明/会话 load-save/todo 提醒/结果截断/审批三档
  provider/           # 单 anthropic wire（多后端 = base_url 覆盖）+ 块事件适配 + per-call 覆盖
  messages/           # 统一 Message 模型（含 Thinking）+ JSON 序列化
  tools/              # Tool 接口 + 注册表 + 12 内置工具 + 只读安全命令白名单（IsSafeCommand）
  agentstate/         # AgentState 快照（todo/权限/plan/摘要/用量/血缘）+ 原子落盘
  compact/            # 上下文压缩：EstimateTokens/ShouldCompact/Summarizer/Runner
  agentsmd/           # AGENTS.md/CLAUDE.md 发现与拼接（.git 项目根向上搜索 + 截断）
  skills/             # 全局技能：SKILL.md 目录发现 + frontmatter 校验 + 渲染（叶子包）
  session/            # workspace 项目分桶 + 块级 transcript 异步 writer + resume
  subagent/           # 子 agent：Manager 注册表 + 5 控制工具 + buildSubagent 独立装配 + 完成通知复用
  e2e/                # 进程外端到端测试（termtest + mock HTTP）
```

### 核心设计理念

- **无状态 agent（ADR-026）**：agent 不持有会话，`Run(ctx, rc, onEvent)` 一切状态经 `rc`（RuntimeContext）传入——同一 agent 可被多 goroutine 并发 Run（并行子 agent 的前提），切换会话 = 换 rc
- **进程内 middleware（ADR-021/024）**：6 hook 洋葱链贯穿回合，能力（压缩/审批/记忆/截断）全部中间件化，核心 loop 零工程能力
- **统一消息模型 + 事件分层（ADR-002/025）**：provider 适配层负责 ↔ 原生格式；transcript 记块级完整事件、conversation 记模型可见序列（双轨审计）
- **内容通道分类（ADR-039）**：对话历史 = Messages / 稳定配置 = 系统提示管道 / 工具定义 = toolspec / 即时信号 = 临时副本
- **完成通知复用 completion 队列（ADR-040/045）**：后台 shell 进程与子 agent 的完成事件 Append 进会话 Queue → 下轮采样前注入 / TUI 唤醒器——无 wait_agent 工具
- **装配分层**：主装配 = `agent.Build(BuildOptions)`（app 层拼内置 + 控制工具）；子装配 = `subagent.buildSubagent(kind)` 独立装配（uniform persona + 委托段 / explore 专属提示词），agent 包不感知 subagent
- **错误二分类（ADR-003）**：工具错误 RespondToModel（回填继续）/ Fatal（终止）；审批拒绝 = 独立 DeniedError（拒绝 ≠ Fatal）

## 设计决策（ADR 索引）

完整决策与背景见 `docs/tasks/DECISIONS.md`（ADR-021~045 为核心）：

- **ADR-021/024/025/026**：middleware 化 / 6 hook / 会话双轨与块级 transcript / 无状态 agent
- **ADR-027/028/029**：todo 工具 / 结果截断与用户中断 / 三档审批
- **ADR-030/034/036**：TUI / thinking 默认开启 / Plan Mode
- **ADR-037/038/039/040**：用量与压缩 / shell 进程树 / 系统提示通道 / 完成通知与唤醒器
- **ADR-041~045**：装配根 / 后台日志竞态 / AGENTS.md / 全局 Skill / 子 agent

## 测试与验证

```bash
go test ./... -count=1     # 全量单测（含 e2e）
go test -race ./...        # 竞态检测
go vet ./... && go build ./...
go test ./internal/e2e/ -count=1   # 进程外 e2e（termtest + mock HTTP，确定性）
```

- 涉及 workspace 的测试/进程用 `HARNESS_HOME=<临时目录>` 隔离，不污染 `~/.harness/`
- e2e 用 mock HTTP 内容路由保证确定性（主/子 agent 按请求内容分路由，turn_done 锚点）

## 参考实现

- [OpenAI Codex CLI](https://github.com/openai/codex)（`codex-rs`，Rust）
- [opencode](https://github.com/sst/opencode)（TypeScript）
- [AgentScope Java v2](https://github.com/agentscope-ai/agentscope-java)
- [deepseek-harness](https://github.com/agentscope-ai/deepseek-harness)

## 规划

阶段规划与决策状态见 `IMPLEMENTATION_PLAN.md`（权威来源）；任务跟踪在 `docs/tasks/{TASKS,PROGRESS}.md`。当前进度：阶段 1~5 已完成，阶段 6 剩余（grep / 双向通信）。

## 一些个人承认的缺陷

1. 审批系统的设计不够完善，因为平时使用ClaudeCode等都是auto或者bypass模式，所以对这一块的设计不愿太费精力
2. tui的设计不够好用，受限于纯go的设计，tui的选型无法实现和react的ink一样的水准，当然也是我自己不算很懂bubbletea的使用
3. 肯定不是一个好的产品，也不是一个值得推广的工具，但一定是一个值得学习的harness架构设计（当然和deepseek-harness的思想有很大不同）
