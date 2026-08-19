"""report.py — 评测结果汇总：results/<run-id>/ → report.md + report.json。

对比列 = 官方基线（DeepSeek-V4-Flash @ DeepSeek Harness 极简模式，2026-07-31
官方更新日志，见 docs/eval/BENCHMARKS.md）。
"""
from __future__ import annotations

import argparse
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent  # eval/

# 官方基线（deepseek-v4-flash @ DeepSeek Harness minimal mode，2026-07-31）
REFERENCE = {
    "terminal-bench": {"score": 82.7, "note": "Terminal-Bench 2.1"},
    "nl2repo": {"score": 54.2, "note": "NL2Repo"},
    "cybergym": {"score": 76.7, "note": "Cybergym"},
    "deepswe": {"score": 54.4, "note": "DeepSWE"},
    "toolathlon": {"score": 70.3, "note": "Toolathlon verified"},
    "ale": {"score": 25.2, "note": "Agent Last Exam"},
    "automation-bench": {"score": 25.1, "note": "Automation Bench (Public) — 无 CLI 接入路径，仅记录"},
}


def load_results(run_dir: Path) -> dict[str, list[dict]]:
    out: dict[str, list[dict]] = {}
    for bench_dir in sorted(p for p in run_dir.iterdir() if p.is_dir()):
        results = []
        for f in sorted(bench_dir.glob("*.json")):
            if f.name in ("meta.json", "summary.json"):
                continue
            try:
                results.append(json.loads(f.read_text(encoding="utf-8")))
            except json.JSONDecodeError:
                pass
        if results:
            out[bench_dir.name] = results
    return out


def bench_score(results: list[dict]) -> float:
    """官方口径 pass 率 = status=pass 的占比（各 runner 的 score 字段口径见 adapter）。"""
    if not results:
        return 0.0
    return 100.0 * sum(1 for r in results if r.get("status") == "pass") / len(results)


def gen_markdown(run_dir: Path, by_bench: dict[str, list[dict]], meta: dict) -> str:
    lines = []
    lines.append(f"# simple-harness 评测报告 — {run_dir.name}\n")
    m = meta.get("model", {})
    lines.append(f"- 模型: `{m.get('model', '?')}`  effort={m.get('effort')} "
                 f"top_p={m.get('top_p')} temperature={m.get('temperature')}")
    lines.append(f"- harness: `{meta.get('harness_bin', '?')}` "
                 f"version={m.get('harness_version', '?')}")
    lines.append(f"- 基线: DeepSeek-V4-Flash @ DeepSeek Harness 极简模式 "
                 f"(官方更新日志 2026-07-31)\n")
    lines.append("| Benchmark | 任务数 | 我们的得分 | 官方基线 | Δ | 成本$ | 总耗时 | 失败归类 |")
    lines.append("|---|---|---|---|---|---|---|---|")
    for bench, results in sorted(by_bench.items()):
        ref = REFERENCE.get(bench)
        score = bench_score(results)
        cost = sum(r.get("cost_usd", 0) or 0 for r in results)
        wall = sum(r.get("wall_seconds", 0) or 0 for r in results)
        fails: dict[str, int] = {}
        for r in results:
            if r.get("status") != "pass":
                k = r.get("failure", "other")
                fails[k] = fails.get(k, 0) + 1
        fail_str = ", ".join(f"{k}:{v}" for k, v in sorted(fails.items())) or "-"
        if ref:
            delta = score - ref["score"]
            ref_s = f"{ref['score']} ({ref['note']})"
            delta_s = f"{delta:+.1f}"
        else:
            ref_s, delta_s = "-", "-"
        lines.append(f"| {bench} | {len(results)} | **{score:.1f}** | {ref_s} | "
                     f"{delta_s} | {cost:.2f} | {wall/60:.0f}min | {fail_str} |")
    lines.append("\n## 失败任务明细\n")
    for bench, results in sorted(by_bench.items()):
        bad = [r for r in results if r.get("status") != "pass"]
        if not bad:
            continue
        lines.append(f"### {bench}")
        for r in bad:
            lines.append(f"- `{r.get('task_id')}` [{r.get('failure')}] "
                         f"score={r.get('score')} {r.get('detail', '')[:120]}")
    lines.append("\n## 附注\n")
    lines.append("- Automation Bench 官方 runner 仅支持 API 驱动（进程内模拟环境），"
                 "无自定义 CLI 接入路径，不参与 harness 对比。")
    lines.append("- 采样参数对齐官方协议：max effort / top_p=0.95 / temperature=1.0。")
    lines.append("- 每任务 trajectory（--json 事件流）存于 results/<run-id>/<bench>/<task>.jsonl。")
    return "\n".join(lines) + "\n"


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--run-id", required=True)
    ap.add_argument("--results-root", default=str(ROOT / "results"))
    args = ap.parse_args()

    run_dir = Path(args.results_root) / args.run_id
    if not run_dir.is_dir():
        raise SystemExit(f"run 目录不存在: {run_dir}")
    meta = json.loads((run_dir / "meta.json").read_text(encoding="utf-8"))
    by_bench = load_results(run_dir)

    report_md = gen_markdown(run_dir, by_bench, meta)
    (run_dir / "report.md").write_text(report_md, encoding="utf-8")

    report_json = {
        "run_id": args.run_id,
        "meta": meta,
        "benchmarks": {
            b: {
                "n": len(rs),
                "score": bench_score(rs),
                "reference": REFERENCE.get(b),
                "cost_usd": round(sum(r.get("cost_usd", 0) or 0 for r in rs), 4),
                "wall_s": round(sum(r.get("wall_seconds", 0) or 0 for r in rs), 1),
                "results": rs,
            }
            for b, rs in by_bench.items()
        },
    }
    (run_dir / "report.json").write_text(
        json.dumps(report_json, ensure_ascii=False, indent=2), encoding="utf-8")
    print(report_md)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
