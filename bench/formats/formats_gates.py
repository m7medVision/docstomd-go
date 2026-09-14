"""Formats bench gates: per-format trigram containment against committed targets and baseline."""

from __future__ import annotations

DIRECTIONS = ("ours_in_baseline", "baseline_in_ours")
DEFAULT_MAX_REGRESSION = 0.02


def same_machine(recorded: dict, current: dict) -> bool:
    return all(recorded.get(key) == current[key] for key in ("hostname", "machine", "cpus"))


def check(
    summary: dict[str, dict],
    committed: dict | None,
    fingerprint: dict,
    corpus_digest: str,
    *,
    baseline_available: bool,
    max_regression: float = DEFAULT_MAX_REGRESSION,
) -> dict[str, dict]:
    results = {}
    for fmt in sorted(summary):
        if committed is None:
            results[fmt] = {"failures": [], "skipped": "no committed baseline"}
            continue
        if not baseline_available:
            results[fmt] = {"failures": [], "skipped": "baseline engine unavailable"}
            continue
        candidate = summary[fmt]
        failures = []
        for direction, target in committed.get("targets", {}).get(fmt, {}).items():
            key = direction.removeprefix("min_")
            value = candidate.get(key)
            if value is None:
                failures.append(f"{key}: no containment measured (target {target:.4f})")
            elif value < target:
                failures.append(f"{key} {value:.4f} is below the committed target {target:.4f}")
        result = {"failures": failures}
        recorded = committed.get("formats", {}).get(fmt)
        if recorded is None:
            result["note"] = "no recorded containment for this format"
        elif not same_machine(committed.get("fingerprint", {}), fingerprint):
            result["note"] = "baseline recorded on another machine; regression check skipped"
        elif committed.get("corpus", {}).get("digest") != corpus_digest:
            result["note"] = "corpus changed since the baseline; regression check skipped"
        else:
            failures += regressions("", recorded, candidate, max_regression)
            candidates = candidate.get("per_document", {})
            for doc_id, before in sorted(recorded.get("per_document", {}).items()):
                failures += regressions(f"{doc_id} ", before, candidates.get(doc_id, {}), max_regression)
        results[fmt] = result
    return results


def regressions(prefix: str, recorded: dict, candidate: dict, max_regression: float) -> list[str]:
    failures = []
    for key in DIRECTIONS:
        before, after = recorded.get(key), candidate.get(key)
        if before is None:
            continue
        if after is None or after < before - max_regression:
            failures.append(f"{prefix}{key} regressed from {before:.4f} to {after if after is None else round(after, 4)}")
    return failures
