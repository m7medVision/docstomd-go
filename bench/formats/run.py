"""Formats bench: docstomd vs the locally built baseline binary on docx/xlsx/pptx.

Usage:
  python3 bench/formats/run.py                     # assemble corpus, build baseline, run, report, gate
  python3 bench/formats/run.py --update-baseline   # record bench/formats/baselines/docstomd.json
  python3 bench/formats/run.py --passes 1 --warmup 0

The baseline engine is described only by the git-ignored bench env file
(see bench.env.example): FORMATS_BASELINE_SRC is the source checkout,
FORMATS_BASELINE_BUILD the build command run there, and FORMATS_BASELINE_BIN
the resulting binary (absolute, or relative to the source checkout), which
must print Markdown for `<binary> <file>`. Every other key in the file is
exported to the build. A missing file, key, toolchain or failed build warns
and the run continues with docstomd alone.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import platform
import shlex
import shutil
import socket
import subprocess
import sys
import time
from pathlib import Path
from typing import Callable

FORMATS_DIR = Path(__file__).resolve().parent
BENCH_DIR = FORMATS_DIR.parent
REPO_ROOT = BENCH_DIR.parent
RESULTS = BENCH_DIR / "results"
BASELINES = FORMATS_DIR / "baselines"
ENV_FILE = REPO_ROOT / "bench.env"
FORMATS = ("docx", "xlsx", "pptx")
SUBJECT = "docstomd"
BASELINE = "baseline"
DEFAULT_PASSES = 5
DEFAULT_WARMUP = 1
EXIT_UNSUPPORTED = 2
LOWEST_DOCUMENTS = 3

sys.path.insert(0, str(FORMATS_DIR))
import formats_gates  # noqa: E402
import generate  # noqa: E402
import metrics  # noqa: E402

Engine = Callable[[Path], tuple[str, str]]


def load_env(path: Path) -> dict[str, str]:
    values = {}
    for line in path.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        value = value.strip()
        if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
            value = value[1:-1]
        values[key.strip()] = value
    return values


def build_baseline(env_file: Path) -> tuple[Path | None, str | None]:
    if not env_file.exists():
        return None, f"{env_file} not found; baseline engine skipped (copy bench.env.example)"
    env = load_env(env_file)
    missing = [key for key in ("FORMATS_BASELINE_SRC", "FORMATS_BASELINE_BUILD", "FORMATS_BASELINE_BIN") if not env.get(key)]
    if missing:
        return None, f"{env_file} lacks {', '.join(missing)}; baseline engine skipped"
    source = Path(env["FORMATS_BASELINE_SRC"])
    if not source.is_dir():
        return None, f"baseline source {source} not found; baseline engine skipped"
    command = shlex.split(env["FORMATS_BASELINE_BUILD"])
    if shutil.which(command[0]) is None:
        return None, f"toolchain {command[0]!r} not found on PATH; baseline engine skipped"
    print("+", " ".join(command), flush=True)
    completed = subprocess.run(command, cwd=source, env={**os.environ, **env}, check=False)
    if completed.returncode != 0:
        return None, f"baseline build failed (exit {completed.returncode}); baseline engine skipped"
    binary = Path(env["FORMATS_BASELINE_BIN"])
    if not binary.is_absolute():
        binary = source / binary
    if not binary.is_file():
        return None, f"baseline binary {binary} missing after build; baseline engine skipped"
    return binary, None


def build_docstomd() -> Path:
    binary = REPO_ROOT / "cmd" / "docstomd" / "docstomd"
    subprocess.run(["go", "build", "-o", str(binary), "./cmd/docstomd"], cwd=REPO_ROOT, check=True)
    return binary


def assemble_corpus(vendored_roots: list[Path], generated_dir: Path) -> list[dict]:
    corpus = []
    seen = set()

    def add(path: Path, doc_id: str, source: str) -> None:
        data = path.read_bytes()
        digest = hashlib.sha256(data).hexdigest()
        if digest in seen:
            return
        seen.add(digest)
        corpus.append({"id": doc_id, "path": path, "format": path.suffix[1:].lower(), "bytes": len(data), "sha256": digest, "source": source})

    for root in vendored_roots:
        for path in sorted(root.rglob("*")):
            relative = path.relative_to(root)
            if path.is_file() and path.suffix[1:].lower() in FORMATS and "abuse" not in relative.parts:
                add(path, relative.as_posix(), "vendored")
    for path in generate.write_generated_corpus(generated_dir):
        add(path, f"generated/{path.name}", "generated")
    return corpus


def corpus_digest(corpus: list[dict]) -> str:
    return hashlib.sha256("".join(sorted(doc["sha256"] for doc in corpus)).encode()).hexdigest()


def docstomd_convert(binary: Path, path: Path) -> tuple[str, str]:
    completed = subprocess.run([str(binary), "convert", "--json", str(path)], capture_output=True, text=True, check=False)
    if completed.returncode == EXIT_UNSUPPORTED:
        return "unsupported", ""
    if completed.returncode != 0:
        return "error", ""
    return "ok", json.loads(completed.stdout)["markdown"]


def baseline_convert(binary: Path, path: Path) -> tuple[str, str]:
    completed = subprocess.run([str(binary), str(path)], capture_output=True, text=True, check=False)
    if completed.returncode != 0:
        return "error", ""
    return "ok", completed.stdout


def run_corpus(corpus: list[dict], engines: dict[str, Engine], passes: int, warmup: int) -> list[dict]:
    documents = [
        {**doc, "status": {}, "markdown": {}, "seconds": {label: [] for label in engines}}
        for doc in corpus
    ]
    for round_index in range(warmup + passes):
        for label, engine in engines.items():
            for document in documents:
                start = time.perf_counter()
                status, markdown = engine(document["path"])
                elapsed = time.perf_counter() - start
                if round_index < warmup:
                    continue
                document["seconds"][label].append(elapsed)
                document["status"][label] = status
                document["markdown"][label] = markdown
    return documents


def machine_fingerprint() -> dict:
    return {
        "hostname": socket.gethostname(),
        "machine": platform.machine(),
        "platform": platform.platform(),
        "cpus": os.cpu_count(),
        "python": sys.version.split()[0],
    }


def binary_version(binary: Path | None) -> str | None:
    return None if binary is None else "sha256:" + hashlib.sha256(binary.read_bytes()).hexdigest()[:12]


def format_ratio(value: float | None) -> str:
    return "n/a" if value is None else f"{value:.4f}"


def format_number(value: float | None, pattern: str) -> str:
    return "n/a" if value is None else format(value, pattern)


def write_report(results: Path, payload: dict) -> Path:
    results.mkdir(parents=True, exist_ok=True)
    fingerprint = payload["fingerprint"]
    engines = payload["engines"]
    body = [
        "# Formats bench report",
        "",
        f"- corpus: {payload['corpus']['documents']} documents (digest `{payload['corpus']['digest'][:12]}`)",
        f"- fingerprint: `{fingerprint['hostname']}` ({fingerprint['platform']}, {fingerprint['cpus']} cpus)",
        f"- engine versions: {json.dumps(payload['engine_versions'])}",
        f"- speed: median over {payload['passes']} alternating pass(es) after {payload['warmup']} warm-up round(s), "
        "per-document process wall time; MB/s is input bytes over summed per-document medians",
    ]
    if payload.get("baseline_warning"):
        body.append(f"- WARNING: {payload['baseline_warning']}")
    for fmt in FORMATS:
        summary = payload["formats"].get(fmt)
        if summary is None:
            continue
        body += [
            "",
            f"## {fmt}",
            "",
            f"- documents: {summary['documents']} ({summary['bytes']} bytes)",
            "- status: " + "; ".join(
                f"{engine} " + ", ".join(f"{count} {status}" for status, count in sorted(summary["status"][engine].items()))
                for engine in engines
            ),
            f"- trigram containment: ours in baseline {format_ratio(summary['ours_in_baseline'])}, "
            f"baseline in ours {format_ratio(summary['baseline_in_ours'])}",
            "",
            "```",
            f"{'engine':<10} {'headings':>9} {'rows':>7} {'lists':>7} {'links':>7} {'notes':>7} {'median ms':>10} {'MB/s':>9}",
        ]
        for engine in engines:
            counts = summary["counts"][engine]
            speed = payload["speed"][fmt][engine]
            body.append(
                f"{engine:<10} {counts['headings']:>9} {counts['table_rows']:>7} {counts['list_items']:>7} "
                f"{counts['links']:>7} {counts['footnotes']:>7} {format_number(speed['median_ms'], '.2f'):>10} "
                f"{format_number(speed['mb_per_s'], '.3f'):>9}"
            )
        body.append("```")
        lowest = sorted(
            (min(v for v in values.values() if v is not None), doc_id, values)
            for doc_id, values in summary["per_document"].items()
            if any(v is not None for v in values.values())
        )[:LOWEST_DOCUMENTS]
        if lowest:
            body += ["", "lowest per-document containment (ours in baseline / baseline in ours):", ""]
            body += [
                f"- `{doc_id}`: {format_ratio(values['ours_in_baseline'])} / {format_ratio(values['baseline_in_ours'])}"
                for _, doc_id, values in lowest
            ]
        gate = payload["gates"][fmt]
        if gate.get("skipped"):
            line = f"skipped ({gate['skipped']})"
        elif gate["failures"]:
            line = "FAIL: " + "; ".join(gate["failures"])
        else:
            line = "pass"
        if gate.get("note"):
            line += f" — {gate['note']}"
        body.append(f"\n- gate: {line}")
    report = results / "formats-report.md"
    report.write_text("\n".join(body) + "\n", encoding="utf-8")
    (results / "formats-results.json").write_text(json.dumps(payload, indent=2) + "\n", encoding="utf-8")
    return report


def write_baseline(path: Path, payload: dict) -> None:
    existing = json.loads(path.read_text(encoding="utf-8")) if path.exists() else {}
    path.parent.mkdir(parents=True, exist_ok=True)
    committed = {
        "fingerprint": payload["fingerprint"],
        "engine_versions": payload["engine_versions"],
        "corpus": payload["corpus"],
        "formats": {
            fmt: {
                "ours_in_baseline": summary["ours_in_baseline"],
                "baseline_in_ours": summary["baseline_in_ours"],
                "per_document": summary["per_document"],
                "status": summary["status"],
                "counts": summary["counts"],
                "speed": payload["speed"][fmt],
            }
            for fmt, summary in payload["formats"].items()
        },
        "targets": existing.get("targets", {}),
    }
    path.write_text(json.dumps(committed, indent=2) + "\n", encoding="utf-8")
    print(f"baseline updated: {path}")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--env", type=Path, default=ENV_FILE)
    parser.add_argument("--vendored", type=Path, action="append", help="fixture root (default: testdata)")
    parser.add_argument("--results", type=Path, default=RESULTS)
    parser.add_argument("--baselines", type=Path, default=BASELINES)
    parser.add_argument("--docstomd-binary", type=Path, help="skip building docstomd")
    parser.add_argument("--baseline-binary", type=Path, help="skip building the baseline engine")
    parser.add_argument("--passes", type=int, default=DEFAULT_PASSES)
    parser.add_argument("--warmup", type=int, default=DEFAULT_WARMUP)
    parser.add_argument("--max-regression", type=float, default=formats_gates.DEFAULT_MAX_REGRESSION)
    parser.add_argument("--update-baseline", action="store_true")
    args = parser.parse_args(argv)
    if args.passes < 1:
        parser.error("--passes must be at least 1")

    corpus = assemble_corpus(args.vendored or [REPO_ROOT / "testdata"], args.results / "formats-generated")
    docstomd = args.docstomd_binary or build_docstomd()
    baseline, warning = (args.baseline_binary, None) if args.baseline_binary else build_baseline(args.env)
    if warning:
        print(f"WARNING: {warning}", file=sys.stderr)

    engines: dict[str, Engine] = {SUBJECT: lambda path: docstomd_convert(docstomd, path)}
    if baseline is not None:
        engines[BASELINE] = lambda path: baseline_convert(baseline, path)
    documents = run_corpus(corpus, engines, args.passes, args.warmup)
    labels = tuple(engines)
    for document in documents:
        document["grams"] = {label: metrics.word_trigrams(document["markdown"][label]) for label in labels}
        document["counts"] = {label: metrics.structural_counts(document["markdown"][label]) for label in labels}

    digest = corpus_digest(corpus)
    fingerprint = machine_fingerprint()
    summary = metrics.format_summary(documents, labels)
    baseline_path = args.baselines / f"{SUBJECT}.json"
    committed = json.loads(baseline_path.read_text(encoding="utf-8")) if baseline_path.exists() else None
    payload = {
        "fingerprint": fingerprint,
        "engine_versions": {SUBJECT: binary_version(docstomd), BASELINE: binary_version(baseline)},
        "engines": list(labels),
        "passes": args.passes,
        "warmup": args.warmup,
        "baseline_warning": warning,
        "corpus": {"documents": len(corpus), "digest": digest},
        "formats": summary,
        "speed": metrics.speed_summary(documents, labels),
        "gates": formats_gates.check(summary, committed, fingerprint, digest,
                                     baseline_available=baseline is not None, max_regression=args.max_regression),
        "documents": [
            {"id": doc["id"], "format": doc["format"], "source": doc["source"], "bytes": doc["bytes"],
             "status": doc["status"], "counts": doc["counts"]}
            for doc in documents
        ],
    }
    report = write_report(args.results, payload)
    print(f"report: {report}")
    if args.update_baseline:
        write_baseline(baseline_path, payload)
        return 0
    failures = [f"{fmt}: {failure}" for fmt, gate in payload["gates"].items() for failure in gate["failures"]]
    if failures:
        print("\nFormats bench gates failed:", file=sys.stderr)
        for failure in failures:
            print(f"  - {failure}", file=sys.stderr)
        return 1
    print("\nFormats bench gates passed (or skipped).")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
