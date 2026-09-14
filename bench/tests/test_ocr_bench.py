import importlib.util
import json
import os
import stat
import subprocess
import sys
from pathlib import Path

import pytest

REPO_ROOT = Path(__file__).resolve().parents[2]
OCR_DIR = REPO_ROOT / "bench" / "ocr"
sys.path.insert(0, str(OCR_DIR))

import fixtures  # noqa: E402

spec = importlib.util.spec_from_file_location("ocr_run", OCR_DIR / "run.py")
ocr_run = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ocr_run)


def doc(name, pages, routed, group="corpus", est=0.004):
    return {
        "document": name,
        "group": group,
        "pdf_type": "scanned" if routed else "text_based",
        "pages": pages,
        "routed_pages": routed,
        "auto_billed": routed,
        "force_billed": pages,
        "auto_est_usd": routed * est,
        "force_est_usd": pages * est,
    }


@pytest.fixture
def binary(docstomd_binary):
    return docstomd_binary


class TestAggregate:
    def test_routing_percentage_and_cost(self):
        totals = ocr_run.aggregate([doc("a", 10, 1), doc("b", 2, 2), doc("c", 8, 0)])
        assert totals["documents"] == 3
        assert totals["pages"] == 20
        assert totals["routed_pages"] == 3
        assert totals["routing_pct"] == pytest.approx(15.0)
        assert totals["auto_est_usd"] == pytest.approx(0.012)
        assert totals["force_est_usd"] == pytest.approx(0.08)

    def test_empty_corpus_has_zero_routing(self):
        assert ocr_run.aggregate([])["routing_pct"] == 0.0


class TestRoutingGates:
    def test_expected_fixture_fractions_pass(self):
        rows = [doc("text_dominant_mixed", 8, 1, group="fixture"), doc("fully_scanned", 4, 4, group="fixture")]
        assert ocr_run.routing_gate_failures(rows) == []

    def test_mixed_fixture_routing_too_much_fails(self):
        rows = [doc("text_dominant_mixed", 8, 8, group="fixture"), doc("fully_scanned", 4, 4, group="fixture")]
        failures = ocr_run.routing_gate_failures(rows)
        assert len(failures) == 1 and "text_dominant_mixed" in failures[0]

    def test_scanned_fixture_routing_too_little_fails(self):
        rows = [doc("text_dominant_mixed", 8, 1, group="fixture"), doc("fully_scanned", 4, 1, group="fixture")]
        failures = ocr_run.routing_gate_failures(rows)
        assert len(failures) == 1 and "fully_scanned" in failures[0]

    def test_missing_fixture_fails(self):
        assert ocr_run.routing_gate_failures([doc("fully_scanned", 4, 4, group="fixture")])


class TestLiveBudget:
    def test_plan_selects_documents_within_budget(self):
        rows = [doc("a", 1, 1), doc("b", 3, 3), doc("c", 1, 1), doc("t", 5, 0)]
        selected, skipped = ocr_run.plan_live(rows, budget=5)
        assert [r["document"] for r in selected] == ["a", "c"]
        assert [r["document"] for r in skipped] == ["b"]

    def test_plan_ignores_unrouted_and_fixture_documents(self):
        rows = [doc("t", 5, 0), doc("fully_scanned", 4, 4, group="fixture"), doc("a", 1, 1)]
        selected, skipped = ocr_run.plan_live(rows, budget=100)
        assert [r["document"] for r in selected] == ["a"]
        assert skipped == []


class TestQualityGate:
    def test_delta_within_bound_passes(self):
        quality = {"a": {"auto": 0.90, "force": 0.91}, "b": {"auto": 0.8, "force": 0.8}}
        summary = ocr_run.quality_summary(quality)
        assert summary["mean_delta"] == pytest.approx(-0.005)
        assert ocr_run.quality_gate_failures(summary, max_quality_drop=0.02) == []

    def test_document_drop_beyond_bound_fails(self):
        quality = {"a": {"auto": 0.5, "force": 0.9}, "b": {"auto": 0.9, "force": 0.9}}
        failures = ocr_run.quality_gate_failures(ocr_run.quality_summary(quality), max_quality_drop=0.02)
        assert any("mean" in f for f in failures)
        assert any("a" in f and "document" in f for f in failures)

    def test_no_scored_documents_reports_no_delta(self):
        summary = ocr_run.quality_summary({})
        assert summary["mean_delta"] is None
        assert ocr_run.quality_gate_failures(summary, max_quality_drop=0.02) == []


@pytest.mark.integration
class TestFixtures:
    def test_generated_fixtures_route_as_specified(self, tmp_path, binary):
        paths = fixtures.write_routing_fixtures(tmp_path)
        for name, path in paths.items():
            detected = json.loads(subprocess.run([binary, "detect", "--json", path], capture_output=True, text=True, check=True).stdout)
            pages = detected["pdf"]["page_count"]
            assert pages == len(fixtures.ROUTING_FIXTURES[name]["kinds"])
        mixed = json.loads(subprocess.run([binary, "detect", "--json", paths["text_dominant_mixed"]], capture_output=True, text=True, check=True).stdout)
        assert mixed["pdf"]["pages_needing_ocr"] == [8]


class TestDryRun:
    @pytest.mark.integration
    def test_dry_run_reports_without_provider_calls(self, tmp_path, binary, monkeypatch):
        monkeypatch.setenv("MISTRAL_API_KEY", "must-not-be-used")
        corpus = tmp_path / "corpus"
        corpus.mkdir()
        for name in ("handmade-scanned.pdf", "cropbox_offset_origin.pdf", "td9264.pdf"):
            (corpus / name).write_bytes((REPO_ROOT / "testdata" / "detect" / name).read_bytes())
        rows = ocr_run.dry_run(binary, sorted(corpus.glob("*.pdf")), group="corpus")
        by_name = {r["document"]: r for r in rows}
        assert by_name["handmade-scanned"]["routed_pages"] == 2
        assert by_name["handmade-scanned"]["auto_est_usd"] == pytest.approx(0.008)
        assert by_name["td9264"]["routed_pages"] == 0
        assert by_name["td9264"]["force_billed"] == 10
        assert all(r["provider_calls"] == 0 for r in rows)

    @pytest.mark.integration
    def test_main_dry_writes_report(self, tmp_path, binary, monkeypatch):
        corpus = tmp_path / "corpus"
        corpus.mkdir()
        (corpus / "handmade-scanned.pdf").write_bytes((REPO_ROOT / "testdata" / "detect" / "handmade-scanned.pdf").read_bytes())
        results = tmp_path / "results"
        code = ocr_run.main(["--corpus", str(corpus), "--results", str(results), "--binary", str(binary)])
        assert code == 0
        payload = json.loads((results / "ocr-results.json").read_text(encoding="utf-8"))
        assert payload["mode"] == "dry"
        assert payload["aggregate"]["corpus"]["routed_pages"] == 2
        assert {r["document"] for r in payload["documents"] if r["group"] == "fixture"} == set(fixtures.ROUTING_FIXTURES)
        report = (results / "ocr-report.md").read_text(encoding="utf-8")
        assert "routing" in report and "handmade-scanned" in report


def make_fake_binary(directory: Path, log: Path) -> Path:
    script = directory / "fake-docstomd"
    script.write_text(
        f"""#!/usr/bin/env python3
import json, sys
args = sys.argv[1:]
with open({str(log)!r}, "a") as handle:
    handle.write(json.dumps(args) + "\\n")
mode = args[args.index("--ocr") + 1]
cap = int(args[args.index("--ocr-max-pages") + 1]) if "--ocr-max-pages" in args else 0
pages = [1] if mode == "auto" else [1, 2]
if cap:
    pages = pages[:cap]
print(json.dumps({{"markdown": "# " + mode, "ocr_cost": {{"provider": "fake", "pages_billed": len(pages), "billed_pages": pages, "estimated_cost_usd": len(pages) * 0.004, "dry_run": False}}}}))
""",
        encoding="utf-8",
    )
    script.chmod(script.stat().st_mode | stat.S_IXUSR)
    return script


class TestLiveRun:
    def test_live_enforces_budget_and_passes_caps(self, tmp_path):
        log = tmp_path / "calls.jsonl"
        binary = make_fake_binary(tmp_path, log)
        pdfs = {name: tmp_path / f"{name}.pdf" for name in ("a", "b")}
        rows = [dict(doc("a", 2, 1), path=str(pdfs["a"])), dict(doc("b", 2, 1), path=str(pdfs["b"]))]
        out = tmp_path / "predictions"
        live = ocr_run.live_run(binary, rows, budget=3, out=out)
        calls = [json.loads(line) for line in log.read_text().splitlines()]
        assert len(calls) == 2
        assert live["pages_billed"] == 3
        assert live["pages_billed"] <= 3
        assert [r["document"] for r in live["skipped"]] == ["b"]
        assert all("--ocr-max-pages" in call for call in calls)
        assert (out / "docstomd-ocr-auto" / "markdown" / "a.md").read_text() == "# auto"
        assert (out / "docstomd-ocr-force" / "markdown" / "a.md").read_text() == "# force"

    def test_live_requires_key(self, tmp_path, monkeypatch):
        monkeypatch.delenv("MISTRAL_API_KEY", raising=False)
        code = ocr_run.main(["--live", "--corpus", str(tmp_path), "--results", str(tmp_path / "r"), "--binary", "unused"])
        assert code == 2
