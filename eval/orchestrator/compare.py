"""compare.py — 回归对比：多个 run-id 的趋势报告（版本变更后一键对比）。

用法：
  python eval/orchestrator/compare.py --runs 2026-08-19-pilot 2026-09-01-pilot
  python eval/orchestrator/compare.py --latest 5   # 最近 5 次 run

输出：results/trend.md（表格：日期 / bench / 得分 / vs 官方基线 Δ / 成本 /
任务数）+ results/trend.json（全量结构化，供后续分析）。
"""
from __future__ import annotations

import argparse
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent  # eval/

# 与 report.py 同源的官方基线（deepseek-v4-flash @ DeepSeek Harness 极简模式）
REFERENCE = {
    "terminal-bench": 82.7,
    "nl2repo": 54.2,
    "cybergym": 76.7,
    "deepswe": 54.4,
    "toolathlon": 70.3,
    "ale": 25.2,
    "automation-bench": 25.1,
}


def load_run(run_dir: Path) -> dict | None:
    meta_p = run_dir / "meta.json"
    if not meta_p.is_file():
        return None
    meta = json.loads(meta_p.read_text(encoding="utf-8"))
    summary_p = run_dir / "summary.json"
    summary = json.loads(summary_p.read_text(encoding="utf-8")) if summary_p.is_file() else {}
    bench = summary.get("bench") or meta.get("bench") or "?"
    # 分数口径：pass 率（report.py bench_score 同款）；runner 的 score 字段兜底
    n = summary.get("n_tasks", 0)
    n_pass = summary.get("n_pass", 0)
    score = 100.0 * n_pass / n if n else None
    return {
        "run_id": run_dir.name,
        "started": meta.get("started_at", ""),
        "bench": bench,
        "n_tasks": n,
        "score": score,
        "cost_usd": summary.get("total_cost_usd", 0),
        "wall_s": summary.get("total_wall_s", 0),
        "model": (meta.get("model") or {}).get("model", "?"),
        "harness_version": (meta.get("model") or {}).get("harness_version", "?"),
    }


def gen_trend(runs: list[dict]) -> tuple[str, dict]:
    lines = ["# simple-harness 评测趋势\n"]
    lines.append("| run-id | 日期 | bench | 任务数 | 得分 | 官方基线 | Δ | 成本$ | 总耗时 |")
    lines.append("|---|---|---|---|---|---|---|---|---|")
    trend: dict[str, list] = {}
    for r in runs:
        ref = REFERENCE.get(r["bench"])
        delta = f"{r['score'] - ref:+.1f}" if (ref is not None and r["score"] is not None) else "-"
        score_s = f"{r['score']:.1f}" if r["score"] is not None else "-"
        lines.append(
            f"| {r['run_id']} | {r['started'][:10]} | {r['bench']} | {r['n_tasks']} | "
            f"**{score_s}** | {ref if ref is not None else '-'} | {delta} | "
            f"{r['cost_usd']:.2f} | {r['wall_s']/60:.0f}min |")
        trend.setdefault(r["bench"], []).append(r)
    # 每 bench 的连续变化
    for bench, rs in sorted(trend.items()):
        if len(rs) >= 2:
            deltas = []
            for prev, cur in zip(rs, rs[1:]):
                if prev["score"] is not None and cur["score"] is not None:
                    deltas.append(f"{cur['score'] - prev['score']:+.1f}")
            if deltas:
                lines.append(f"\n- {bench} 连续变化: {' → '.join(deltas)}")
    return "\n".join(lines) + "\n", trend


def main() -> int:
    ap = argparse.ArgumentParser(description="eval trend compare")
    ap.add_argument("--runs", nargs="*", default=[])
    ap.add_argument("--latest", type=int, default=0, help="取最近 N 次 run（按目录名）")
    ap.add_argument("--results-root", default=str(ROOT / "results"))
    args = ap.parse_args()

    root = Path(args.results_root)
    if not root.is_dir():
        raise SystemExit(f"results 目录不存在: {root}")
    run_ids = list(args.runs)
    if not run_ids and args.latest > 0:
        run_ids = sorted(p.name for p in root.iterdir() if p.is_dir())[-args.latest:]
    if not run_ids:
        raise SystemExit("请提供 --runs 或 --latest N")

    runs = []
    for rid in run_ids:
        r = load_run(root / rid)
        if r:
            runs.append(r)
        else:
            print(f"!! {rid}: meta.json 缺失，跳过")
    if not runs:
        raise SystemExit("无可用 run")

    md, trend = gen_trend(runs)
    (root / "trend.md").write_text(md, encoding="utf-8")
    (root / "trend.json").write_text(
        json.dumps(trend, ensure_ascii=False, indent=2), encoding="utf-8")
    print(md)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
