# 数据结构关系图

> 本项目所有核心数据结构的组织、关系与**设计机制**一览。用于快速理解"数据从哪来、在哪存、怎么流、为什么这样组织"。
> 配合 `CLAUDE.md`（架构约束）与 `docs/tasks/DECISIONS.md`（ADR）阅读。

## 一、包依赖分层

```mermaid
flowchart TD
    CMD[cmd/harness<br/>App / SessionManager<br/>CLI 装配 + REPL]
    AG[internal/agent<br/>Agent 无状态 ReAct loop]
    MW[internal/middleware<br/>RuntimeContext / Chain / 6 hook]
    PV[internal/provider<br/>Config / Resolved / Client / Event]
    MSG[internal/messages<br/>Message / Conversation 统一模型]
    AST[internal/agentstate<br/>AgentState / TodoItem]
    SES[internal/session<br/>Store / Project / Session / Writer]
    TL[internal/tools<br/>Tool / Registry]
    E2E[internal/e2e<br/>进程外测试]

    CMD --> AG & SES & PV & TL
    AG --> MW & PV & TL & MSG
    MW --> MSG & AST & PV
    SES --> AG & AST & MSG & MW
    TL --> MSG & MW & PV
    E2E --> SES

    classDef leaf fill:#e8f5e9,stroke:#2e7d32
    class MSG,AST leaf
```

依赖约束（防环，ADR-025/026）：`agentstate` 独立成包，`middleware` 与 `session` 都只依赖它；`messages` 是叶子（核心层唯一操作模型）。

## 二、一次 `agent.Run` 的数据流

```mermaid
sequenceDiagram
    autonumber
    participant CLI as cmd/harness (App)
    participant SES as session.Session
    participant RC as middleware.RuntimeContext
    participant AG as agent.Agent
    participant MW as middleware.Chain
    participant PV as provider.Client
    participant TL as tools.Registry
    participant TR as TranscriptWriter

    CLI->>SES: RuntimeContext()（每轮新建）
    SES-->>RC: rc{Messages/State/StatePath/Model/Effort/Enabled}
    CLI->>AG: Run(ctx, rc, onEvent)
    AG->>MW: WrapAgent → SessionMiddleware before
    MW->>RC: load AgentState（rc.State 为空时）
    loop 采样 → 工具 → 回填
        AG->>MW: WrapReasoning → WrapModelCall
        AG->>PV: Request{Model/Effort/Messages/Tools/Enabled}
        PV-->>AG: provider.Event 流（delta + 块完成）
        AG-->>TR: agent.Event（块级，thinking/text/tool）
        alt 工具调用
            AG->>MW: WrapToolCall → WrapActing
            MW->>TL: Tool.Handle(ctx, rc, callID, args)
            TL-->>AG: ToolResult（回填 + emit）
            AG->>RC: conversation.Add(assistant / tool_result)
        end
    end
    MW->>SES: SessionMiddleware after：SaveFile(rc.State) → agentstate.json
    CLI->>SES: Close() → TranscriptWriter flush
```

**要点**：agent 完全无状态（ADR-026）——每次 Run 传入独立 `rc`，`rc` 引用会话的 conversation/state；切换会话 = 换 active 再取新 `rc`，并行 = 每 goroutine 一个 `rc`。

## 三、核心设计机制（为什么这样组织）

> 数据结构清单是"是什么"，本节省略"为什么"。以下机制是全项目的骨架，理解它们比记字段更重要。

### 3.1 无状态 agent：一切经 rc（ADR-026 的骨架）

agent 是系统的"心脏"，但它**完全不持有会话状态**——`agent.Run(ctx, rc, onEvent)` 只认 rc，不碰 `Session`：

```
┌────────────────────────────────────────────┐
│ agent.Agent（无状态，一个进程共享一个）      │
│   Run(ctx, rc, onEvent)                    │
│     ├─ rc.Messages  → 读/追加消息          │
│     ├─ rc.Model / rc.Thinking* → Request   │
│     └─ 经 middleware 读写 rc.State         │
└────────────────────────────────────────────┘
                     ▲  每轮一个独立 rc
   ┌─────────────────┴─────────────────────┐
   │ Session.RuntimeContext() 生成 rc      │
   │  切换会话 = 换 active → 取新 rc       │
   │  并行    = 每 goroutine 各取一个 rc   │
   └───────────────────────────────────────┘
```

收益：
- **切换会话**：换 `active` 指针，下一轮取新 rc 即可——agent 核心零改动
- **并行 agent**（阶段五）：共享 agent + 共享 chain（全部中间件无状态），每 goroutine 一个 rc，零共享可变状态
- **可测**：agent 只依赖 rc（middleware 类型），测试用 FakeClient + 手填 rc，不碰磁盘

### 3.2 Session ↔ RuntimeContext：引用而非拷贝

`Session.RuntimeContext()` 不是复制数据，而是**新建 rc 并填入指向会话内部对象的指针**：

```go
rc.Messages == sess.conversation   // 同一个 *messages.Conversation
rc.State    == sess.state          // 同一个 *agentstate.AgentState
```

副作用是双向的：
- 工具 `Handle(ctx, rc, ...)` 里 `rc.State.Todos` 加 todo → 就是改会话的 state
- agent 里 `rc.Messages.Add(...)` → 就是往会话 conversation 加消息
- Run 结束后 `SessionMiddleware` 把 `rc.State` 存盘 → 会话持久化

| 维度 | Session | RuntimeContext |
|---|---|---|
| 生命周期 | 长：启动创建 → 退出 flush；或 resume 加载 | 短：每轮新建 → 该轮 Run 结束丢弃 |
| 拥有数据 | **是**（conversation / state / writer / statePath） | **否**（仅指针引用） |
| 持久化 | 管（transcript writer + agentstate.json） | 不管（经 SessionMiddleware 落盘） |
| 数量关系 | 1 个 Session | N 个 rc（每轮一个，用完即弃） |
| 所在包 | `internal/session` | `internal/middleware` |

**为什么这样设计**：
1. **无状态 agent**：agent 只认 rc，`Request` 从 rc 取模型/档位，核心循环无会话概念
2. **防环**：`session.go` 引 agent（事件类型）；若 agent 再引 Session 会成环。rc 是中间介质——session 生成 rc（依赖 middleware），agent 消费 rc（依赖 middleware），两者只经 rc 通信，互不依赖

> 类比：Session 是**房间**（长期存在，内放 conversation/state/writer）；rc 是**每次进门发的门禁卡**（写房间号 + 能碰哪些对象，每轮重发）。改卡片指向的东西 = 改房间里的真实物品。

### 3.3 三层会话结构 + SessionManager

workspace 的会话按三层组织（Store → Project → Session），职责逐层收窄：

| 层 | 类型 | 职责 |
|---|---|---|
| Store | `session.Store` | 全局目录布局（root 解析、项目桶定位 `FindProject`） |
| Project | `session.Project` | 某个项目下的会话集合（`Create` / `Resume` / `Sessions` / `Last`） |
| Session | `session.Session` | 单个会话本体（conversation + state + writer + statePath） |

CLI 侧另有一个**横切**的 `SessionManager`（cmd/harness），它不是第四层，而是 REPL 的"会话注册表 + 当前指针"：

```
SessionManager
 ├─ open   map[string]*Session   // 已开会话（进程内 resume 缓存，切回零成本）
 └─ active *Session              // 当前对话的会话（每轮取 RuntimeContext 起 rc）
```

**进程内切换** = 换 `active`：`/switch <id>`（未开 → `proj.Resume` 从磁盘重建入 `open`；已开 → 直接复用）。`/model`、`/effort` 也只改 `active` 的 state 并落盘。三个命令全部不触碰 agent 核心。

### 3.4 Request 的粒度：一次 model call

`provider.Request` 的粒度是**一次模型调用（model call）**，不是一次 agent run：

```
agent.Run（一个回合）
 └─ for 循环（每遇工具调用就再采一轮，直到模型不再请求工具）
     └─ 每轮 = 1 个 provider.Request
         └─ client.Stream(req) = 1 次 HTTP 请求
```

- `AgentInput`（onAgent）↔ 一整个 `agent.Run`
- `ModelCallInput`（onModelCall）↔ 1 个 `Request`（`sample()` 从 ModelCallInput 构建）

因此 `Request.Model/ThinkingEnabled/ThinkingEffort` 是"**这一次调用**用哪个模型、什么档位"（ADR-026 per-call 覆盖，三态：nil/空 = client 默认）。同一回合内不同轮理论上可不同（实际会话里每轮从 rc 取，通常一致）。

### 3.5 App（进程级）vs RuntimeContext（调用级）

两个带"运行时"意味但概念完全不同的对象：

| 维度 | `cmd.App` | `middleware.RuntimeContext` |
|---|---|---|
| 作用域 | **进程级**：一次进程一份 | **调用级**：每次 Run 一份 |
| 内容 | 配置（Config）+ 默认模型（Resolved） | 会话引用 + per-call 覆盖 |
| 用途 | buildAgent / resolveFlags | 承载会话上下文，供 agent / middleware / 工具 |
| 生命周期 | 惰性单例（defaultApp，进程存活期） | 每轮新建，Run 结束丢弃 |
| 所在包 | cmd/harness | middleware（被 agent / session / tools 依赖） |

## 四、核心数据结构

### 4.1 会话与消息（messages / agentstate / session）

```mermaid
classDiagram
    direction LR
    class Conversation {
        +string ID
        +string CreatedAt
        +[]*Message Messages
        +Add(m)
    }
    class Message {
        +string ID
        +Role Role
        +string Content
        +string Thinking
        +[]ToolCall ToolCalls
        +string ToolCallID
        +[]ToolResultBlock ToolResults
        +bool IsError
    }
    class ToolCall {
        +string ID
        +string Name
        +json.RawMessage Args
        +*ToolResult Result
    }
    class ToolResult {
        +bool Success
        +string Content
    }
    class ToolResultBlock {
        +string ToolCallID
        +bool Success
        +string Content
    }
    class AgentState {
        +string SessionID
        +string Model
        +*bool ThinkingEnabled
        +string ThinkingEffort
        +string CWD
        +string CreatedAt
        +string UpdatedAt
        +[]TodoItem Todos
        +*PermissionState Permission
        +*PlanState Plan
        +string Summary
    }
    class TodoItem {
        +int Position
        +string Description
        +string Status
    }
    class Session {
        +string ID
        -string dir
        -string historyDir
        -string statePath
        -*Conversation conversation
        -*AgentState state
        -*TranscriptWriter writer
        +RuntimeContext()
        +AddUser(content)
        +SetModel(model)
        +SetThinkingEnabled(enabled)
        +SetThinkingEffort(effort)
    }
    class Store {
        -string root
        +FindProject(cwd)
    }
    class Project {
        +string Path
        +string Dir
        +Create(model)
        +Resume(info)
        +Sessions()
    }

    Conversation "1" *-- "many" Message
    Message "1" *-- "many" ToolCall
    Message "1" *-- "many" ToolResultBlock
    ToolCall "1" o-- "0..1" ToolResult
    AgentState "1" *-- "many" TodoItem
    Session "1" *-- "1" AgentState : 持久化快照
    Session "1" *-- "1" Conversation : 消息流
    Store "1" *-- "many" Project
    Project "1" *-- "many" Session
```

**双轨持久化**（ADR-025）：消息流 → `transcript`（块级 Line，见 4.5）；非消息状态（模型/档位/todo/权限/plan/摘要）→ `AgentState`（`agentstate.json`，每次 Run 进出各存一次）。

### 4.2 配置与请求（provider）

```mermaid
classDiagram
    direction LR
    class Config {
        +string DefaultProvider
        +map[string]ProviderConfig Providers
    }
    class ProviderConfig {
        +string BaseURL
        +string EnvKey
        +string APIKey
        +map[string]Model Models
    }
    class Model {
        +int ContextWindow
        +*Thinking Thinking
    }
    class Thinking {
        +*bool Enabled
        +[]string Efforts
    }
    class Resolved {
        +string ProviderID
        +string BaseURL
        +string APIKey
        +string Model
        +int ContextWindow
        +bool ThinkingEnabled
        +string ThinkingEffort
        +[]string ThinkingEfforts
    }
    class Request {
        +string Model
        +string Instructions
        +[]*Message Messages
        +[]ToolSpec Tools
        +int MaxOutputTokens
        +*bool ThinkingEnabled
        +string ThinkingEffort
    }
    class ToolSpec {
        +string Name
        +string Description
        +json.RawMessage Parameters
    }

    Config "1" *-- "many" ProviderConfig
    ProviderConfig "1" *-- "many" Model
    Model "1" o-- "0..1" Thinking
    Resolved ..> Model : Resolve() 解析默认
    Request ..> Model : per-call 覆盖
```

**配置链路**：`provider.LoadConfig` → `Config` → `Resolve` → `Resolved`（默认模型，`App.Resolved` 缓存）→ `NewClient`。per-call 覆盖（ADR-026）：`Request.Model/ThinkingEnabled/ThinkingEffort`，`nil/空 = 继承 client 默认`。

### 4.3 运行时上下文与中间件（middleware）

```mermaid
classDiagram
    direction LR
    class RuntimeContext {
        +string SessionID
        +*Conversation Messages
        +*AgentState State
        +string StatePath
        +string Model
        +string ThinkingEffort
        +*bool ThinkingEnabled
        -map attrs
    }
    class Chain {
        -[]Middleware middlewares
        +Add(m)
        +WrapAgent / WrapReasoning / WrapToolCall / WrapActing / WrapModelCall
        +ComposeSystemPrompt(base)
    }
    class SessionMiddleware {
        +OnAgent(rc)
    }
    class ToolInstructionsMiddleware {
        +Tools []ToolSpec
        +OnSystemPrompt(current)
    }
    class Middleware {
        +OnAgent(ctx, rc, in, next)
        +OnReasoning(ctx, rc, in, next)
        +OnToolCall(ctx, rc, in, next)
        +OnActing(ctx, rc, in, next)
        +OnModelCall(ctx, rc, in, next)
        +OnSystemPrompt(ctx, rc, current)
    }

    Chain "1" o-- "many" Middleware
    Middleware <|.. SessionMiddleware
    Middleware <|.. ToolInstructionsMiddleware
    RuntimeContext ..> SessionMiddleware : 读写 State / StatePath
```

**注**：`RuntimeContext` 是 per-call 上下文（每 Run 新建），`Chain` 及其中间件全部无状态 → 共享 chain 可被多 goroutine 并发 Run。

### 4.4 agent 与工具（agent / tools）

```mermaid
classDiagram
    direction LR
    class Agent {
        -Client client
        -string model
        -string instructions
        -*Registry tools
        -*Chain mw
        +Run(ctx, rc, onEvent)
    }
    class Event {
        +EventType Type
        +string MsgID
        +string Text
        +*ToolCall ToolCall
        +*ToolResult ToolResult
        +error Err
    }
    class Registry {
        -[]string order
        -map[string]Tool tools
        +Register(t)
        +Specs()
        +Get(name)
    }
    class ToolError {
        +bool RespondToModel
        +string Message
    }

    Agent "1" o-- "1" Registry
    Registry "1" *-- "many" Tool
    ToolError ..> Tool : Handle 错误二分类
```

**工具错误二分类**（ADR-003）：`RespondToModel=true` → 结果回填、循环继续；`false`（Fatal）→ 终止回合。

**update_todo 工具**（ADR-027）：全量替换当前会话待办清单（`{todos:[{position, description, status}]}`，模型显式填 position 维护顺序），经 `rc.State.Todos` 读写（`AgentState.ReplaceTodos` 按 position 稳定排序；`RenderTodos` 渲染为 markdown 有序列表 `1. [~] …`），随 SessionMiddleware after 无条件落盘 agentstate.json（resume 恢复）。**TodoReminderMiddleware**（onReasoning）：每轮采样计数存 rc.attrs（per-Run），todo 非空且模型连续 ≥10 次 model call 未更新 → 在**请求消息尾部**注入提醒（注入临时副本，不写 conversation → 不落盘/不重放）。模型可见性 = 工具结果回填（基础）+ 提醒（防偏离兜底）。

### 4.5 落盘格式

#### workspace 目录树（`$HARNESS_HOME || ~/.harness`）

```
~/.harness/
├── config.yaml                # 全局配置（provider.LoadConfig 查找）
├── agents.md                  # 全局 persona（阶段四注入）
├── workspaces/
│   └── <项目转义>/            # D:\a\b → D__a_b（保留盘符）
│       └── <session-id>/      # 目录名即会话 id（时间戳-8hex）
│           ├── historys/history-<n>.jsonl   # 块级 transcript
│           ├── agentstate.json              # AgentState 快照（JSON 整体重写）
│           └── plans/                        # 计划文件（预留）
```

#### transcript 行（Line）—— 块级事件，每块一行

```mermaid
classDiagram
    class Line {
        +int64 Ordinal
        +string Type
        +string SessionID
        +string CWD
        +string Model
        +string CreatedAt
        +int Turn
        +string MsgID
        +string CallID
        +string Name
        +json.RawMessage Args
        +*bool Success
        +string Content
        +string Text
    }
    class TranscriptWriter {
        -chan[Line] ch
        -string dir
        -*os.File file
        -int segment
        -int64 ordinal
        -int turn
        +Write(line)
        +OnAgentEvent(ev)
        +NewSegment()
        +Close()
    }

    TranscriptWriter "1" *-- "many" Line : 单 goroutine FIFO 保序
```

`Line.Type` 取值：`meta | user | thinking | text | tool_use | tool_result | turn_start | turn_end`。`MsgID` 关联 thinking/text/tool_use 归属的 assistant 消息。

**resume 语义**：只读最大序号文件（`history-<n>.jsonl`），按 ordinal 逐行重建 conversation（thinking 入 `Message.Thinking`，tool_result 合并）；`agentstate.json` 恢复非消息状态（含模型/档位）。

## 五、CLI 层（cmd/harness）

```mermaid
classDiagram
    class App {
        +Config Config
        +*Resolved Resolved
        +buildAgent()
        +resolveFlags(...)
    }
    class SessionManager {
        -*App app
        -*Agent a
        -*Project proj
        -map[string]*Session open
        -*Session active
        +switchTo(id)
        +switchLast()
        +handleCommand(cmd)
    }
    class output {
        <<interface>>
        +start(t)
        +event(ev)
    }

    App "1" *-- "1" Config
    App "1" *-- "1" Resolved
    SessionManager "1" *-- "1" App
    SessionManager "1" *-- "1" Agent
    SessionManager "1" *-- "1" Project
    SessionManager "1" *-- "many" Session : open 注册表
    SessionManager "1" *-- "1" Session : active 激活会话
```

**REPL 运行时切换**：`/switch <id>|--last`（换 `active`，未开则 Resume 入注册表）、`/model <name>`、`/effort <level>`（都落 `AgentState` 立即持久化）。

## 六、关键关系速查

| 关系 | 说明 |
|---|---|
| `Agent.Run` ↔ `rc` | agent 无状态，每次 Run 一个独立 rc；rc 引用会话的 conversation/state（`Session.RuntimeContext()` 每轮新建） |
| `rc` ↔ `SessionMiddleware` | 无状态中间件从 `rc.StatePath` 读 load / 写 save（共享 chain 可并发） |
| `Request` ↔ `Model/Thinking` | per-call 覆盖：`Request.Model/ThinkingEnabled/ThinkingEffort`，三态（nil/空 = client 默认） |
| `Message.Thinking` | 存审计但不重放（provider 重放 assistant 时剥离；thinking-only 空消息跳过） |
| `transcript` ↔ `AgentState` | 双轨：消息流（块级 Line） vs 非消息状态（JSON 快照），resume 两者重建 |
| `update_todo` ↔ `rc.State.Todos` | todo 全量替换挂 AgentState（ReplaceTodos/RenderTodos），SessionMiddleware after 落盘；TodoReminder 防偏离（ADR-027） |
| `App` ↔ `Config/Resolved` | 配置统一入口：惰性单例缓存默认模型，`--config` 显式路径单独加载 |
| `Conversation` ↔ `Message` ↔ `ToolResult` | 一条 tool_result 消息可合并多块（满足 anthropic 紧邻要求，ADR-024） |
