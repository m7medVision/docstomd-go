import copy
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "pdf"))

from gates import compare, evaluate_gates, scores

BASELINE = Path(__file__).resolve().parents[1] / "pdf" / "baselines" / "docstomd.json"


def load_baseline():
    return json.loads(BASELINE.read_text(encoding="utf-8"))


class TestCommittedDocstomdBaseline:
    def test_records_full_corpus_with_fingerprint_and_speed(self):
        baseline = load_baseline()
        assert len(baseline["corpus_revision"]) == 40
        assert {"hostname", "machine", "cpus"} <= baseline["fingerprint"].keys()
        assert baseline["engine_versions"]["docstomd"].startswith("dev+")
        assert baseline["speed"]["median_seconds"] > 0
        assert len(baseline["evaluation"]["documents"]) == 200
        assert {"overall_mean", "nid_mean", "teds_mean", "mhs_mean"} <= scores(baseline["evaluation"]).keys()

    def test_clean_rerun_passes_gates(self):
        evaluation = load_baseline()["evaluation"]
        assert evaluate_gates(compare(evaluation, copy.deepcopy(evaluation))) == []

    def test_injected_document_regression_fails_gates(self):
        evaluation = load_baseline()["evaluation"]
        candidate = copy.deepcopy(evaluation)
        document = next(d for d in candidate["documents"] if (d["scores"].get("overall") or 0) > 0.1)
        document["scores"]["overall"] -= 0.05
        candidate["metrics"]["score"]["overall_mean"] -= 0.05 / len(candidate["documents"])
        failures = evaluate_gates(compare(evaluation, candidate))
        assert any("overall delta" in f for f in failures)
        assert any(document["document_id"] in f for f in failures)
