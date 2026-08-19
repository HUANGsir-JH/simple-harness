"""toolathlon.py — Toolathlon（hkust-nlp）适配器：本地 Decoupled 模式驱动。

接入方式（调研结论，见 docs/eval/BENCHMARKS.md §5）：
- 官方服务 = 平台制 + OpenAI 兼容 API（服务端固定 scaffold），无 --agent-command
- 唯一 CLI 路径 = **本地部署 + Decoupled Agent Loop**：
    1. 容器 preprocess → 启动 MCP gateway（scripts/decoupled/container_tool_gateway.py，
       SSE 端点 http://127.0.0.1:<port>/sse）
    2. host 拿任务 bundle（task_str=指令 / agent_workspace / log_file）+ gateway URL
    3. agent 在 workspace 干活（工具经 MCP-SSE gateway 暴露）
    4. container_eval.py 读轨迹 JSONL + --agent_exit_code 评分（确定性，检查环境状态）
- **⚠️ 硬门槛：harness 需支持 MCP over SSE 客户端**（32 MCP server / 604 工具）——
  当前未实现，本适配器标注 MCP 接入点（TODO(mcp)）；能力落地前不可实机运行
- "verified" = 2026-06-30 最终校准版（非人工验证流程）；108 任务 / 3 次采样 /
  pass@1±std / 单任务上限 5400s / max turns 100

本文件被编排器以 `runners.toolathlon` 加载：list_tasks / run_task。
"""
from __future__ import annotations

import json
import shlex
import shutil
import subprocess
import time
from pathlib import Path


def _checkout(cfg: dict) -> Path:
    extra = cfg.get("benchmarks", {}).get("toolathlon", {}).get("extra", {})
    p = Path(extra.get("checkout", ""))
    if not p.is_dir():
        raise SystemExit(
            f"toolathlon: checkout 不存在: {p}（git clone github.com/hkust-nlp/Toolathlon）")
    return p


def list_tasks(cfg: dict) -> list[dict]:
    """列任务：tasks/finalpool/ 下 108 个任务目录（109 条目含 task_conflict.json）。"""
    co = _checkout(cfg)
    pool = co / "tasks" / "finalpool"
    if not pool.is_dir():
        raise SystemExit(f"toolathlon: 任务池不存在: {pool}")
    tasks = []
    for d in sorted(p for p in pool.iterdir() if p.is_dir()):
        # 每个任务目录含 task 配置（task.json 或 bundle）；跳过冲突说明文件
        if d.name == "task_conflict.json":
            continue
        tasks.append({"id": d.name, "title": d.name, "dir": str(d)})
    if not tasks:
        raise SystemExit(f"toolathlon: finalpool 为空（{pool}）")
    return tasks


def run_task(task: dict, cfg: dict, harness_bin: str, home: str, workdir: str,
             traj_path: str) -> dict:
    """单任务：decoupled 四步（preprocess → gateway → harness run → eval）。

    依赖（阶段 2 就绪项）：
      - Toolathlon checkout + 环境（install_env_minimal.sh、任务镜像
        docker.io/lockon0927/toolathlon-task-image:1016beta）
      - 应用账号凭据（configs/token_key_session.py）+ deploy_containers.sh 部署
      - **harness MCP-SSE 客户端能力（TODO(mcp)，当前未实现）**
    """
    extra = cfg.get("benchmarks", {}).get("toolathlon", {}).get("extra", {})
    co = _checkout(cfg)
    task_id = str(task["id"])
    started = time.time()

    # 1) preprocess + gateway（TODO(env): 按 scripts/run_single_decoupled.sh /
    #    container_tool_gateway.py 实际参数核对；产出 bundle JSON + SSE URL）。
    bundle_path = Path(workdir) / "bundle.json"
    gateway = subprocess.run(
        ["bash", str(co / "scripts" / "run_single_decoupled.sh"),
         "--task", task_id, "--framework", "simple_harness",
         "--output", str(bundle_path)],
        capture_output=True, text=True, timeout=1800)
    if gateway.returncode != 0 or not bundle_path.is_file():
        return {"status": "error", "failure": "env_error",
                "detail": f"preprocess/gateway 失败: {gateway.stderr[-800:]}"}
    bundle = json.loads(bundle_path.read_text(encoding="utf-8"))
    task_str = bundle.get("task_str", "")
    workspace = bundle.get("host_paths", {}).get("agent_workspace", workdir)
    log_file = bundle.get("container_paths", {}).get("log_file", "")

    # 2) harness run（工具经 MCP-SSE gateway —— TODO(mcp): 接入点。
    #    MCP 能力落地前，此步退化为"无工具直跑"，实机不可用）。
    mcp_env = {"TOOLATHLON_GATEWAY_SSE": extra.get("gateway_sse", ""),
               "TOOLATHLON_BUNDLE": str(bundle_path)}
    proc = subprocess.run(
        [harness_bin, "run", "--json", "--effort", "max",
         "--max-turns", str(extra.get("max_turns", 100)),
         task_str],
        cwd=workspace, env={**mcp_env}, capture_output=True, text=True,
        timeout=int(extra.get("timeout_s", 5400)))
    Path(traj_path).write_text(proc.stdout, encoding="utf-8", errors="replace")

    # 3) 轨迹 JSONL（Toolathlon dump 格式：config/status/messages）+
    #    agent_exit_code —— TODO(env): 按 host_agent_loop.py/container_eval.py
    #    的格式要求转换 harness 事件流；骨架先直接落原始事件流。
    if log_file:
        Path(log_file).parent.mkdir(parents=True, exist_ok=True)
        Path(log_file).write_text(proc.stdout, encoding="utf-8", errors="replace")

    # 4) 容器内评分（确定性 eval，检查环境状态）
    eval_cmd = subprocess.run(
        ["bash", str(co / "scripts" / "decoupled" / "container_eval.py"),
         "--bundle", str(bundle_path), "--agent_exit_code", str(proc.returncode)],
        capture_output=True, text=True, timeout=1800)
    # TODO(env): 解析 eval 输出（pass@1 判定）；骨架先粗判。
    ok = proc.returncode == 0 and eval_cmd.returncode == 0
    return {
        "status": "pass" if ok else "error",
        "score": 1.0 if ok else 0.0,
        "failure": "" if ok else (
            f"harness exit {proc.returncode} / eval exit {eval_cmd.returncode}"),
        "exit_code": proc.returncode,
        "detail": (eval_cmd.stdout or eval_cmd.stderr)[-1000:],
        "wall_seconds": round(time.time() - started, 2),
    }
