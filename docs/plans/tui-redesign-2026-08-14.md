# TUI 重构方案：视觉 + 组件化（2026-08-14）

> 状态：**已实施完成（2026-08-14）**——六阶段全部落地，`go test ./...` + e2e 每阶段全绿；待用户按 §9 人工清单实测
> 分支：`feat/tui-redesign`（自 main 拉出）
> 关联 ADR：ADR-043（本方案批准后落 DECISIONS.md）
> 参考调研：
> - `/Users/jiehao/zzz-harness/codex-tui-调研报告.md`（codex-rs/tui，ratatui 自研渲染层）
> - `/Users/jiehao/zzz-harness/opencode-tui-调研报告.md`（OpenTUI + SolidJS 响应式组件树）

## 1. 背景与目标

现有 TUI（ADR-030，bubbletea + lipgloss + glamour，`internal/ui/tui` 约 6100 行）功能完备、架构干净（Model/Update 纯函数 + Controller 事件桥 + 几何收敛 ADR-031/032），但视觉与组织偏"工程化"：全 ASCII 边框、`YOU/ASSISTANT` 文字标签、9 个写死的颜色、无主题概念、todo/queue 以裸文本条堆叠、每次 refresh 全量重渲染整个时间线。

**本次目标**（范围经用户确认，见 §2）：

1. **视觉升级**：层次、留白、边框、颜色系统达到 codex/opencode 同等质感；
2. **组件化**：theme 包、cell 渲染器、统一 dialog 框架、集中 keymap 表；
3. **渲染性能**：cell 级渲染缓存（P1）、事件合帧（P2，可选）；
4. **功能边界**：命令集、审批流程、队列语义、会话模型一律不变；除用户明确追加的三项交互增强（§2）外零功能变化；
5. **用户追加需求**（2026-08-14 补充）：时间线右侧滚动条、鼠标文本选择、工具块展开态完整展示参数。

## 2. 已确认决策（用户拍板）

| 决策点 | 结论 |
|---|---|
| 布局形态 | 单栏 + 命令面板/弹窗（opencode 风格），不做常驻侧栏 |
| 视觉风格 | 保留无 emoji 基线（ADR-030 风格约束延续）；建立主题系统，Unicode 边框 + ASCII 兜底 |
| 功能范围 | 只做视觉与组件化重构；功能集合、命令集、键位**零变化** |
| 键位 | 维持现状；仅实现层把散落键位收拢到集中 keymap 表（行为不变，不引入前导键） |
| 分支 | `feat/tui-redesign`，分阶段提交，每阶段 `go test ./...` 全绿 |
| 滚动条（补充） | 时间线右侧细滚动条（轨道 + 拇指），主题化、可点击跳转、可拖拽，见 §6.2.1 |
| 文本选择（补充） | 时间线内鼠标左键拖拽选择文本；Ctrl+C 优先复制选区（无选区回退现有行为），见 §6.7 |
| 工具参数（补充） | 工具块展开态完整展示调用参数（args 不截断）+ 完整结果，见 §6.4 |
| 运行时长展示（补充） | 纯 UI 侧计时：回合耗时（footer 运行中实时 + 结束后 `last Ns`）、thinking 耗时（折叠行）、工具调用耗时（块头）；事件边界打点，不回写 transcript/不进 conversation，见 §6.3/§6.4/§6.6 |

## 3. 参考实现提炼（两份调研的核心结论）

### 3.1 共同模式（两个项目都这么做）

1. **时间线 = 类型化 cell**：codex `HistoryCell` trait（user/assistant/tool/thinking 各一种 cell）；opencode 按 part 类型分发（text/tool/reasoning）+ 工具 14 种专用组件。cell 是时间线唯一单元。
2. **主题 = 语义 token**：opencode 60+ token JSON（文本/背景/边框/diff/markdown/syntax 各自一套）；codex 走"语义色 + 终端默认色探测 + 256 色量化"（dim/bold/cyan/green/red 表达语义，背景/分隔线才用探测色 blend）。
3. **统一 dialog/选择器框架**：opencode `DialogSelect` 一个组件承载会话/模型/主题所有选择；codex bottom_pane 的 view 栈统一承载审批/命令/文件搜索弹窗。
4. **集中 keymap**：codex 三件套（配置 schema → 运行时解析 → action 目录）；opencode 命令注册表 + mode 栈。键位与命令是同一事实来源。
5. **渲染快照测试**：codex insta `.snap` + vt100 逐帧测试；opencode bun test + 组件测试。
6. **流式性能核心 = 稳定前缀 + 可变尾部**：codex `stable_source_len/stable_rendered_len` 只重渲染最后一个顶层块；opencode `message.part.delta` 字符串拼接 + 16ms `batch()` 合帧 + 细粒度局部重绘。

### 3.2 采纳 / 不采纳

| 参考点 | 决策 |
|---|---|
| cell 类型化渲染器 | ✅ 采纳（cells.go / tools.go，见 §5） |
| 主题 token 化 + 明暗探测 | ✅ 采纳（theme.go，~24 token 精简版，见 §6.1） |
| 统一 modal 框架 | ✅ 采纳（dialogs.go，四实例：审批/ask/select/help） |
| 集中 keymap 表 | ✅ 采纳（keymap.go，行为不变） |
| cell 渲染缓存 | ✅ 采纳（P1，render.go） |
| 事件合帧（16ms batch） | ⏸ P2 可选：现有逐事件 refresh 在缓存生效后开销已大降，合帧收益再评估 |
| thinking 抽取首个 `**粗体**` 做标题 | ✅ 采纳（纯渲染层，cells.go） |
| 工具双形态（Inline 单行 / Block 边框块） | ✅ 采纳（tools.go，现有 per-tool 分派升级为双形态） |
| diff 语法高亮（chroma，已在依赖树） | ✅ 采纳（P1 内；渲染层增强，非新功能） |
| codex 内联视口 + 终端原生 scrollback | ❌ 不采纳（bubbletea alt-screen 模型，改动过深） |
| 右侧滚动条 + 鼠标文本选择 | ✅ 用户追加需求（2026-08-14）：opencode scrollbox 滚条 / codex 原生选择的等价体验，自实现见 §6.2.1 / §6.7 |
| `@` 提及 / `!` shell / 外部编辑器 / undo-redo / vim 模式 / 侧栏 / 主题切换命令 | ❌ 本次不做（功能变动，列入二期 §12） |

## 4. 设计原则

1. **功能零变化是硬约束**：本次任何 commit 不得改变命令集、键位语义、审批决策流、队列消费规则、会话/transcript 行为。
2. **几何单一来源**（ADR-032 教训延续）：弹窗宽度/内容宽度继续收敛到统一函数，禁止调用方各自算偏移。
3. **语义色优先**（codex styles.md 哲学）：颜色只表达语义（accent/成功/失败/进行中/muted），背景与分隔线用主题 token，避免随意自定义 RGB。
4. **无 emoji 基线**：状态用 ASCII + 颜色（`[OK]/[ERR]/[RUN]` 保留），边框用 Unicode box-drawing，非 TTY/窄屏回退 ASCII（沿用现有 asciiBorder 兜底策略）。
5. **每阶段可测可回滚**：Phase 1 先做"零视觉变化"的地基（theme/keymap 收拢），证明重构管线稳定后再动视觉。
6. **缓存正确性优先于缓存收益**：cell 缓存 key 必须包含内容版本 + 宽度 + 主题 + 选中态，宁可少缓存不可错渲染。

## 5. 目标架构（单包内文件重组）

不拆子包（避免 import 环与测试churn；包内按职责分文件）：

```
internal/ui/tui/
  theme.go        # 主题 token + 内置主题 + 明暗探测 + 样式构建（取代 view.go 全局 style 变量）
  keymap.go       # 键位动作表 + 上下文分组（行为不变，handleKey 表驱动）
  layout.go       # 布局几何（自 interaction.go 迁移，扩展为区域矩形模型）
  view.go         # View() 组装（header/timeline/aux/composer/footer 拼装）
  cells.go        # message/thinking/system cell 渲染器（自 view.go 拆出）
  tools.go        # 工具双形态 cell（自 tool.go 分派逻辑 + view.go 渲染重组）
  diff.go         # diff/代码块渲染（gotextdiff 输出 + chroma 高亮）
  dialogs.go      # 统一 modal 框架 + 审批/ask/select/help 四实例（自 popup.go/view.go 重组）
  render.go       # timeline 组装 + cell 缓存 + 命中区间记录（自 view.go renderTimeline 演进）
  model.go / controller.go / command.go / events.go / run.go / interaction.go / md.go / approver.go
                  # 保留并局部调整（交互状态机与 Controller 事件桥不动）
```

### 5.1 theme.go 接口草案

```go
type Token int // 语义 token 枚举
const (
    TokenCanvas Token = iota // 屏幕背景
    TokenPanel               // 面板背景（todo/弹窗）
    TokenRaised              // 选中态背景
    TokenBorder              // 普通边框
    TokenBorderFocus         // 焦点边框
    TokenText                // 正文
    TokenMuted               // 次级文字
    TokenAccent              // 品牌/焦点（现 cyan）
    TokenUser                // 用户消息
    TokenSuccess / TokenWarning / TokenError
    TokenDiffAdd / TokenDiffDel / TokenDiffMeta
    TokenSyntaxComment / TokenSyntaxKeyword / TokenSyntaxString / TokenSyntaxNumber / TokenSyntaxFunc
)

type Theme struct {
    Name   string
    Dark   bool
    Colors map[Token]lipgloss.Color
    Border lipgloss.Border // unicode 圆角 / ASCII 兜底
}

// 当前主题：termenv.HasDarkBackground() 探测 → harness-dark / harness-light
func currentTheme() Theme
// 样式构建：theme + token → lipgloss.Style（带缓存）
func tstyle(t Token) lipgloss.Style
```

- 内置主题：`harness-dark`（默认，基于现有 234/235 底色精修）、`harness-light`。
- 现有 `styleXXX` 全局变量迁移为 `tstyle()` 派生；`styleBrand/styleText/...` 等兼容别名保留至全部调用点迁移完。
- 测试可注入主题（`setThemeForTest`），渲染输出确定性不变。

## 6. 视觉规范（分区域）

### 6.1 主题 token 表（harness-dark 默认）

| Token | 值（dark） | 用途 |
|---|---|---|
| Canvas | 234 | 屏幕背景 |
| Panel | 235 | 面板背景 |
| Raised | 237 | 选中/悬浮背景 |
| Border | 240 | 普通边框 |
| BorderFocus | accent | 焦点边框 |
| Text | 252 | 正文 |
| Muted | 244 | 次级文字 |
| Accent | 81（cyan） | 品牌/焦点/用户消息 |
| Success | 78 | 成功 |
| Warning | 220 | 运行中 |
| Error | 203 | 失败 |
| DiffAdd | 78 / DiffDel | 203 / DiffMeta | 244 |
| Syntax* | 自 chroma 默认 dark 映射 | 代码高亮 |

### 6.2 布局（单栏，top→bottom）

```
 HARNESS · <model>                          <session name>    header 1 行
 ──────────────────────────────────────────────────────────    divider（border 色）
                                                              timeline（viewport 弹性主体，
   [user/assistant/tool/system cells，cell 间 1 空行]            贴底滚动；向上翻自动脱离贴底）
 ──────────────────────────────────────────────────────────
 TODO（有 todo 时；panel 背景细边框块，≤5 项 + 统计行）            aux 区（原文本条面板化）
 QUEUED（有排队时；muted 同风格）                                aux 区
 ╭──────────────────────────────────────────────────────────╮
 │ composer（多行，焦点 accent 边框 / 失焦 border）             │  composer
 ╰──────────────────────────────────────────────────────────╯
 READY / ◌ RUNNING          [PLAN] readonly · effort:x · ctx · todo · queued   footer 1 行
```

- 布局几何延续现有 `layout()` 的分区计算（headerHeight/footerHeight/composerTop/mainTop），aux 高度并入计算（现状已是）。
- 窄屏（<56 列）沿用 outerPad 归零策略；弹窗宽度继续用 `modalPanelWidth/modalInnerWidth` 收敛（ADR-032）。
- 贴底滚动：沿用 `autoScroll` 语义（在底部→新内容自动贴底；向上滚动→脱离贴底），本次只做视觉，不加增量滚动/加速度（二期）。

### 6.2.1 右侧滚动条（用户追加需求）

```
                                                     ┃   ← 轨道（1 列，TokenBorder 淡化色）
   ▸ user message                                    █   ← 拇指（TokenAccent；总行数≤视口高时不显示）
   assistant markdown 正文 …                          ┃
   [OK] shell_command: ls -la                         ┃
   …（内容太长时，右侧滚条提供全局位置感）                   █
                                                     ┃
```

- **位置**：时间线右侧固定 1 列，不参与内容排版（viewport 宽度减 1 即可）。
- **轨道**：满高 `│`（或 `┃`），颜色 = TokenBorder 淡化（可读但不抢眼）。
- **拇指**：`█` 字符；高度 `h = clamp(viewportHeight² / totalLines, 1, viewportHeight)`；位置 ∝ `YOffset / (totalLines - viewportHeight)`；颜色 TokenAccent（滚动/拖拽中）→ TokenMuted（静止）；`totalLines ≤ viewportHeight` 时不渲染。
- **交互**：左键点击轨道任意处 = 按比例跳转（SetYOffset，配合贴底判定）；按住拇指拖拽 = 连续滚动（Press 记录 → Motion 更新 → Release 结束）；滚轮行为不变；弹窗打开时滚条不响应（与现有点击路由一致）。
- **实现**：`View()` 内 `lipgloss.JoinHorizontal(viewport.View(), scrollbarView())`；hits 命中坐标保持内容相对（不受滚条影响）；modal 居中按含滚条的总宽计算。
- **美观要求**：颜色全部走主题 token；拖拽期间无闪烁（Motion 事件统一走 refresh 管线）。

### 6.3 消息 cell 规范

| 类型 | 现状 | 新视觉 |
|---|---|---|
| user | `YOU` 标签 + 左侧竖线 | 首行 `›` 前缀（accent bold）+ 内容；多行后续行缩进 2 列（codex UserHistoryCell 同款），去标签 |
| assistant | `ASSISTANT` 标签 + 正文 | 无标签，正文直接渲染（markdown）；thinking 块在正文前 |
| system | `SYSTEM` 标签（muted） | 保留标签但缩为 muted 小标签 `·` 前缀风格：`─ SYSTEM` 行 + 内容 |
| error | `ERROR` 标签（红） | 同上，红色 |
| thinking 折叠 | `THINKING [collapsed] N chars` | `Thinking · <首个**粗体**标题，无则省略> · N chars · <耗时> [collapsed]`（muted）；展开淡化样式渲染全文（首个标题加粗），点击区域/键位不变。耗时 = 首个 thinking 增量 → thinking_done（流式中实时、结束后定格；历史块无时间戳不显示） |
| 流式 | 纯文本拼接 | 保留纯文本流式（block 完成才走 markdown，ADR-030 决策不变） |

### 6.4 工具 cell 双形态（tools.go）

- **Inline（单行）**：read_file（元信息行）、list_dir、glob、update_todo 概要 → `[OK] read_file path · 42 lines · 1.2KB`（状态徽章 + 摘要单行）。
- **Block（边框块）**：shell_command 输出、write_file diff、apply_patch diff、失败详情 → 左竖线块 + 状态行 + 内容折叠 + `… +N lines` 展开提示（现有折叠交互不变：点击/Enter）。
- **展开态完整参数（用户追加需求）**：展开后 = 状态行 + **完整 args**（原始 JSON pretty-print + Hardwrap，可选 chroma JSON 高亮）+ 完整结果（diff/输出/代码），一律**不截断**。头部摘要行（`toolCallSummary`）维持截断——那只是折叠态的块头；展开态正文不再吃 `truncate(...,200)` 类截断。
- 状态徽章保持 `[RUN]/[OK]/[ERR]` + 黄/绿/红（无 emoji 约束）。
- **调用耗时（用户追加需求）**：块头追加 `· 1.4s`（ToolStatus.Started/Duration 打点；运行中实时、结束后定格；历史块无时间戳不显示）。
- diff 渲染（diff.go）：gotextdiff 输出按 +/-/hunk 行着色（现状已做），代码块语言检测 + chroma 高亮（shell/输出不强行高亮，markdown 代码块与 write_file 内容高亮）。
- 折叠提示行文字从 `[collapsed N lines]` 升级为 `… +N lines`（muted）——**注意**：单测如断言旧文案需同步（§8 契约表）。

### 6.5 dialog 统一框架（dialogs.go）

```
 ╭─ PERMISSION REQUIRED ───────────────────────────────────╮
 │                                                          │
 │  tool_name                                               │
 │  summary（Hardwrap）                                      │
 │                                                          │
 │  [Y] Allow once   [S] Allow for session   [N] Deny       │
 ╰──────────────────────────────────────────────────────────╯
```

- 通用骨架：圆角边框 + panel 背景 + accent bold 标题 + 内容区 + muted footer 提示行。
- 四实例迁移：审批（键位 y/s/n/esc 不变）、ask（↑↓/Space/Enter/Esc + custom 输入不变）、select（↑↓/Enter/Esc 不变，标题 `SESSIONS/MODELS/...` 不变）、help（双列，键位 esc/enter 关闭不变）。
- 几何继续 `modalPanelWidth/modalInnerWidth`；`TestModalsFitPanelWidth`（ADR-032）迁移到新框架。

### 6.6 composer / footer / aux

- composer：保留 boxed textarea；边框改圆角（unicode），焦点 accent、失焦 border；占位符文案 `Ask anything or type / for commands` **保留**（e2e 断言依赖）；去掉 `MESSAGE [inactive]` 文字标签，焦点状态由边框色 + footer 表达（Tab 切焦点行为不变）。
- footer：保持单行左右布局；左侧运行中 `◌ RUNNING <elapsed>`（回合打点实时计时）、空闲 `READY · last <elapsed>`（上一回合总耗时，用户追加需求）；右侧 `[PLAN] permission · effort · ctx · todo · queued`（内容不变，仅样式统一 muted）。
- aux：todo 面板化（细边框 + `TODO` 小标题 + ≤5 项 + 统计行，内容与排序不变）；queue 条与 todo 同风格。

### 6.7 鼠标文本选择（用户追加需求）

- **触发**：时间线内左键 Press 记录锚点（内容行/列）；按住拖拽（Motion）扩展选区；Release 结束。**位移为零（或 < 1 格）按"点击"处理**——保留现有工具块/thinking 点击折叠切换语义。
- **渲染**：选区用选中背景（TokenRaised + TokenText，或 lipgloss Reverse）精确覆盖；跨行选区逐行片段渲染。
- **复制**：有选区时 Ctrl+C 复制选区纯文本（渲染行经 `ansi.Strip` 取正文）；**无选区时保持现有"复制 composer 内容"行为**；Esc / 在其他区域点击清除选区。
- **状态机**：鼠标处理重写为 press / motion / release 三态（含"点击 vs 拖拽"判定），替代现有单 press 处理；弹窗打开时时间线选择不生效。
- **范围**：仅时间线内容；composer 内选择受 bubbles textarea 组件限制，本次不做（二期）。
- **边界拖动自动滚动**（拖到视口上/下缘自动滚屏）：P2 可选，视实现复杂度决定。

## 7. 性能策略

- **P1 cell 渲染缓存**：`timelineItem` 增加渲染缓存 `{content string, width, themeRev, selected bool}`；事件处理只标记受影响 cell dirty，`refresh` 重拼时间线字符串时命中缓存的 cell 直接复用。markdown 块级缓存（md.go 现状）保留。
- **P2 事件合帧**（可选，P1 后评估）：高频 text delta 合批到 ~32ms 一次 refresh（对齐 opencode 16ms batch / codex FrameRequester）。注意 `refresh(true)` 的贴底判定在合帧下语义不变（最后一次统一处理）。

## 8. 兼容性契约

### 8.1 保持不变（回归红线）

- 命令集：`/switch /model /effort /thinking /permission /plan /usage /compact /rename /help /exit` 及参数语义；
- 键位：Tab 焦点 / Esc 中断 / PgUp-PgDn / j-k-Enter-Space / Shift+Enter 换行 / ↑↓ 历史 / 审批 y-s-n / ask 键位——全部不变；
- Ctrl+C：**有选区复制选区（新增）**；无选区复制 composer 内容（现有行为不变）；
- 鼠标点击工具块/thinking 折叠切换：保留（press→release 无位移 = 点击）；press→拖拽 = 文本选择（新增）；
- 审批三档 + 会话记忆、队列消费规则（runDoneMsg 边界）、懒加载会话、后台唤醒——不动；
- 时长展示（用户追加需求）= 纯 UI 侧计时（事件边界打点 + 渲染时 time.Since）：不进 conversation、不回写 transcript、不新增事件；
- 事件桥（agentEventMsg/approvalRequestMsg/askRequestMsg/completionWakeMsg）——不动。

### 8.2 e2e 字符串契约（termtest 断言）

| 字符串 | 处置 |
|---|---|
| `Ask anything`（composer 占位符） | 保留 |
| `PERMISSION REQUIRED`（审批标题） | 保留 |
| `PLAN APPROVAL`（plan_done ask 标题） | 保留 |
| 系统行中文文案（`Plan 模式已开启` 等） | 保留 |
| 工具块摘要（`created a.txt` 等） | 保留 |
| mock 回复文本（`目录已列出` 等） | 保留 |

单测中断言旧视觉文案（如 `YOU`/`ASSISTANT` 标签、`[collapsed` 行）的用例在对应 Phase 同步更新（§9）。

## 9. 测试策略

- **单测为主**（ADR-030 策略延续）：
  - Model.Update 状态机测试（键位/队列/审批/ask/命令）全部保留，仅因视觉文案变化的断言同步；
  - 新增：theme 解析/明暗探测单测、cell 渲染单测（user/assistant/thinking/system）、工具双形态单测（含**展开态 args 完整性**）、dialog 几何单测（继承 ADR-032 `TestModalsFitPanelWidth`/`TestHitRangesAlignWithRenderedLines` 精神）、keymap 表完整性测试（每 action 有绑定）；
  - 滚动条单测：thumb 高度/位置比例、totalLines ≤ 视口高时隐藏、点击跳转坐标换算；
  - 文本选择单测：press/motion/release 状态机、点击 vs 拖拽判定（零位移不误触发 toggle）、跨行选区渲染、`ansi.Strip` 复制内容正确性；
  - 渲染缓存正确性测试：改宽度/主题/选中态后缓存失效。
- **e2e**：`internal/e2e` 6 个 TUI 用例断言串保持（§8.2），跑全绿；若视觉改动触碰断言，只改该断言对应文案并在 commit message 注明。
- **人工清单**（Phase 6）：鼠标点击/中文 IME/Ctrl+C 复制/resize/窄屏 40 列/长会话滚动性能。

## 10. 实施阶段（分支内，每阶段独立 commit + 全绿）

| Phase | 内容 | 视觉变化 |
|---|---|---|
| 0 | 本方案批准；ADR-043 落 DECISIONS.md；TASKS.md 建条目；基线 `go test ./...` 确认 | 无 |
| 1 | theme.go + keymap.go：样式与键位收拢为表驱动，全部调用点迁移 | **零**（验收：全部测试不因视觉文案变化而改动） |
| 2 | dialogs.go：统一 modal 框架 + 四实例迁移 | 弹窗视觉升级 |
| 3 | cells.go + layout/view：header/footer/aux/composer 视觉 + 消息 cell + **右侧滚动条**（§6.2.1）+ **鼠标文本选择状态机**（§6.7）+ **回合/thinking 时长**（§6.3/§6.6） | 主界面视觉升级 + 交互增强 |
| 4 | tools.go + diff.go：工具双形态 + **展开态完整 args**（§6.4）+ **工具耗时**（§6.4）+ diff/代码高亮 | 工具块视觉升级 |
| 5 | render.go：cell 渲染缓存（P1；P2 合帧视评估） | 无 |
| 6 | 测试迁移收尾 + e2e 全绿 + 文档（ADR-043/IMPLEMENTATION_PLAN.md 状态/PROGRESS.md）+ 人工实测清单（含滚条拖拽/文本选择复制）交付 | — |

## 11. 风险与对策

| 风险 | 对策 |
|---|---|
| 视觉回归（渲染正确性下降） | Phase 1 零视觉基线；cell/dialog 单测锚定渲染输出；e2e 兜底 |
| 窄屏/ConPTY 边框异常 | 沿用 asciiBorder 兜底与 ADR-031/032 几何收敛；e2e 与 40 列人工测试 |
| 缓存错渲染（选中态/宽度/主题 key 不全） | 缓存 key 五元组 + 针对性失效测试；宁可缓存窄（仅内容不变的 done cell） |
| 阶段间 long-lived 分支漂移 | 每阶段一次小步 commit；main 有冲突时优先 rebase 于功能不变子集 |
| glamour 固定 dark 样式与主题不一致 | glamour 按主题 dark/light 选 style；代码高亮统一走主题 Syntax token |

## 12. 明确不做（二期候选清单）

`@` 文件引用、`!` shell 前缀、外部编辑器、`/export` 导出、`undo/redo`（git 驱动）、`/theme` 主题切换命令、Ctrl+P 命令面板快捷键、会话列表搜索/分组、vim 模式、常驻侧栏、滚动加速度。以上任一都属功能变动，留待二期单独提案。
