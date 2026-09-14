import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "pdf"))

from speed import alternating_medians


def recording_runner(log, label, seconds):
    samples = iter(seconds)

    def runner():
        log.append(label)
        return next(samples)

    return runner


class TestAlternatingProtocol:
    def test_engines_alternate_every_round(self):
        log = []
        runners = {
            "a": recording_runner(log, "a", [1, 1, 1, 1]),
            "b": recording_runner(log, "b", [2, 2, 2, 2]),
        }
        alternating_medians(runners, passes=3, warmup=1)
        assert log == ["a", "b", "a", "b", "a", "b", "a", "b"]

    def test_warmup_is_excluded_from_median(self):
        runners = {"a": recording_runner([], "a", [100.0, 3.0, 1.0, 2.0])}
        speed = alternating_medians(runners, passes=3, warmup=1)
        assert speed["a"]["samples"] == [3.0, 1.0, 2.0]
        assert speed["a"]["median_seconds"] == 2.0
        assert speed["a"]["warmup_seconds"] == [100.0]

    def test_default_protocol_is_five_passes_after_one_warmup(self):
        log = []
        runners = {"a": recording_runner(log, "a", [9.0, 5.0, 4.0, 3.0, 2.0, 1.0])}
        speed = alternating_medians(runners)
        assert len(log) == 6
        assert speed["a"]["median_seconds"] == 3.0
        assert speed["a"]["passes"] == 5
        assert speed["a"]["warmup"] == 1

    def test_at_least_one_measured_pass_required(self):
        with pytest.raises(ValueError):
            alternating_medians({"a": lambda: 1.0}, passes=0)
