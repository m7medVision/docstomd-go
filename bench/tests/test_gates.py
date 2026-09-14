import copy
import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "pdf"))

from gates import compare, evaluate_gates, format_comparison_table, format_scores_table, scores


def make_evaluation(doc_scores=None, overall=0.5, missing=0):
    doc_scores = doc_scores or {}
    return {
        "metrics": {
            "score": {
                "overall_mean": overall,
                "nid_mean": 0.9,
                "teds_mean": 0.8,
                "mhs_mean": 0.7,
            },
            "missing_predictions": missing,
        },
        "documents": [
            {"document_id": doc_id, "scores": {"overall": value}}
            for doc_id, value in doc_scores.items()
        ],
    }


class TestIdenticalEvaluationsPass:
    def test_no_failures_when_identical(self):
        baseline = make_evaluation(doc_scores={"a": 0.5, "b": 0.6})
        comparison = compare(baseline, copy.deepcopy(baseline))
        assert evaluate_gates(comparison) == []

    def test_zero_delta_counts_as_pass(self):
        baseline = make_evaluation(doc_scores={"a": 0.5})
        comparison = compare(baseline, copy.deepcopy(baseline))
        assert comparison["deltas"]["overall_mean"] == 0.0


class TestInjectedRegressionFails:
    def test_overall_regression_fails_gate(self):
        baseline = make_evaluation(overall=0.8, doc_scores={"a": 0.9})
        candidate = make_evaluation(overall=0.6, doc_scores={"a": 0.4})
        comparison = compare(baseline, candidate)
        failures = evaluate_gates(comparison)
        assert any("overall delta" in f for f in failures)
        assert any("document regression" in f for f in failures)

    def test_small_document_regression_within_bound_passes(self):
        baseline = make_evaluation(doc_scores={"a": 0.90})
        candidate = make_evaluation(doc_scores={"a": 0.885})
        comparison = compare(baseline, candidate)
        assert evaluate_gates(comparison, max_document_regression=0.02) == []

    def test_missing_predictions_fail_gate(self):
        baseline = make_evaluation(missing=0)
        candidate = make_evaluation(missing=3)
        comparison = compare(baseline, candidate)
        failures = evaluate_gates(comparison)
        assert any("missing predictions" in f for f in failures)

    def test_missing_predictions_already_in_baseline_pass(self):
        baseline = make_evaluation(missing=3)
        comparison = compare(baseline, copy.deepcopy(baseline))
        assert evaluate_gates(comparison) == []

    def test_new_missing_prediction_beyond_baseline_fails(self):
        baseline = make_evaluation(missing=3)
        candidate = make_evaluation(missing=4)
        failures = evaluate_gates(compare(baseline, candidate))
        assert any("4 missing predictions" in f for f in failures)

    def test_improvement_passes(self):
        baseline = make_evaluation(overall=0.5, doc_scores={"a": 0.5})
        candidate = make_evaluation(overall=0.6, doc_scores={"a": 0.7})
        comparison = compare(baseline, candidate)
        assert evaluate_gates(comparison) == []


class TestComparisonShape:
    def test_scores_extraction(self):
        evaluation = make_evaluation()
        extracted = scores(evaluation)
        assert extracted["overall_mean"] == 0.5
        assert extracted["nid_mean"] == 0.9

    def test_document_counts(self):
        baseline = make_evaluation(doc_scores={"a": 0.5, "b": 0.6, "c": 0.7})
        candidate = make_evaluation(doc_scores={"a": 0.4, "b": 0.6, "c": 0.8})
        comparison = compare(baseline, candidate)
        assert comparison["documents"]["shared"] == 3
        assert comparison["documents"]["improved"] == 1
        assert comparison["documents"]["regressed"] == 1
        assert comparison["documents"]["unchanged"] == 1

    def test_disjoint_documents_report_zero_shared(self):
        baseline = make_evaluation(doc_scores={"a": 0.5})
        candidate = make_evaluation(doc_scores={"z": 0.5})
        comparison = compare(baseline, candidate)
        assert comparison["documents"]["shared"] == 0


class TestReportTable:
    def test_table_renders_rows(self):
        table = format_scores_table([
            {"engine": "docstomd", "overall_mean": 0.8, "nid_mean": 0.9, "teds_mean": 0.8, "mhs_mean": 0.7, "missing": 0, "speed_seconds": 1.2},
            {"engine": "markitdown", "overall_mean": 0.6, "nid_mean": 0.8, "teds_mean": 0.3, "mhs_mean": 0.0, "missing": 2, "speed_seconds": 9.0},
        ])
        assert "docstomd" in table and "markitdown" in table
        assert "overall" in table
        assert "1.200s" in table and "9.000s" in table

    def test_missing_speed_renders_na(self):
        table = format_scores_table([{"engine": "docstomd", "overall_mean": 0.8}])
        assert "n/a" in table


class TestComparisonTable:
    rows = [
        {"engine": "docstomd", "overall_mean": 0.80, "nid_mean": 0.90, "teds_mean": 0.50, "mhs_mean": 0.70, "speed_seconds": 2.0},
        {"engine": "other", "overall_mean": 0.60, "nid_mean": 0.95, "teds_mean": 0.50, "mhs_mean": 0.10, "speed_seconds": 20.0},
    ]

    def test_deltas_are_subject_minus_engine(self):
        table = format_comparison_table(self.rows, subject="docstomd")
        line = next(l for l in table.splitlines() if l.startswith("other"))
        assert line.split()[1:5] == ["+0.2000", "-0.0500", "+0.0000", "+0.6000"]

    def test_speedup_is_engine_time_over_subject_time(self):
        table = format_comparison_table(self.rows, subject="docstomd")
        assert table.splitlines()[-1].endswith("10.0x")

    def test_subject_is_not_compared_with_itself(self):
        table = format_comparison_table(self.rows, subject="docstomd")
        assert len(table.splitlines()) == 3
