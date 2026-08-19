"""generic.py — 通用 S2 驱动：在隔离工作目录跑 `harness run <prompt>`。

每个 benchmark 的 adapter 要么直接复用本驱动的 run_task，要么在外部官方
runner 里调用本驱动的 run_harness()（如 Cybergym submit.sh 模式、ALE
Deployer、Pier InstalledAgent 都复用同一函数）。

契约：
- 环境变量：HARNESS_HOME=<隔离 home>（含 config.yaml：provider + bypass）
- 命令：<harness_bin> run --json [--effort max] [--max-turns N] <prompt>
- 输出：--json 事件流 → traj_path（JSONL）；退出码 + 超时 → 归因
"""
from __future__ import annotations

import json
import os
import subprocess
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
from orchestrator import util  # noqa: E402


def run_harness(prompt: str, harness_bin: str, home: str, workdir: str,
                traj_path: str, cfg: dict, env_extra: dict | None = None,
                timeout_s: float | None = None) -> dict:
    """跑一次 harness run，返回结构化结果（不含 bench 评分）。"""
    budget = cfg.get("budget", {})
    timeout_s = timeout_s or budget.get("per_task_timeout_s", 3600)
    max_turns = budget.get("max_turns", 0)

    cmd = [harness_bin, "run", "--json"]
    if max_turns:
        cmd += [f"--max-turns={max_turns}"]
    cmd += [prompt]

    env = dict(os.environ)
    env["HARNESS_HOME"] = home
    env.update(env_extra or {})

    started = time.time()
    timed_out = False
    try:
        proc = subprocess.run(
            cmd, cwd=workdir, env=env, capture_output=True, text=True,
            timeout=timeout_s,
        )
        exit_code = proc.returncode
        stdout = proc.stdout
    except subprocess.TimeoutExpired as e:
        timed_out = True
        exit_code = -9
        stdout = (e.stdout or b"").decode(errors="replace") if isinstance(
            e.stdout, bytes) else (e.stdout or "")

    Path(traj_path).write_text(stdout, encoding="utf-8", errors="replace")
    st = util.parse_trajectory(traj_path)
    cost = util.estimate_cost(st.usage, cfg.get("pricing", {}))
    failure = util.classify_failure(exit_code, timed_out, False, st)

    out = {
        "status": "pass" if not timed_out and exit_code == 0 else "error",
        "score": 0.0,
        "failure": failure,
        "exit_code": exit_code,
        "tokens": st.usage,
        "cost_usd": round(cost, 6),
        "turns": st.turns,
        "tool_calls": st.tool_calls,
        "answer": util.final_answer(st),
        "detail": "",
    }
    if timed_out:
        out["detail"] = f"timeout after {timeout_s}s"
    elif exit_code != 0:
        out["detail"] = f"exit {exit_code}"
    elif st.errors:
        out["detail"] = "; ".join(st.errors[:3])
    return out


def list_tasks(cfg: dict) -> list[dict]:
    """generic 驱动没有自带任务集；具体 benchmark 的 adapter 覆写此函数。"""
    raise NotImplementedError("generic 驱动不直接评测，需用具体 benchmark adapter")


def run_task(task: dict, cfg: dict, harness_bin: str, home: str, workdir: str,
             traj_path: str) -> dict:
    """直接按 task.prompt 跑（适用于 prompt-in/answer-out 的简单任务）。"""
    prompt = task.get("prompt") or task.get("instruction") or ""
    if not prompt:
        return {"status": "error", "failure": "env_error", "detail": "task 无 prompt"}
    out = run_harness(prompt, harness_bin, home, workdir, traj_path, cfg)
    # 结果契约：任务规定答案写 workdir 下某文件时，adapter 在此处评分
    grader = task.get("grader")
    if grader and out["status"] == "pass":
        try:
            subprocess.run(grader, shell=True, cwd=workdir, capture_output=True,
                           text=True, timeout=120, check=True)
            out["score"] = 1.0
        except Exception as e:
            out["status"] = "fail"
            out["failure"] = "other"
            out["detail"] = f"grader failed: {e}"
    return out
