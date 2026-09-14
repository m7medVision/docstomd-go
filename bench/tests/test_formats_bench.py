import importlib.util
import json
import stat
import sys
import zipfile
from pathlib import Path

import pytest

REPO_ROOT = Path(__file__).resolve().parents[2]
FORMATS_DIR = REPO_ROOT / "bench" / "formats"
sys.path.insert(0, str(FORMATS_DIR))

import formats_gates  # noqa: E402
import generate  # noqa: E402

spec = importlib.util.spec_from_file_location("formats_run", FORMATS_DIR / "run.py")
formats_run = importlib.util.module_from_spec(spec)
spec.loader.exec_module(formats_run)

FINGERPRINT = {"hostname": "bench-host", "machine": "x86_64", "cpus": 8}


def executable(path: Path, body: str) -> Path:
    path.write_text("#!/usr/bin/env python3\n" + body, encoding="utf-8")
    path.chmod(path.stat().st_mode | stat.S_IXUSR)
    return path


def fake_docstomd(directory: Path) -> Path:
    return executable(directory / "fake-docstomd", """
import json, sys
path = sys.argv[-1]
if path.endswith(".xlsx"):
    print(json.dumps({"error": {"code": "unsupported", "message": "unsupported input format: xlsx"}}))
    sys.exit(2)
print(json.dumps({"markdown": "# Heading\\n\\nshared words appear here in " + path.rsplit(".", 1)[1]}))
""")


def fake_baseline(directory: Path) -> Path:
    return executable(directory / "fake-baseline", """
import sys
path = sys.argv[1]
print("# Heading\\n\\nshared words appear here in " + path.rsplit(".", 1)[1] + " plus extra baseline words")
""")


class TestBenchEnv:
    def test_parses_keys_comments_and_quotes(self, tmp_path):
        env = tmp_path / "bench.env"
        env.write_text("# comment\n\nFORMATS_BASELINE_SRC=/src/dir\nFORMATS_BASELINE_BUILD=\"make all\"\nEXTRA='x=y'\n", encoding="utf-8")
        assert formats_run.load_env(env) == {"FORMATS_BASELINE_SRC": "/src/dir", "FORMATS_BASELINE_BUILD": "make all", "EXTRA": "x=y"}

    def test_missing_env_file_warns_and_skips_baseline(self, tmp_path):
        binary, warning = formats_run.build_baseline(tmp_path / "missing.env")
        assert binary is None and "missing.env" in warning

    def test_missing_toolchain_warns(self, tmp_path):
        env = tmp_path / "bench.env"
        env.write_text(f"FORMATS_BASELINE_SRC={tmp_path}\nFORMATS_BASELINE_BUILD=no-such-toolchain-xyz build\nFORMATS_BASELINE_BIN=out/bin\n", encoding="utf-8")
        binary, warning = formats_run.build_baseline(env)
        assert binary is None
        assert "no-such-toolchain-xyz" in warning and "not found" in warning

    def test_incomplete_env_warns(self, tmp_path):
        env = tmp_path / "bench.env"
        env.write_text(f"FORMATS_BASELINE_SRC={tmp_path}\n", encoding="utf-8")
        binary, warning = formats_run.build_baseline(env)
        assert binary is None and "FORMATS_BASELINE_BUILD" in warning

    def test_builds_in_source_dir_with_env_exported(self, tmp_path):
        source = tmp_path / "src"
        source.mkdir()
        env = tmp_path / "bench.env"
        env.write_text(
            f"FORMATS_BASELINE_SRC={source}\n"
            f"FORMATS_BASELINE_BUILD={sys.executable} -c \"import os, pathlib; p = pathlib.Path(os.environ['OUT_DIR']) / 'conv'; p.parent.mkdir(parents=True); p.write_text('x'); p.chmod(0o755)\"\n"
            f"OUT_DIR={tmp_path / 'target'}\n"
            f"FORMATS_BASELINE_BIN={tmp_path / 'target' / 'conv'}\n",
            encoding="utf-8",
        )
        binary, warning = formats_run.build_baseline(env)
        assert warning is None
        assert binary == tmp_path / "target" / "conv"

    def test_relative_binary_resolves_against_source(self, tmp_path):
        source = tmp_path / "src"
        (source / "bin").mkdir(parents=True)
        executable(source / "bin" / "conv", "")
        env = tmp_path / "bench.env"
        env.write_text(f"FORMATS_BASELINE_SRC={source}\nFORMATS_BASELINE_BUILD={sys.executable} -c pass\nFORMATS_BASELINE_BIN=bin/conv\n", encoding="utf-8")
        binary, warning = formats_run.build_baseline(env)
        assert warning is None and binary == source / "bin" / "conv"

    def test_failed_build_warns(self, tmp_path):
        env = tmp_path / "bench.env"
        env.write_text(f"FORMATS_BASELINE_SRC={tmp_path}\nFORMATS_BASELINE_BUILD={sys.executable} -c \"raise SystemExit(3)\"\nFORMATS_BASELINE_BIN=conv\n", encoding="utf-8")
        binary, warning = formats_run.build_baseline(env)
        assert binary is None and "build failed" in warning


class TestCorpus:
    def test_assembles_vendored_and_generated_deduplicated(self, tmp_path):
        vendored = tmp_path / "testdata"
        (vendored / "a").mkdir(parents=True)
        (vendored / "b").mkdir()
        (vendored / "abuse").mkdir()
        (vendored / "a" / "one.docx").write_bytes(b"docx-bytes")
        (vendored / "b" / "copy.docx").write_bytes(b"docx-bytes")
        (vendored / "b" / "sheet.XLSX").write_bytes(b"xlsx-bytes")
        (vendored / "b" / "ignored.pdf").write_bytes(b"pdf")
        (vendored / "abuse" / "bomb.pptx").write_bytes(b"bomb")
        corpus = formats_run.assemble_corpus([vendored], tmp_path / "generated")
        vendored_docs = [d for d in corpus if d["source"] == "vendored"]
        assert [(d["id"], d["format"]) for d in vendored_docs] == [("a/one.docx", "docx"), ("b/sheet.XLSX", "xlsx")]
        generated_docs = [d for d in corpus if d["source"] == "generated"]
        assert {d["format"] for d in generated_docs} == {"docx", "xlsx", "pptx"}
        assert len(generated_docs) == 3 * len(generate.SIZES)
        assert all(d["bytes"] > 0 and len(d["sha256"]) == 64 for d in corpus)

    def test_generated_packages_are_deterministic_zip_files(self, tmp_path):
        first = generate.write_generated_corpus(tmp_path / "one")
        second = generate.write_generated_corpus(tmp_path / "two")
        for a, b in zip(first, second):
            assert a.read_bytes() == b.read_bytes()
            with zipfile.ZipFile(a) as archive:
                assert "[Content_Types].xml" in archive.namelist()


class TestEngines:
    def test_docstomd_statuses(self, tmp_path):
        binary = fake_docstomd(tmp_path)
        status, markdown = formats_run.docstomd_convert(binary, tmp_path / "x.docx")
        assert status == "ok" and markdown.startswith("# Heading")
        status, markdown = formats_run.docstomd_convert(binary, tmp_path / "x.xlsx")
        assert status == "unsupported" and markdown == ""

    def test_docstomd_other_failures_are_errors(self, tmp_path):
        binary = executable(tmp_path / "broken", "import sys\nprint('{\"error\": {\"code\": \"malformed\", \"message\": \"bad\"}}')\nsys.exit(4)\n")
        assert formats_run.docstomd_convert(binary, tmp_path / "x.docx") == ("error", "")

    def test_baseline_statuses(self, tmp_path):
        binary = fake_baseline(tmp_path)
        status, markdown = formats_run.baseline_convert(binary, tmp_path / "x.pptx")
        assert status == "ok" and "pptx" in markdown
        failing = executable(tmp_path / "failing", "import sys\nsys.exit(1)\n")
        assert formats_run.baseline_convert(failing, tmp_path / "x.pptx") == ("error", "")

    def test_run_corpus_alternates_engines_and_keeps_last_pass(self, tmp_path):
        log = []
        calls = {"docstomd": 0}

        def ours(path):
            log.append(("docstomd", path.name))
            calls["docstomd"] += 1
            return "ok", f"pass {calls['docstomd']}"

        def theirs(path):
            log.append(("baseline", path.name))
            return "unsupported", ""

        corpus = [{"id": "a.docx", "path": tmp_path / "a.docx", "format": "docx", "bytes": 1},
                  {"id": "b.docx", "path": tmp_path / "b.docx", "format": "docx", "bytes": 1}]
        documents = formats_run.run_corpus(corpus, {"docstomd": ours, "baseline": theirs}, passes=2, warmup=1)
        assert log[:4] == [("docstomd", "a.docx"), ("docstomd", "b.docx"), ("baseline", "a.docx"), ("baseline", "b.docx")]
        assert len(log) == 12
        assert documents[1]["markdown"]["docstomd"] == "pass 6"
        assert documents[0]["status"] == {"docstomd": "ok", "baseline": "unsupported"}
        assert len(documents[0]["seconds"]["docstomd"]) == 2


def summary(ours=0.9, theirs=0.8, per_document=None):
    return {"docx": {"ours_in_baseline": ours, "baseline_in_ours": theirs, "per_document": per_document or {}}}


def baseline_file(ours=0.9, theirs=0.8, targets=None, fingerprint=FINGERPRINT, digest="abc", per_document=None):
    return {
        "fingerprint": fingerprint,
        "corpus": {"digest": digest},
        "formats": {"docx": {"ours_in_baseline": ours, "baseline_in_ours": theirs, "per_document": per_document or {}}},
        "targets": targets or {},
    }


class TestGates:
    def test_no_committed_baseline_skips(self):
        results = formats_gates.check(summary(), None, FINGERPRINT, "abc", baseline_available=True)
        assert results["docx"] == {"failures": [], "skipped": "no committed baseline"}

    def test_unavailable_baseline_engine_skips(self):
        results = formats_gates.check(summary(None, None), baseline_file(), FINGERPRINT, "abc", baseline_available=False)
        assert results["docx"]["skipped"] == "baseline engine unavailable"

    def test_meeting_targets_and_baseline_passes(self):
        committed = baseline_file(targets={"docx": {"min_ours_in_baseline": 0.85, "min_baseline_in_ours": 0.75}})
        assert formats_gates.check(summary(), committed, FINGERPRINT, "abc", baseline_available=True)["docx"]["failures"] == []

    def test_below_target_fails(self):
        committed = baseline_file(targets={"docx": {"min_ours_in_baseline": 0.95, "min_baseline_in_ours": 0.75}})
        failures = formats_gates.check(summary(), committed, FINGERPRINT, "abc", baseline_available=True)["docx"]["failures"]
        assert len(failures) == 1 and "ours_in_baseline" in failures[0] and "target" in failures[0]

    def test_missing_candidate_value_fails_target(self):
        committed = baseline_file(targets={"docx": {"min_ours_in_baseline": 0.5}})
        failures = formats_gates.check(summary(None, 0.8), committed, FINGERPRINT, "abc", baseline_available=True)["docx"]["failures"]
        assert failures and "no containment" in failures[0]

    def test_regression_beyond_bound_fails_on_same_machine(self):
        failures = formats_gates.check(summary(0.85, 0.8), baseline_file(), FINGERPRINT, "abc", baseline_available=True)["docx"]["failures"]
        assert len(failures) == 1 and "regressed" in failures[0]

    def test_small_regression_within_bound_passes(self):
        assert formats_gates.check(summary(0.885, 0.8), baseline_file(), FINGERPRINT, "abc", baseline_available=True)["docx"]["failures"] == []

    def test_one_document_losing_content_fails_even_when_the_format_average_holds(self):
        recorded = {"small.docx": {"ours_in_baseline": 1.0, "baseline_in_ours": 1.0}, "big.docx": {"ours_in_baseline": 1.0, "baseline_in_ours": 1.0}}
        candidate = {"small.docx": {"ours_in_baseline": 1.0, "baseline_in_ours": 0.1}, "big.docx": {"ours_in_baseline": 1.0, "baseline_in_ours": 1.0}}
        failures = formats_gates.check(summary(0.9, 0.8, candidate), baseline_file(per_document=recorded), FINGERPRINT, "abc", baseline_available=True)["docx"]["failures"]
        assert len(failures) == 1 and "small.docx" in failures[0] and "baseline_in_ours" in failures[0]

    def test_document_that_stops_converting_fails(self):
        recorded = {"a.docx": {"ours_in_baseline": 1.0, "baseline_in_ours": 1.0}}
        candidate = {"a.docx": {"ours_in_baseline": None, "baseline_in_ours": 0.0}}
        failures = formats_gates.check(summary(0.9, 0.8, candidate), baseline_file(per_document=recorded), FINGERPRINT, "abc", baseline_available=True)["docx"]["failures"]
        assert len(failures) == 2 and all("a.docx" in f for f in failures)

    def test_other_machine_or_corpus_skips_regression_but_keeps_targets(self):
        other = dict(FINGERPRINT, hostname="elsewhere")
        committed = baseline_file(targets={"docx": {"min_ours_in_baseline": 0.95}}, fingerprint=other)
        result = formats_gates.check(summary(0.5, 0.8), committed, FINGERPRINT, "abc", baseline_available=True)["docx"]
        assert len(result["failures"]) == 1 and "target" in result["failures"][0]
        assert "another machine" in result["note"]
        result = formats_gates.check(summary(0.5, 0.8), baseline_file(), FINGERPRINT, "changed", baseline_available=True)["docx"]
        assert result["failures"] == [] and "corpus changed" in result["note"]


class TestMain:
    @pytest.fixture(autouse=True)
    def tiny_generated_corpus(self, monkeypatch):
        monkeypatch.setattr(generate, "SIZES", {"small": 1, "medium": 2, "large": 3})

    def run_main(self, tmp_path, *extra):
        vendored = tmp_path / "testdata"
        vendored.mkdir()
        (vendored / "vendored.docx").write_bytes(b"not really docx")
        results = tmp_path / "results"
        args = [
            "--vendored", str(vendored),
            "--results", str(results),
            "--docstomd-binary", str(fake_docstomd(tmp_path)),
            "--baseline-binary", str(fake_baseline(tmp_path)),
            "--baselines", str(tmp_path / "baselines"),
            "--passes", "1", "--warmup", "0",
            *extra,
        ]
        return formats_run.main(args), results

    def test_end_to_end_report_with_unsupported_format(self, tmp_path):
        code, results = self.run_main(tmp_path)
        assert code == 0
        payload = json.loads((results / "formats-results.json").read_text(encoding="utf-8"))
        assert set(payload["formats"]) == {"docx", "xlsx", "pptx"}
        assert payload["formats"]["xlsx"]["status"]["docstomd"] == {"unsupported": 3}
        assert payload["formats"]["docx"]["ours_in_baseline"] == pytest.approx(1.0)
        assert 0 < payload["formats"]["docx"]["baseline_in_ours"] < 1
        assert payload["speed"]["docx"]["docstomd"]["median_ms"] > 0
        assert payload["speed"]["xlsx"]["docstomd"]["median_ms"] is None
        report = (results / "formats-report.md").read_text(encoding="utf-8")
        for needle in ("docx", "xlsx", "pptx", "unsupported", "MB/s", "median ms", "containment", "lowest per-document"):
            assert needle in report

    def test_update_baseline_records_fingerprint_and_keeps_targets(self, tmp_path):
        baselines = tmp_path / "baselines"
        baselines.mkdir()
        (baselines / "docstomd.json").write_text(json.dumps({"targets": {"docx": {"min_ours_in_baseline": 0.5}}}), encoding="utf-8")
        code, _ = self.run_main(tmp_path, "--update-baseline")
        assert code == 0
        committed = json.loads((baselines / "docstomd.json").read_text(encoding="utf-8"))
        assert committed["targets"] == {"docx": {"min_ours_in_baseline": 0.5}}
        assert {"hostname", "machine", "cpus"} <= committed["fingerprint"].keys()
        assert set(committed["formats"]) == {"docx", "xlsx", "pptx"}
        assert committed["corpus"]["documents"] == 10
        assert committed["formats"]["docx"]["per_document"]["vendored.docx"]["ours_in_baseline"] == pytest.approx(1.0)

    def test_gate_failure_exits_nonzero(self, tmp_path):
        baselines = tmp_path / "baselines"
        baselines.mkdir()
        (baselines / "docstomd.json").write_text(json.dumps({"targets": {"xlsx": {"min_baseline_in_ours": 0.5}}}), encoding="utf-8")
        code, results = self.run_main(tmp_path)
        assert code == 1
        assert "FAIL" in (results / "formats-report.md").read_text(encoding="utf-8")

    def test_missing_baseline_engine_still_reports(self, tmp_path, monkeypatch):
        vendored = tmp_path / "testdata"
        vendored.mkdir()
        results = tmp_path / "results"
        code = formats_run.main([
            "--vendored", str(vendored), "--results", str(results),
            "--docstomd-binary", str(fake_docstomd(tmp_path)),
            "--env", str(tmp_path / "absent.env"),
            "--baselines", str(tmp_path / "baselines"),
            "--passes", "1", "--warmup", "0",
        ])
        assert code == 0
        payload = json.loads((results / "formats-results.json").read_text(encoding="utf-8"))
        assert payload["baseline_warning"]
        assert payload["formats"]["docx"]["ours_in_baseline"] is None
