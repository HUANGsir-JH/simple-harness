"""util.py — harness --json 事件流解析 / 用量统计 / 失败归因。

约定：`harness run --json <prompt>` 输出 JSONL，每行一个回合级事件：
  {"type":"turn_start"|"thinking_delta"|"thinking_done"|"text_delta"|"text_done"
   |"tool_call"|"tool_result"|"turn_done"|"usage"|"notice"|"error", ...}
见 internal/events/events.go（simple-harness）。
"""
from __future__ import annotations

import json
from dataclasses import dataclass, field


@dataclass
class TrajectoryStats:
    turns: int = 0                    # tool_call 批次外的采样轮数（近似 = usage 事件数）
    tool_calls: int = 0
    tool_errors: int = 0
    text_blocks: int = 0
    last_text: str = ""               # 最后一个 text_done 的完整文本（最终答案）
    usage: dict = field(default_factory=dict)  # 累计 input/output/cache_read/cache_creation
    errors: list[str] = field(default_factory=list)
    events: int = 0


def parse_trajectory(path: str) -> TrajectoryStats:
    """解析 harness --json 事件流文件，返回统计。

    用法：编排器每任务把 harness stdout 落盘为 <task>.jsonl 后调用。
    """
    st = TrajectoryStats()
    with open(path, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                ev = json.loads(line)
            except json.JSONDecodeError:
                continue
            st.events += 1
            t = ev.get("type")
            if t == "usage":
                st.turns += 1
                u = ev.get("usage") or {}
                for k in ("input_tokens", "output_tokens", "cache_read_input_tokens",
                          "cache_creation_input_tokens"):
                    st.usage[k] = st.usage.get(k, 0) + (u.get(k) or 0)
            elif t == "tool_call":
                st.tool_calls += 1
            elif t == "tool_result":
                if ev.get("success") is False or ev.get("is_error"):
                    st.tool_errors += 1
            elif t == "text_done":
                st.text_blocks += 1
                st.last_text = ev.get("text", "")
            elif t == "error":
                st.errors.append(str(ev.get("err") or ev.get("error") or "unknown"))
    return st


def estimate_cost(usage: dict, pricing: dict) -> float:
    """按单价（USD / 1M tokens）估算成本。"""
    return (
        usage.get("input_tokens", 0) * pricing.get("input", 0)
        + usage.get("output_tokens", 0) * pricing.get("output", 0)
        + usage.get("cache_read_input_tokens", 0) * pricing.get("cache_read", 0)
        + usage.get("cache_creation_input_tokens", 0) * pricing.get("input", 0)
    ) / 1_000_000


def classify_failure(exit_code: int, timed_out: bool, max_turns_hit: bool,
                     trajectory: TrajectoryStats) -> str:
    """失败归因：timeout | max_turns | env_error | model_error | other。

    - timeout：外部超时 kill（进程被杀，exit_code 非 0 或 -9/-15）
    - max_turns：harness --max-turns 触发（事件流含对应 error 标记）
    - env_error：进程启动失败 / 非零退出且无 trajectory
    - model_error：事件流含 error 事件（API 层失败）
    """
    if timed_out:
        return "timeout"
    if max_turns_hit:
        return "max_turns"
    if exit_code != 0 and trajectory.events == 0:
        return "env_error"
    if trajectory.errors:
        return "model_error"
    return "other"


def final_answer(trajectory: TrajectoryStats) -> str:
    """最终答案 = 最后一个 text_done 文本（评测结果契约按 benchmark 定制）。"""
    return trajectory.last_text
