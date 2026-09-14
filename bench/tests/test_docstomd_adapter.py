import json
import stat
import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "pdf" / "engines"))

import docstomd_adapter

FIXTURES = Path(__file__).resolve().parents[2] / "testdata" / "detect"
PDF_FIXTURES = sorted(FIXTURES.glob("*.pdf"))[:3]


def make_fake_binary(directory: Path) -> Path:
    path = directory / "fake-docstomd"
    path.write_text(
        "#!/bin/sh\n"
        'case "$1" in convert) echo \'{"markdown": "# converted"}\' ;;\n'
        "*) exit 1 ;; esac\n",
        encoding="utf-8",
    )
    path.chmod(path.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)
    return path


class TestAdapterSuccessPath:
    def test_fake_binary_writes_predictions(self, tmp_path):
        binary = make_fake_binary(tmp_path)
        out = tmp_path / "predictions"
        code = docstomd_adapter.main(
            ["--binary", str(binary), "--corpus", str(FIXTURES), "--out", str(out), "--limit", "3"]
        )
        assert code == 0
        markdown_files = sorted((out / "markdown").glob("*.md"))
        assert len(markdown_files) == 3
        assert markdown_files[0].read_text(encoding="utf-8") == "# converted"
        timing = json.loads((out / "timing.json").read_text(encoding="utf-8"))
        assert timing["engine"] == "docstomd"
        assert timing["documents"] == 3
        assert timing["passes"][0]["ok"] == 3
        assert timing["passes"][0]["failed"] == 0

    def test_speed_passes_median(self, tmp_path):
        binary = make_fake_binary(tmp_path)
        out = tmp_path / "predictions"
        code = docstomd_adapter.main(
            ["--binary", str(binary), "--corpus", str(FIXTURES), "--out", str(out), "--limit", "2", "--speed-passes", "3"]
        )
        assert code == 0
        timing = json.loads((out / "timing.json").read_text(encoding="utf-8"))
        assert len(timing["passes"]) == 3
        assert timing["median_wall_seconds"] > 0


class TestAdapterFailurePath:
    def test_failed_conversions_leave_missing_predictions(self, tmp_path):
        binary = tmp_path / "failing-docstomd"
        binary.write_text("#!/bin/sh\necho boom >&2\nexit 2\n", encoding="utf-8")
        binary.chmod(binary.stat().st_mode | stat.S_IXUSR)
        out = tmp_path / "predictions"
        code = docstomd_adapter.main(
            ["--binary", str(binary), "--corpus", str(FIXTURES), "--out", str(out), "--limit", "3"]
        )
        assert code == 0
        assert list((out / "markdown").glob("*.md")) == []
        timing = json.loads((out / "timing.json").read_text(encoding="utf-8"))
        assert timing["passes"][0]["failed"] == 3
        assert timing["passes"][0]["ok"] == 0
        assert all(error.startswith("exit 2: boom") for error in timing["passes"][0]["errors"].values())


class TestAdapterRealBinary:
    @pytest.mark.integration
    def test_real_binary_converts_fixtures(self, tmp_path, docstomd_binary):
        binary = docstomd_binary
        out = tmp_path / "predictions"
        code = docstomd_adapter.main(
            ["--binary", str(binary), "--corpus", str(FIXTURES), "--out", str(out), "--limit", "3"]
        )
        assert code == 0
        timing = json.loads((out / "timing.json").read_text(encoding="utf-8"))
        assert timing["passes"][0]["failed"] == 0, timing["passes"][0]["errors"]
        assert len(list((out / "markdown").glob("*.md"))) == 3

    def test_corpus_discovery(self, tmp_path):
        assert len(PDF_FIXTURES) == 3
