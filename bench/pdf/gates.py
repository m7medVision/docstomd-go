"""Gate logic for bench evaluations: compare, report failures, injectable tests."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

SCORE_KEYS = (
    "overall_mean",
    "nid_mean",
    "nid_s_mean",
    "teds_mean",
    "teds_s_mean",
    "mhs_mean",
    "mhs_s_mean",
)

EPSILON = 1e-12


def load_evaluation(path: Path) -> dict[str, Any]:
    with path.open(encoding="utf-8") as handle:
        return json.load(handle)


def scores(evaluation: dict[str, Any]) -> dict[str, float]:
    score = evaluation.get("metrics", {}).get("score", {})
    return {key: float(score[key]) for key in SCORE_KEYS if score.get(key) is not None}


def document_scores(evaluation: dict[str, Any]) -> dict[str, float]:
    documents: dict[str, float] = {}
    for document in evaluation.get("documents", []):
        overall = document.get("scores", {}).get("overall")
        if overall is not None:
            documents[str(document["document_id"])] = float(overall)
    return documents


def missing_predictions(evaluation: dict[str, Any]) -> int:
    return int(evaluation.get("metrics", {}).get("missing_predictions", 0))


def compare(baseline: dict[str, Any], candidate: dict[str, Any]) -> dict[str, Any]:
    baseline_scores = scores(baseline)
    candidate_scores = scores(candidate)
    baseline_docs = document_scores(baseline)
    candidate_docs = document_scores(candidate)
    shared = sorted(baseline_docs.keys() & candidate_docs.keys())
    deltas = [
        {
            "document_id": doc_id,
            "baseline": baseline_docs[doc_id],
            "candidate": candidate_docs[doc_id],
            "delta": candidate_docs[doc_id] - baseline_docs[doc_id],
        }
        for doc_id in shared
    ]
    return {
        "baseline": baseline_scores,
        "candidate": candidate_scores,
        "deltas": {key: candidate_scores[key] - baseline_scores[key] for key in SCORE_KEYS if key in baseline_scores and key in candidate_scores},
        "missing": {
            "baseline": missing_predictions(baseline),
            "candidate": missing_predictions(candidate),
        },
        "documents": {
            "shared": len(shared),
            "improved": sum(item["delta"] > EPSILON for item in deltas),
            "regressed": sum(item["delta"] < -EPSILON for item in deltas),
            "unchanged": sum(abs(item["delta"]) <= EPSILON for item in deltas),
            "worst_regression": min(deltas, key=lambda item: item["delta"]) if deltas else None,
        },
    }


def evaluate_gates(
    comparison: dict[str, Any],
    *,
    min_overall_delta: float = 0.0,
    max_document_regression: float = 0.02,
    max_missing: int = 0,
) -> list[str]:
    failures: list[str] = []
    overall_delta = comparison["deltas"].get("overall_mean")
    if overall_delta is None or overall_delta < min_overall_delta:
        failures.append(f"overall delta {overall_delta!r} is below {min_overall_delta:+.6f}")
    candidate_missing = comparison["missing"]["candidate"]
    allowed_missing = comparison["missing"]["baseline"] + max_missing
    if candidate_missing > allowed_missing:
        failures.append(f"candidate has {candidate_missing} missing predictions (maximum {allowed_missing})")
    regression = comparison["documents"].get("worst_regression")
    if max_document_regression is not None and regression is not None and regression["delta"] < -max_document_regression:
        failures.append(
            "largest document regression "
            f"{regression['document_id']}={regression['delta']:+.6f} "
            f"exceeds -{max_document_regression:.6f}"
        )
    return failures


def format_speed(seconds: float | None) -> str:
    return "n/a" if seconds is None else f"{seconds:.3f}s"


def format_scores_table(rows: list[dict[str, Any]]) -> str:
    header = f"{'engine':<16} {'overall':>9} {'nid':>9} {'teds':>9} {'mhs':>9} {'missing':>8} {'speed':>10}"
    lines = [header, "-" * len(header)]
    for row in rows:
        lines.append(
            f"{row['engine']:<16} {row.get('overall_mean', float('nan')):>9.4f} "
            f"{row.get('nid_mean', float('nan')):>9.4f} {row.get('teds_mean', float('nan')):>9.4f} "
            f"{row.get('mhs_mean', float('nan')):>9.4f} {row.get('missing', 0):>8} "
            f"{format_speed(row.get('speed_seconds')):>10}"
        )
    return "\n".join(lines)


def format_comparison_table(rows: list[dict[str, Any]], subject: str) -> str:
    by_engine = {row["engine"]: row for row in rows}
    ours = by_engine[subject]
    header = f"{subject + ' vs':<16} {'overall':>9} {'nid':>9} {'teds':>9} {'mhs':>9} {'speedup':>10}"
    lines = [header, "-" * len(header)]
    for label, row in by_engine.items():
        if label == subject:
            continue
        deltas = " ".join(
            f"{ours.get(key, float('nan')) - row.get(key, float('nan')):>+9.4f}"
            for key in ("overall_mean", "nid_mean", "teds_mean", "mhs_mean")
        )
        ours_speed, their_speed = ours.get("speed_seconds"), row.get("speed_seconds")
        speedup = f"{their_speed / ours_speed:.1f}x" if ours_speed and their_speed else "n/a"
        lines.append(f"{label:<16} {deltas} {speedup:>10}")
    return "\n".join(lines)
