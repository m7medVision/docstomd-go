"""PDF bench orchestrator: pinned corpus, engines, evaluator, report, gates.

Usage:
  python3 bench/pdf/run.py                 # full corpus, all engines
  python3 bench/pdf/run.py --limit 5       # quick subset run
  python3 bench/pdf/run.py --update-baselines [engine ...]
  python3 bench/pdf/run.py --self-test     # gate logic + adapters, no corpus
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import platform
import socket
import subprocess
import sys
import time
from pathlib import Path

BENCH_DIR = Path(__file__).resolve().parent
REPO_ROOT = BENCH_DIR.parents[1]
CACHE = REPO_ROOT / "bench" / ".cache"
RESULTS = REPO_ROOT / "bench" / "results"
BASELINES = BENCH_DIR / "baselines"

CORPUS_REPO = "https://github.com/opendataloader-project/opendataloader-bench"
CORPUS_REVISION = "7af1d8f4d0c09f51ea1a5c6ba5f66e993286d109"
CORPUS_DOC_COUNT = 200

EVALUATOR_DEPS = ["rapidfuzz", "apted", "beautifulsoup4", "lxml", "py-cpuinfo"]
ENGINE_DEPS = ["markitdown[pdf]==0.1.7", "pymupdf4llm==1.28.2"]

SUBJECT = "docstomd"

sys.path.insert(0, str(BENCH_DIR))
import gates as gates_module  # noqa: E402
import speed as speed_module  # noqa: E402


def run(command: list[str], **kwargs) -> subprocess.CompletedProcess:
    print("+", " ".join(str(c) for c in command), flush=True)
    return subprocess.run([str(c) for c in command], check=True, **kwargs)


def ensure_corpus() -> Path:
    repo = CACHE / "opendataloader-bench"
    if not (repo / ".git").exists():
        CACHE.mkdir(parents=True, exist_ok=True)
        run(["git", "clone", CORPUS_REPO, repo])
    current = subprocess.run(
        ["git", "rev-parse", "HEAD"], cwd=repo, capture_output=True, text=True, check=True
    ).stdout.strip()
    if current != CORPUS_REVISION:
        run(["git", "fetch", "origin"], cwd=repo)
        run(["git", "checkout", CORPUS_REVISION], cwd=repo)
    pdfs = sorted((repo / "pdfs").glob("*.pdf"))
    if len(pdfs) != CORPUS_DOC_COUNT:
        raise SystemExit(f"corpus at {repo} has {len(pdfs)} PDFs, expected {CORPUS_DOC_COUNT}")
    return repo


def ensure_venv() -> Path:
    venv = CACHE / "venv"
    python = venv / "bin" / "python"
    if not python.exists():
        run(["uv", "venv", venv])
    probe = subprocess.run(
        [python, "-c", "import rapidfuzz, apted, bs4, lxml, markitdown, pymupdf4llm"],
        capture_output=True,
    )
    if probe.returncode != 0:
        run(["uv", "pip", "install", "--python", python, *EVALUATOR_DEPS, *ENGINE_DEPS])
    return python


def build_binary() -> Path:
    binary = REPO_ROOT / "cmd" / "docstomd" / "docstomd"
    run(["go", "build", "-o", binary, "./cmd/docstomd"], cwd=REPO_ROOT)
    return binary


def machine_fingerprint() -> dict:
    return {
        "hostname": socket.gethostname(),
        "machine": platform.machine(),
        "platform": platform.platform(),
        "cpus": os.cpu_count(),
        "python": sys.version.split()[0],
    }


def same_machine(recorded: dict, current: dict) -> bool:
    return all(recorded.get(key) == current[key] for key in ("hostname", "machine", "cpus"))


def engine_runners(corpus_repo: Path, python: Path, binary: Path, limit: int) -> tuple[dict[str, Path], dict]:
    prediction_root = corpus_repo / "prediction"
    limit_args = ["--limit", limit] if limit else []
    outputs = {label: prediction_root / label for label in (SUBJECT, "markitdown", "pymupdf4llm")}

    def timing_seconds(label: str) -> float:
        timing = json.loads((outputs[label] / "timing.json").read_text(encoding="utf-8"))
        return timing["median_wall_seconds"]

    def docstomd() -> float:
        run([python, BENCH_DIR / "engines" / "docstomd_adapter.py", "--binary", binary,
             "--corpus", corpus_repo / "pdfs", "--out", outputs[SUBJECT], *limit_args])
        return timing_seconds(SUBJECT)

    def markitdown() -> float:
        run([python, "src/pdf_parser.py", "--engine", "markitdown", "--log-level", "WARNING"], cwd=corpus_repo)
        summary = json.loads((outputs["markitdown"] / "summary.json").read_text(encoding="utf-8"))
        return summary["total_elapsed"]

    def pymupdf4llm() -> float:
        run([python, BENCH_DIR / "engines" / "pymupdf4llm_adapter.py",
             "--corpus", corpus_repo / "pdfs", "--out", outputs["pymupdf4llm"], *limit_args])
        return timing_seconds("pymupdf4llm")

    return outputs, {SUBJECT: docstomd, "markitdown": markitdown, "pymupdf4llm": pymupdf4llm}


def evaluate(corpus_repo: Path, python: Path, engines: dict[str, Path]) -> dict[str, dict]:
    evaluations = {}
    for label, out_dir in engines.items():
        run(
            [python, "src/evaluator.py", "--prediction-root", out_dir.parent, "--engine", label,
             "--log-level", "WARNING"],
            cwd=corpus_repo,
        )
        evaluations[label] = gates_module.load_evaluation(out_dir / "evaluation.json")
    return evaluations


def engine_versions(python: Path, binary: Path) -> dict[str, str]:
    versions = {}
    probe = subprocess.run(
        [python, "-c", "import markitdown, pymupdf4llm; print(markitdown.__version__ if hasattr(markitdown, '__version__') else 'unknown'); print(pymupdf4llm.__version__ if hasattr(pymupdf4llm, '__version__') else 'unknown')"],
        capture_output=True,
        text=True,
        check=True,
    )
    lines = probe.stdout.strip().splitlines()
    versions["markitdown"] = lines[0] if lines else "unknown"
    versions["pymupdf4llm"] = lines[1] if len(lines) > 1 else "unknown"
    digest = hashlib.sha256(binary.read_bytes()).hexdigest()[:12]
    versions[SUBJECT] = f"dev+{digest}"
    return versions


def engine_rows(evaluations: dict[str, dict], speed: dict[str, dict]) -> list[dict]:
    rows = []
    for label, evaluation in evaluations.items():
        row = {"engine": label, **gates_module.scores(evaluation), "missing": gates_module.missing_predictions(evaluation)}
        if label in speed:
            row["speed_seconds"] = speed[label]["median_seconds"]
        rows.append(row)
    return rows


def speed_protocol_text(context: dict) -> str:
    return (
        f"median of {context['speed_passes']} alternating full-corpus pass(es) per engine after "
        f"{context['speed_warmup']} excluded warm-up round(s), sequential single process; "
        "each engine's own conversion wall time"
    )


def gate_line(result: dict) -> str:
    status = "FAIL: " + "; ".join(result["failures"]) if result["failures"] else "pass"
    if "comparison" not in result:
        return status if result["failures"] else f"skipped ({result['skipped']})"
    comparison = result["comparison"]
    documents = comparison["documents"]
    return (
        f"{status} (overall {comparison['deltas'].get('overall_mean', float('nan')):+.4f}; "
        f"documents {documents['improved']} improved, {documents['regressed']} regressed, "
        f"{documents['unchanged']} unchanged)"
    )


def write_report(rows: list[dict], context: dict, full_evaluations: dict[str, dict], gate_results: dict[str, dict]) -> Path:
    RESULTS.mkdir(parents=True, exist_ok=True)
    fingerprint = context["fingerprint"]
    body = [
        "# PDF bench report",
        "",
        f"- corpus revision: `{context['corpus_revision']}` ({context['corpus_docs']} documents)",
        f"- fingerprint: `{fingerprint['hostname']}` ({fingerprint['platform']}, {fingerprint['cpus']} cpus)",
        f"- engine versions: {json.dumps(context['engine_versions'])}",
        f"- speed: {speed_protocol_text(context)}",
        "",
        "## Scores",
        "",
        "```",
        gates_module.format_scores_table(rows),
        "```",
    ]
    if len(rows) > 1 and any(row["engine"] == SUBJECT for row in rows):
        body += [
            "",
            f"## {SUBJECT} vs baseline engines",
            "",
            f"Score columns are {SUBJECT} minus the engine; speedup is the engine's median time over {SUBJECT}'s.",
            "",
            "```",
            gates_module.format_comparison_table(rows, SUBJECT),
            "```",
        ]
    if gate_results:
        body += ["", "## Gates vs committed baselines", ""]
        body += [f"- {label}: {gate_line(result)}" for label, result in gate_results.items()]
    report = RESULTS / "report.md"
    report.write_text("\n".join(body) + "\n", encoding="utf-8")
    (RESULTS / "evaluations.json").write_text(
        json.dumps({"context": context, "rows": rows, "full": full_evaluations}, indent=2) + "\n",
        encoding="utf-8",
    )
    return report


def check_gates(evaluations: dict[str, dict], args) -> dict[str, dict]:
    results = {}
    for label in sorted(evaluations):
        baseline_path = BASELINES / f"{label}.json"
        if not baseline_path.exists():
            print(f"gates: no baseline for {label}, skipping", flush=True)
            results[label] = {"skipped": "no committed baseline", "failures": []}
            continue
        baseline = json.loads(baseline_path.read_text(encoding="utf-8"))
        if not same_machine(baseline.get("fingerprint", {}), machine_fingerprint()):
            results[label] = {"failures": ["baseline was recorded on another machine; refusing to compare"]}
            continue
        comparison = gates_module.compare(baseline["evaluation"], evaluations[label])
        failures = gates_module.evaluate_gates(
            comparison,
            min_overall_delta=args.min_overall_delta,
            max_document_regression=args.max_document_regression,
            max_missing=args.max_missing,
        )
        results[label] = {"comparison": comparison, "failures": failures}
        if not failures:
            print(f"gates: {label} passed", flush=True)
    return results


def report_gate_failures(gate_results: dict[str, dict]) -> int:
    failures = [f"{label}: {failure}" for label, result in gate_results.items() for failure in result["failures"]]
    if failures:
        print("\nBenchmark gates failed:", file=sys.stderr)
        for failure in failures:
            print(f"  - {failure}", file=sys.stderr)
        return 1
    print("\nBenchmark gates passed (or no baselines present).")
    return 0


def update_baselines(evaluations: dict[str, dict], rows: list[dict], context: dict, only: list[str]) -> None:
    BASELINES.mkdir(parents=True, exist_ok=True)
    speed_by_engine = {row["engine"]: row.get("speed_seconds") for row in rows}
    for label, evaluation in evaluations.items():
        if only and label not in only:
            continue
        payload = {
            "corpus_revision": context["corpus_revision"],
            "fingerprint": context["fingerprint"],
            "engine_versions": context["engine_versions"],
            "speed": {"protocol": speed_protocol_text(context), "median_seconds": speed_by_engine.get(label)},
            "evaluation": evaluation,
        }
        (BASELINES / f"{label}.json").write_text(json.dumps(payload, indent=2) + "\n", encoding="utf-8")
        print(f"baseline updated: {label}")


def self_test() -> int:
    import copy
    import tempfile

    baseline = {
        "metrics": {"score": {"overall_mean": 0.8}, "missing_predictions": 0},
        "documents": [{"document_id": "a", "scores": {"overall": 0.9}}],
    }
    comparison = gates_module.compare(baseline, copy.deepcopy(baseline))
    if gates_module.evaluate_gates(comparison):
        print("self-test: identical evaluations must pass gates", file=sys.stderr)
        return 1
    regressed = copy.deepcopy(baseline)
    regressed["metrics"]["score"]["overall_mean"] = 0.5
    regressed["documents"][0]["scores"]["overall"] = 0.3
    comparison = gates_module.compare(baseline, regressed)
    if len(gates_module.evaluate_gates(comparison)) < 2:
        print("self-test: injected regression must fail gates", file=sys.stderr)
        return 1
    with tempfile.TemporaryDirectory() as tmp:
        binary = build_binary()
        run(
            [sys.executable, BENCH_DIR / "engines" / "docstomd_adapter.py", "--binary", binary,
             "--corpus", REPO_ROOT / "testdata" / "detect", "--out", Path(tmp) / SUBJECT, "--limit", "3"]
        )
    print("self-test passed")
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--limit", type=int, default=0)
    parser.add_argument("--speed-passes", type=int, default=speed_module.DEFAULT_PASSES)
    parser.add_argument("--speed-warmup", type=int, default=speed_module.DEFAULT_WARMUP)
    parser.add_argument("--update-baselines", nargs="*", metavar="ENGINE")
    parser.add_argument("--self-test", action="store_true")
    parser.add_argument("--gates-only", action="store_true")
    parser.add_argument("--min-overall-delta", type=float, default=0.0)
    parser.add_argument("--max-document-regression", type=float, default=0.02)
    parser.add_argument("--max-missing", type=int, default=0)
    args = parser.parse_args(argv)

    if args.self_test:
        return self_test()

    if args.gates_only:
        results_path = RESULTS / "evaluations.json"
        if not results_path.exists():
            print(f"no recorded run at {results_path}; run the bench first", file=sys.stderr)
            return 1
        recorded = json.loads(results_path.read_text(encoding="utf-8"))
        evaluations = recorded.get("full", {})
        return report_gate_failures(check_gates(evaluations, args))

    corpus_repo = ensure_corpus()
    python = ensure_venv()
    binary = build_binary()

    start = time.perf_counter()
    outputs, runners = engine_runners(corpus_repo, python, binary, args.limit)
    speed = speed_module.alternating_medians(runners, passes=args.speed_passes, warmup=args.speed_warmup)
    evaluations = evaluate(corpus_repo, python, outputs)
    elapsed = time.perf_counter() - start

    context = {
        "corpus_revision": CORPUS_REVISION,
        "corpus_docs": CORPUS_DOC_COUNT if not args.limit else args.limit,
        "fingerprint": machine_fingerprint(),
        "engine_versions": engine_versions(python, binary),
        "speed_passes": args.speed_passes,
        "speed_warmup": args.speed_warmup,
        "speed": speed,
        "harness_elapsed_seconds": round(elapsed, 3),
    }
    rows = engine_rows(evaluations, speed)

    if args.update_baselines is not None:
        report = write_report(rows, context, evaluations, {})
        update_baselines(evaluations, rows, context, args.update_baselines)
        print(f"\n{gates_module.format_scores_table(rows)}\n\nreport: {report}")
        return 0

    gate_results = check_gates(evaluations, args)
    report = write_report(rows, context, evaluations, gate_results)
    print(f"\n{gates_module.format_scores_table(rows)}\n\nreport: {report}")
    return report_gate_failures(gate_results)


if __name__ == "__main__":
    raise SystemExit(main())
