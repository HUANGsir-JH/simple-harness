# 评测套件设计（Eval Suite）

> 状态：方案设计（2026-08-19）。本文档是评测阶段的**权威方案**；任务跟踪见
> `docs/tasks/TASKS.md`（阶段 7：评测），完成后记 `PROGRESS.md`。
> benchmark 调研结果矩阵见 `docs/eval/BENCHMARKS.md`（随接入进度更新）。

## 1. 背景与目标

simple-harness 0.13.0 已具备完整 agent harness 能力（ReAct loop、12 内置工具、
middleware 链、子 agent、压缩、审批三档、TUI）。框架"能做什么"已由单测/e2e
证明，但**真实任务上的端到端能力**尚未量化。

**目标**：用 7 个业界标准 benchmark 评测 simple-harness，产出**可复现的分数 +
成本/耗时指标**，并与官方基线对比，量化框架能力。

### 1.1 控制变量原则

- **模型固定**：`deepseek-v4-flash`（与官方基线同一模型）。
- **变量 = harness**：simple-harness（默认装配）vs deepseek-harness 极简模式。
- 每 benchmark 用**官方 runner + 官方评分**，只替换 agent 后端，保证分数口径一致。

### 1.2 官方基线（deepseek-v4-flash @ deepseek-harness 极简模式）

**全部 7 个分数出自 [DeepSeek API 更新日志 2026-07-31「DeepSeek-V4-Flash Update」](https://api-docs.deepseek.com/updates/)**：

| Benchmark | 官方得分 | 备注 |
|---|---|---|
| Terminal-Bench 2.1 | 82.7 | 终端任务 |
| NL2Repo | 54.2 | 自然语言 → 仓库生成 |
| Cybergym | 76.7 | 网络安全攻防 |
| DeepSWE | 54.4 | 深度学习仓库 bug 修复 |
| Toolathlon (verified) | 70.3 | 真实工具使用任务套件 |
| Agent Last Exam | 25.2 | 长程 agent 知识/操作考试 |
| Automation Bench (Public) | 25.1 | ⚠️ 无 CLI 接入路径，见 §2.1/§7 |

> **官方评测协议（Note 1 原文）**：Code Agent 任务用 DeepSeek Harness 极简模式
> （`minimal.cordis.yml`：系统提示固定 `You are a helpful software engineer
> assistant.`，工具仅 2 个 = 持久 `bash` + `str_replace_editor`，每任务独立
> workspace/session），采样参数 **max effort + topp=0.95 + temperature=1.0**。
> 2026-08-13 V4-Pro GA 同表（87.9/61.5/83.3/62.7/74.1/25.7/31.8）可作扩展基线。

**协议对齐约束**（我们评测必须满足）：
1. 模型固定 `deepseek-v4-flash`（当前默认模型即 deepseek-v4-flash？→ 评测配置显式声明）；
2. 采样参数：`--effort max` 已有；**`top_p=0.95` + `temperature=1.0` 目前 provider/
   config 未暴露 → 列入 harness 改造项**（§4.3）；
3. 全自动无审批（`approval.mode: bypass`）；
4. 每任务独立工作区/会话（`HARNESS_HOME` 隔离）。

## 2. 评测矩阵（7 benchmark）

> 调研结论见 `docs/eval/BENCHMARKS.md`（6/7 已确认，Toolathlon 调研中）。

**已确认决策（2026-08-19 用户拍板）**：
- **Pilot 范围**：Terminal-Bench 2.1 + DeepSWE + NL2Repo（三者接入方式均已确认，
  且 TB/DeepSWE 同属 Harbor/Pier 框架家族，适配器可复用）；
- **Automation Bench 排除**：官方 runner 仅支持 API 驱动、环境为进程内模拟，
  CLI agent 无法参与，不作 harness 对比项（报告中说明原因）；
- **只跑默认装配**：不做极简模式消融（默认 12 工具 + 完整提示词直接对标官方
  极简模式分数，差异在报告中注明）；
- **改造 harness 支持采样参数**：`top_p=0.95` / `temperature=1.0`（官方协议对齐）；
- **Toolathlon 放第二阶段**：本地 Decoupled 模式需要 harness 支持 MCP over
  SSE 客户端（32 MCP server / 604 工具）——先做 MCP 能力（评估 mcp-go 或
  自研轻量客户端）再接入。

| Benchmark | 接入方式 | 任务量 | 单任务预算 | 环境依赖 | 评分方式 |
|---|---|---|---|---|---|
| Terminal-Bench 2.1 | S2（Harbor 适配器类） | 89 | 低 | Docker，无 GPU | 确定性 verifier |
| NL2Repo | S2 自研驱动（复用官方评分） | 104 | ~$0.03 | Docker（镜像数十 GB） | 真实 pytest |
| Cybergym | S2 官方示例模式（submit.sh） | 1507（可子集） | ~$0.47/33min | Docker + 240GB 数据 | PoC 复现 |
| DeepSWE | S2（官方 Pier InstalledAgent，git commit 契约） | 113（×4 rollouts） | $3–16 | Docker，无 GPU | 手写 verifier（patch 应用） |
| Toolathlon | S2（本地 Decoupled 模式；⚠️ 需 harness 支持 MCP-SSE 客户端） | 108（×3） | <$1 | Docker，需 32 应用凭据 | 容器内确定性 eval |
| Agent Last Exam | S2（官方 Deployer 类，sandbox-CLI 形态） | 105 (ALE-CLI) / 152 (public) | $3–15 | Docker（~105GB 镜像） | 本地确定性 evaluate() |
| Automation Bench (Public) | ❌ 排除（无 CLI 接入路径） | 600 | $0.4–1.3 | 纯 Python | 确定性断言 |

### 2.1 接入策略三选一（按优先级）

1. **S1 官方 runner 的 agent-command 模式**（首选）：runner 在任务容器内直接跑
   `harness run <prompt>`，测到完整 harness（loop + 工具 + 中间件 + 提示词）。
2. **S2 自研轻量驱动**：官方 runner 不支持自定义 agent 时，用官方环境镜像/
   任务数据 + 官方评分脚本，自己写"任务下发 → harness run → 答案采集"薄驱动
   （Cybergym 官方示例模式 / NL2Repo 复用评分脚本均属此类）。
3. **S3 API 模式**（**原则上不用**）：runner 自己驱动 loop、把 harness 当"模型"
   调用，测不到 harness 能力。**Automation Bench 是例外**：官方只支持 API 驱动
   且环境是进程内模拟（CLI agent 无法参与），**建议从 harness 对比中排除**，
   或作为扩展项（自建 OpenAI 兼容网关 + 口径标注）单独评估。

## 3. 评测指标

### 3.1 主指标

- 各 benchmark 官方口径得分（与官方基线直接对比）：`pass@1` / resolved % /
  官方评分，完全以官方 runner 输出为准。

### 3.2 副指标（每任务自动采集）

| 指标 | 来源 |
|---|---|
| 得分/状态（pass/fail/error/timeout） | 官方 runner 结果 |
| token 用量（input/output/cache） | harness `--json` usage 事件 + agentstate.json |
| 成本 USD | 用量 × 模型单价（配置中声明） |
| 墙钟耗时 | runner 计时 |
| 回合数 / 工具调用数 | harness `--json` 事件流统计 |
| 失败归因（超时/超步/环境错/拒答/其他） | 事件流 + 退出码分类 |
| trajectory | `--json` 事件流 + transcript 存档（可复查） |

### 3.3 对比报告

- `eval/results/<run-id>/report.md`：7 表（每 bench 一行：**我们的分数 vs 官方
  基线 Δ** + 成本 + 耗时），附失败任务列表与 trajectory 链接。
- `eval/results/<run-id>/report.json`：全量结构化数据（供后续趋势/回归对比）。

## 4. 架构

### 4.1 目录布局（simple-harness 仓库内）

```
eval/
├── README.md               # 评测入口说明（怎么装、怎么跑、怎么读报告）
├── config.example.yaml     # 评测配置模板（bench 列表/模型/API/并发/预算/超时）
├── orchestrator/           # 编排器（Python：任务队列 + 隔离 + 汇总；薄）
│   ├── run.py              # 入口：evalsuite run --bench ... --tasks ... --run-id ...
│   ├── report.py           # 汇总：results/<run-id>/ → report.md + report.json
│   └── util.py             # 事件流解析（usage/turn 统计）/ 失败归因
├── runners/                # 每 benchmark 一个适配器（封装官方 CLI）
│   ├── terminal_bench.py
│   ├── deepswe.py
│   ├── nl2repo.py
│   └── ...（7 个）
└── results/                # 评测产物（gitignore）
    └── <run-id>/
        ├── meta.json       # 运行元信息（模型/版本/bench/日期/API）
        ├── <bench>/<task>.json   # 每任务结构化结果
        └── report.md / report.json
```

> **编排语言选 Python 的理由**：7 个官方 runner 全部是 pip 包（Python），评测
> 环境天然是 Python；编排器只做"调官方 CLI + 调 harness 二进制 + 汇总"，自身
> 保持薄。harness 本体仍是 Go，不引入跨语言复杂度。
> （若后续要做 `harness eval` 产品化子命令，可在 Go 侧重写编排，适配器协议不变。）

### 4.2 每任务运行约定（对齐官方基线做法）

- **隔离**：每任务独立 `HARNESS_HOME=<run-id>/home/<task>`（独立 workspace/
  会话，不污染 `~/.harness/`）+ 独立工作目录（容器内或任务沙箱）。
- **权限**：评测 = 全自动，config 播种 `approval.mode: bypass`（或 `--permission
  bypass` CLI flag，见 4.3）。
- **确定性**：固定 runner 版本 + 固定任务子集；LLM judge 类评分记录 judge
  版本；一次评测一个 run-id，可重跑对比。

### 4.3 harness 评测支持改造（产品代码，小改动）

| 改动 | 说明 | 必要性 |
|---|---|---|
| `top_p`/`temperature` 配置 | provider.Request + config 暴露（官方协议 top_p=0.95/temp=1.0） | **高：协议对齐必需（用户已拍板）** |
| `--max-turns N` | agent loop 回合上限（超限以失败收尾并回填提示） | **高：防死循环烧钱** |
| （可选）`--permission bypass` | run 模式 CLI flag 覆盖审批档位 | 低：评测用 HARNESS_HOME 配置播种 bypass 即可（零代码） |
| （可选）`--answer-file <path>` | 最终答案写文件 | 低：三个 pilot 的结果契约都不用答案文件（TB=verifier / DeepSWE=git commit / NL2Repo=仓库状态） |
| 退出码语义确认 | 中断/超时与正常完成区分（现中断返回 nil） | 中：评测归因需要 |
| （可选）`--timeout` | 单任务墙钟上限（内部实现而非外部 kill） | 低：外部 kill 可先顶 |

> 现有能力（无需改）：`--json` 事件流（text_done/tool_call/usage/error）、
> 非 TTY 自动拒绝（bypass 下无影响）、HARNESS_HOME 隔离、thinking 开关。

## 5. 分阶段实施

| 阶段 | 内容 | 完成标志 |
|---|---|---|
| 0 环境 | Docker Desktop 启动 + WSL2；`harness` Linux 交叉编译（GOOS=linux）；pip 安装 7 个 runner | `harness` linux 版可跑 `run "你好"` |
| 1 Pilot | **Terminal-Bench 2.1 + DeepSWE + NL2Repo** 三选接入，全链路出分 | 3 张对比表出数，含成本/耗时 |
| 2 扩展 | Cybergym + Toolathlon + ALE + Automation Bench 接入 | 7 张对比表全出 |
| 3 回归 | 固定任务子集（每 bench 抽样）+ 一键重跑 + 趋势对比 | 版本变更后 1 条命令出对比报告 |

## 6. 风险与对策

| 风险 | 对策 |
|---|---|
| Docker Desktop 当前未运行（本机） | 阶段 0 先安装/启动；不行则评估 WSL2 内 Docker / 远端 Linux |
| harness 是 Windows 开发的，评测要 Linux 二进制 | 交叉编译 + 容器内运行（simple-harness 无平台相关依赖？shell 工具需验证） |
| 评测成本（数百任务 × 多轮调用） | 预算上限配置 + 失败快速熔断（连续 N 任务环境错则停） |
| LLM judge 非确定性 | 固定 grader 版本；必要时同任务多次采样记录方差 |
| 官方 runner 版本漂移 | 锁定版本（pip 冻结 / 提交版本号到 meta.json） |
| 网络（任务环境下载依赖） | 镜像预热 + 失败重试一次 |

## 7. 交付物清单

1. `docs/plans/eval-suite.md`（本文档，权威方案）
2. `docs/eval/BENCHMARKS.md`（7 benchmark 调研矩阵）
3. `eval/` 编排器 + 7 适配器 + 配置模板（阶段 1~2 落地）
4. harness 评测支持改动（`--max-turns` / `--answer-file` / `--permission bypass`，附单测 + e2e）
5. 首份对比报告（阶段 1：3 bench；阶段 2：7 bench）
6. TASKS.md 阶段条目 + PROGRESS.md 记录
