"""run.py — 评测编排器入口。

用法：
  python eval/orchestrator/run.py --config eval/config.yaml \
      --bench nl2repo --tasks 0-9 --run-id 2026-08-19-pilot

流程（每任务）：
  1. 建隔离目录：results/<run-id>/home/<task>（HARNESS_HOME）+ 工作目录
  2. 生成 HARNESS_HOME/config.yaml（模型/审批 bypass/采样参数）
  3. 调 runners/<bench>.py 的 run_task()（封装官方 runner，内部跑 harness）
  4. 收集每任务结果 → results/<run-id>/<bench>/<task>.json + .jsonl trajectory
  5. 写 meta.json（模型/版本/协议/预算）

并发：--max-concurrent（默认 1，跑通后再并行）；成本熔断：累计成本超
cost_cap_usd 即停。失败重试：仅 env_error 重试一次。
"""
from __future__ import annotations

import argparse
import importlib
import json
import os
import shutil
import subprocess
import sys
import time
import uuid
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parent.parent  # eval/
RUNNERS = ROOT / "runners"
sys.path.insert(0, str(ROOT))  # 使 `import runners.<bench>` 可解析


def load_config(path: Path) -> dict:
    with open(path, encoding="utf-8") as f:
        return yaml.safe_load(f)


def write_harness_config(home: Path, cfg: dict) -> None:
    """生成 HARNESS_HOME/config.yaml：provider/模型/审批 bypass/采样参数。

    采样参数（top_p/temperature）在 harness 支持后经 provider config 注入；
    当前版本先写占位，未支持则忽略。
    """
    hcfg = cfg.get("harness_config", {})
    doc = {
        "default_provider": hcfg.get("default_provider", "deepseek"),
        "providers": hcfg.get("providers", {}),
        "approval": hcfg.get("approval", {"mode": "bypass"}),
    }
    (home / "config.yaml").write_text(
        yaml.safe_dump(doc, allow_unicode=True), encoding="utf-8"
    )


def run_single(bench: str, task: dict, cfg: dict, run_dir: Path, home_root: Path) -> dict:
    """跑单个任务：隔离目录 → runner.run_task() → 结果落盘。"""
    task_id = str(task["id"])
    task_dir = run_dir / bench
    task_dir.mkdir(parents=True, exist_ok=True)

    home = home_root / f"{bench}-{task_id}"
    home.mkdir(parents=True, exist_ok=True)
    write_harness_config(home, cfg)

    workdir = task.get("workdir") or (run_dir / "work" / f"{bench}-{task_id}")
    workdir.mkdir(parents=True, exist_ok=True)

    result_path = task_dir / f"{task_id}.json"
    traj_path = task_dir / f"{task_id}.jsonl"

    started = time.time()
    try:
        runner = importlib.import_module(f"runners.{bench}") if bench != "generic" \
            else importlib.import_module("runners.generic")
        out = runner.run_task(
            task=task,
            cfg=cfg,
            harness_bin=str(cfg["harness_bin"]),
            home=str(home),
            workdir=str(workdir),
            traj_path=str(traj_path),
        )
    except Exception as e:  # runner 自身崩溃 = env_error
        out = {"status": "error", "failure": "env_error", "detail": str(e)}
    out["wall_seconds"] = round(time.time() - started, 2)
    out["task_id"] = task_id
    out["bench"] = bench

    result_path.write_text(json.dumps(out, ensure_ascii=False, indent=2), encoding="utf-8")
    return out


def load_tasks(bench: str, spec: dict, cfg: dict) -> list[dict]:
    """从 runner 模块取任务清单，按 spec.tasks 切子集。"""
    try:
        runner = importlib.import_module(f"runners.{bench}")
    except ModuleNotFoundError:
        raise SystemExit(f"runners/{bench}.py 尚未实现（调研/接入未完成）")
    tasks = runner.list_tasks(cfg)
    rng = spec.get("tasks") or []
    if rng:
        if len(rng) == 2:
            tasks = tasks[rng[0]:rng[1]]
        else:
            tasks = [t for t in tasks if t["id"] in rng or int(t["id"]) in rng]
    if not tasks:
        raise SystemExit(f"{bench}: 任务子集为空")
    return tasks


def main() -> int:
    ap = argparse.ArgumentParser(description="simple-harness eval orchestrator")
    ap.add_argument("--config", required=True)
    ap.add_argument("--bench", required=True)
    ap.add_argument("--tasks", default="", help="如 0-9 或逗号列表；空 = 配置里的子集")
    ap.add_argument("--run-id", default="")
    ap.add_argument("--max-concurrent", type=int, default=1)
    ap.add_argument("--dry-run", action="store_true", help="只列任务不执行")
    args = ap.parse_args()

    cfg = load_config(Path(args.config))
    bench = args.bench
    spec = cfg["benchmarks"].get(bench)
    if spec is None:
        raise SystemExit(f"config 里没有 benchmark: {bench}")

    run_id = args.run_id or time.strftime("%Y%m%d-%H%M%S")
    results_root = ROOT / "results" / run_id
    home_root = results_root / "home"
    tasks = load_tasks(bench, spec, cfg)
    if args.tasks:
        if "-" in args.tasks and not any(c.isalpha() for c in args.tasks):
            a, b = args.tasks.split("-")
            tasks = tasks[int(a):int(b)]
        else:
            ids = [x.strip() for x in args.tasks.split(",") if x.strip()]
            tasks = [t for t in tasks
                     if t["id"] in ids
                     or (t["id"].isdigit() and int(t["id"]) in {int(x) for x in ids if x.isdigit()})]
    print(f"[{bench}] run-id={run_id} tasks={len(tasks)}")

    meta = {
        "run_id": run_id,
        "bench": bench,
        "started_at": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
        "model": cfg.get("meta", {}),
        "harness_bin": cfg.get("harness_bin"),
        "budget": cfg.get("budget", {}),
        "python": sys.version.split()[0],
    }
    os.makedirs(results_root, exist_ok=True)
    (results_root / "meta.json").write_text(
        json.dumps(meta, ensure_ascii=False, indent=2), encoding="utf-8")

    if args.dry_run:
        for t in tasks:
            print(f"  {bench}/{t['id']}  {t.get('title', '')[:80]}")
        return 0

    os.makedirs(home_root, exist_ok=True)
    results: list[dict] = []
    cost_cap = cfg.get("budget", {}).get("cost_cap_usd", 0)
    total_cost = 0.0
    pricing = cfg.get("pricing", {})
    max_concurrent = max(1, args.max_concurrent)

    with ThreadPoolExecutor(max_workers=max_concurrent) as ex:
        futs = {ex.submit(run_single, bench, t, cfg, results_root, home_root): t
                for t in tasks}
        for fut in as_completed(futs):
            out = fut.result()
            results.append(out)
            tc = out.get("cost_usd", 0) or 0
            total_cost += tc
            print(f"  [{out.get('status','?')}] {out.get('task_id')} "
                  f"score={out.get('score')} ${tc:.3f} "
                  f"{out.get('wall_seconds',0):.0f}s {out.get('failure','')}")
            if cost_cap and total_cost >= cost_cap:
                print(f"!! cost cap ${cost_cap} hit at ${total_cost:.2f}; aborting")
                for f in futs:
                    f.cancel()
                break

    summary = {
        "bench": bench,
        "run_id": run_id,
        "n_tasks": len(results),
        "n_pass": sum(1 for r in results if r.get("status") == "pass"),
        "total_cost_usd": round(total_cost, 4),
        "total_wall_s": round(sum(r.get("wall_seconds", 0) for r in results), 1),
        "status_counts": {},
    }
    for r in results:
        s = r.get("status", "?")
        summary["status_counts"][s] = summary["status_counts"].get(s, 0) + 1
    (results_root / "summary.json").write_text(
        json.dumps(summary, ensure_ascii=False, indent=2), encoding="utf-8")
    print(json.dumps(summary, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
