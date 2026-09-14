"""Speed protocol: warm-up rounds excluded, then alternating measured passes, median per engine."""

from __future__ import annotations

import statistics
from typing import Callable

DEFAULT_PASSES = 5
DEFAULT_WARMUP = 1


def alternating_medians(
    runners: dict[str, Callable[[], float]],
    passes: int = DEFAULT_PASSES,
    warmup: int = DEFAULT_WARMUP,
) -> dict[str, dict]:
    if passes < 1:
        raise ValueError(f"speed protocol needs at least one measured pass, got {passes}")
    warmups: dict[str, list[float]] = {label: [] for label in runners}
    samples: dict[str, list[float]] = {label: [] for label in runners}
    for round_index in range(warmup + passes):
        for label, runner in runners.items():
            seconds = float(runner())
            (warmups if round_index < warmup else samples)[label].append(seconds)
    return {
        label: {
            "warmup": warmup,
            "passes": passes,
            "warmup_seconds": warmups[label],
            "samples": samples[label],
            "median_seconds": statistics.median(samples[label]),
        }
        for label in runners
    }
