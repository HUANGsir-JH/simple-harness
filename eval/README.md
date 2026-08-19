# eval — simple-harness 评测套件

对 simple-harness 做端到端能力评测：用 7 个业界标准 benchmark，同一模型
（`deepseek-v4-flash`）对比官方基线（DeepSeek Harness 极简模式，见
`docs/plans/eval-suite.md` 与 `docs/eval/BENCHMARKS.md`）。

## 目录

```
eval/
├── config.example.yaml    # 评测配置模板（复制为 config.yaml 使用）
├── orchestrator/          # 编排器（Python，薄）
│   ├── run.py             # 入口：跑选定 benchmark 的任务子集
│   ├── report.py          # 汇总 results/<run-id>/ → report.md + report.json
│   ├── compare.py         # 回归对比：多 run 趋势 → results/trend.md + trend.json
│   └── util.py            # harness --json 事件流解析 / 失败归因 / 用量统计
├── runners/               # 每 benchmark 一个适配器（封装官方 runner）
│   ├── terminal_bench.py  # Harbor agent 类（骨架，TODO(env)）
│   ├── nl2repo.py         # 自研驱动 + 官方 pytest 评分（骨架，TODO(env)）
│   ├── cybergym.py        # 官方示例包装器模式（骨架，TODO(env)）
│   ├── deepswe.py         # Pier InstalledAgent（骨架，TODO(env)）
│   ├── ale.py             # 官方 Deployer 类（骨架，TODO(env)）
│   ├── toolathlon.py      # Decoupled 四步驱动（骨架，需 harness MCP-SSE 能力 TODO(mcp)）
│   └── generic.py         # 通用驱动（run_harness：隔离环境跑 harness + 超时 + 事件流）
└── results/               # 评测产物（gitignore）
    └── <run-id>/          # 一次评测 = 一个 run-id（时间戳）
        ├── meta.json      # 模型/版本/日期/API/采样参数/bench 列表
        ├── <bench>/<task-id>.json   # 每任务结构化结果
        ├── <bench>/<task-id>.jsonl  # harness --json 事件流（trajectory）
        └── report.md / report.json  # 汇总报告
```

## 快速开始（阶段 1 之后可用）

```bash
# 1. 准备：Python 3.12+、Docker、Linux 版 harness 二进制、各 runner 依赖
# 2. 配置（复制模板并填写）
cp eval/config.example.yaml eval/config.yaml   # 填模型/API/预算/并发

# 3. 跑评测（示例：NL2Repo 前 10 个任务）
python eval/orchestrator/run.py --config eval/config.yaml \
    --bench nl2repo --tasks 0-9 --run-id 2026-08-19-pilot

# 4. 出报告
python eval/orchestrator/report.py --run-id 2026-08-19-pilot
```

## 结果 schema（每任务）

```json
{
  "bench": "nl2repo",
  "task_id": "math-verify",
  "status": "pass|fail|error|timeout",
  "score": 0.0,
  "detail": { "passed": 3, "total": 5 },
  "tokens": { "input": 0, "output": 0, "cache_read": 0 },
  "cost_usd": 0.0,
  "wall_seconds": 0.0,
  "turns": 0,
  "trajectory": "results/<run-id>/nl2repo/<task-id>.jsonl",
  "exit_code": 0,
  "failure": "timeout|max_turns|env_error|model_error|other"
}
```

## 运行约定

- **隔离**：每任务独立 `HARNESS_HOME=<run-id>/home/<task>` + 独立工作目录；
  不污染 `~/.harness/`。
- **全自动**：配置播种 `approval.mode: bypass`；`--json` 事件流落盘为 trajectory。
- **协议对齐**：`--effort max`；`top_p=0.95` + `temperature=1.0`（harness
  改造后经配置注入）；模型固定 `deepseek-v4-flash`。
- **版本锁定**：runner pip 版本与任务子集记入 `meta.json`，可复现。

## 已知问题

Windows 宿主跑评测遇到的全部问题、修复与临时方案见
`eval/KNOWN_ISSUES.md`（pier CRLF 补丁、verifier 镜像预构建、git 身份等）。
重装环境/换机器时先读它。
