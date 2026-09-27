"""OCR bench: routing percentage, billed pages, estimated spend, auto vs force,
and the local engine's quality and speed.

Usage:
  python3 bench/ocr/run.py                      # dry mode: whole corpus, zero provider calls
  python3 bench/ocr/run.py --live --page-budget 10
  python3 bench/ocr/run.py --corpus DIR         # any directory of PDFs
  python3 bench/ocr/run.py --local --local-models pp-ocrv5-mobile --local-backends go

Dry mode runs every corpus document plus the generated routing fixtures
through `convert --ocr-dry-run` with the provider key removed from the
environment, so no call can reach the provider. Live mode (needs
MISTRAL_API_KEY) OCRs the routed subset in auto and force modes under a
hard page budget and scores both against the corpus ground truth with the
corpus's real evaluator.

Local mode (--local) force-OCRs born-digital corpus pages with
docstomd-ocr-local for every model × backend and scores the OCR text against
the page's own text layer (word F1), with pages per second. With --live too,
the local engine also OCRs the routed subset and its evaluator score is shown
next to Mistral's; that comparison is reported, never gated.
"""

from __future__ import annotations

import argparse
import importlib.util
import json
import os
import re
import statistics
import subprocess
import sys
import time
from collections import Counter
from pathlib import Path

OCR_DIR = Path(__file__).resolve().parent
BENCH_DIR = OCR_DIR.parent
RESULTS = BENCH_DIR / "results"
KEY_ENV = "MISTRAL_API_KEY"
ENGINES = ("auto", "force")

sys.path.insert(0, str(OCR_DIR))
import fixtures  # noqa: E402


def load_pdf_bench():
    spec = importlib.util.spec_from_file_location("pdf_run", BENCH_DIR / "pdf" / "run.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def convert(binary: Path, pdf: Path, mode: str, *, dry: bool, max_pages: int = 0,
            provider: str = "mistral", extra: list[str] | None = None, env_extra: dict | None = None) -> dict:
    command = [str(binary), "convert", "--json", "--ocr", mode, "--ocr-provider", provider] + (extra or [])
    env = dict(os.environ)
    env.update(env_extra or {})
    if dry:
        command.append("--ocr-dry-run")
        env.pop(KEY_ENV, None)
    if max_pages:
        command += ["--ocr-max-pages", str(max_pages)]
    command.append(str(pdf))
    completed = subprocess.run(command, capture_output=True, text=True, env=env, check=False)
    try:
        payload = json.loads(completed.stdout)
    except json.JSONDecodeError:
        payload = {}
    if completed.returncode != 0 or "error" in payload:
        detail = payload.get("error", {}).get("message") or completed.stderr.strip()[:200]
        return {"error": f"exit {completed.returncode}: {detail}"}
    return payload


def dry_run(binary: Path, pdfs: list[Path], group: str) -> list[dict]:
    rows = []
    for pdf in pdfs:
        row = {"document": pdf.stem, "group": group, "path": str(pdf)}
        auto = convert(binary, pdf, "auto", dry=True)
        force = convert(binary, pdf, "force", dry=True)
        if "error" in auto or "error" in force:
            row["error"] = auto.get("error") or force.get("error")
            rows.append(row)
            continue
        auto_cost = auto.get("ocr_cost") or {}
        force_cost = force.get("ocr_cost") or {}
        row.update(
            pages=auto["page_count"],
            routed_pages=len(auto.get("pages_needing_ocr") or []),
            auto_billed=auto_cost.get("pages_billed", 0),
            force_billed=force_cost.get("pages_billed", 0),
            auto_est_usd=auto_cost.get("estimated_cost_usd", 0.0),
            force_est_usd=force_cost.get("estimated_cost_usd", 0.0),
            provider=auto_cost.get("provider"),
            provider_calls=sum(0 if cost.get("dry_run", True) else 1 for cost in (auto_cost, force_cost)),
        )
        rows.append(row)
    return rows


def aggregate(rows: list[dict]) -> dict:
    scored = [row for row in rows if "error" not in row]
    pages = sum(row["pages"] for row in scored)
    routed = sum(row["routed_pages"] for row in scored)
    return {
        "documents": len(scored),
        "failed": len(rows) - len(scored),
        "documents_routed": sum(1 for row in scored if row["routed_pages"]),
        "pages": pages,
        "routed_pages": routed,
        "routing_pct": 100.0 * routed / pages if pages else 0.0,
        "auto_billed": sum(row["auto_billed"] for row in scored),
        "force_billed": sum(row["force_billed"] for row in scored),
        "auto_est_usd": sum(row["auto_est_usd"] for row in scored),
        "force_est_usd": sum(row["force_est_usd"] for row in scored),
    }


def routing_gate_failures(rows: list[dict]) -> list[str]:
    by_name = {row["document"]: row for row in rows if row["group"] == "fixture"}
    failures = []
    for name, spec in fixtures.ROUTING_FIXTURES.items():
        row = by_name.get(name)
        if row is None or "error" in row:
            failures.append(f"routing fixture {name} did not convert: {row and row.get('error')}")
            continue
        fraction = row["routed_pages"] / row["pages"] if row["pages"] else 0.0
        if "max_routed_fraction" in spec and fraction > spec["max_routed_fraction"]:
            failures.append(f"routing fixture {name} routed {fraction:.0%} of pages (maximum {spec['max_routed_fraction']:.0%})")
        if "min_routed_fraction" in spec and fraction < spec["min_routed_fraction"]:
            failures.append(f"routing fixture {name} routed {fraction:.0%} of pages (minimum {spec['min_routed_fraction']:.0%})")
    return failures


def plan_live(rows: list[dict], budget: int) -> tuple[list[dict], list[dict]]:
    selected, skipped = [], []
    remaining = budget
    for row in rows:
        if row["group"] != "corpus" or "error" in row or not row["routed_pages"]:
            continue
        need = row["auto_billed"] + row["force_billed"]
        if need > remaining:
            skipped.append(row)
            continue
        selected.append(row)
        remaining -= need
    return selected, skipped


def live_run(binary: Path, rows: list[dict], budget: int, out: Path) -> dict:
    selected, skipped = plan_live(rows, budget)
    for engine in ENGINES:
        (out / f"docstomd-ocr-{engine}" / "markdown").mkdir(parents=True, exist_ok=True)
    billed = 0
    spend = 0.0
    documents = []
    for row in selected:
        entry = {"document": row["document"]}
        for engine in ENGINES:
            remaining = budget - billed
            if remaining <= 0:
                entry[engine] = {"error": "page budget exhausted"}
                continue
            payload = convert(binary, Path(row["path"]), engine, dry=False, max_pages=remaining)
            if "error" in payload:
                entry[engine] = {"error": payload["error"]}
                continue
            cost = payload.get("ocr_cost") or {}
            billed += cost.get("pages_billed", 0)
            spend += cost.get("estimated_cost_usd", 0.0)
            entry[engine] = {"pages_billed": cost.get("pages_billed", 0), "est_usd": cost.get("estimated_cost_usd", 0.0),
                             "needs_review": payload.get("needs_review") or []}
            markdown = out / f"docstomd-ocr-{engine}" / "markdown" / f"{row['document']}.md"
            markdown.write_text(payload.get("markdown", ""), encoding="utf-8")
        documents.append(entry)
    return {
        "page_budget": budget,
        "pages_billed": billed,
        "est_usd": spend,
        "documents": documents,
        "skipped": [{"document": row["document"], "needed_pages": row["auto_billed"] + row["force_billed"]} for row in skipped],
    }


def evaluate_quality(python: Path, corpus_repo: Path, out: Path, document_ids: list[str]) -> dict[str, dict]:
    quality: dict[str, dict] = {doc_id: {} for doc_id in document_ids}
    for engine in ENGINES:
        label = f"docstomd-ocr-{engine}"
        for doc_id in document_ids:
            subprocess.run(
                [str(python), "src/evaluator.py", "--prediction-root", str(out), "--engine", label,
                 "--doc-id", doc_id, "--log-level", "ERROR"],
                cwd=corpus_repo,
                check=True,
                stdout=subprocess.DEVNULL,
            )
            evaluation = json.loads((out / label / "evaluation.json").read_text(encoding="utf-8"))
            for document in evaluation.get("documents", []):
                overall = document.get("scores", {}).get("overall")
                if document["document_id"] == doc_id and overall is not None:
                    quality[doc_id][engine] = float(overall)
    return quality


def quality_summary(quality: dict[str, dict]) -> dict:
    deltas = {doc_id: scores["auto"] - scores["force"] for doc_id, scores in quality.items() if "auto" in scores and "force" in scores}
    return {
        "documents": quality,
        "deltas": deltas,
        "mean_delta": statistics.fmean(deltas.values()) if deltas else None,
    }


def quality_gate_failures(summary: dict, max_quality_drop: float) -> list[str]:
    failures = []
    if summary["mean_delta"] is not None and summary["mean_delta"] < -max_quality_drop:
        failures.append(f"auto vs force mean quality delta {summary['mean_delta']:+.4f} exceeds -{max_quality_drop:.4f}")
    for doc_id, delta in sorted(summary["deltas"].items()):
        if delta < -max_quality_drop:
            failures.append(f"document {doc_id} auto vs force quality delta {delta:+.4f} exceeds -{max_quality_drop:.4f}")
    return failures


def format_documents_table(rows: list[dict]) -> str:
    header = f"{'document':<24} {'group':<8} {'pages':>6} {'routed':>7} {'auto$':>9} {'force$':>9}"
    lines = [header, "-" * len(header)]
    for row in rows:
        if "error" in row:
            lines.append(f"{row['document']:<24} {row['group']:<8} error: {row['error']}")
            continue
        lines.append(
            f"{row['document']:<24} {row['group']:<8} {row['pages']:>6} {row['routed_pages']:>7} "
            f"{row['auto_est_usd']:>9.4f} {row['force_est_usd']:>9.4f}"
        )
    return "\n".join(lines)


def format_aggregate(label: str, totals: dict) -> str:
    return (
        f"{label}: {totals['documents']} docs ({totals['failed']} failed), {totals['pages']} pages, "
        f"routing {totals['routed_pages']} pages ({totals['routing_pct']:.2f}%) in {totals['documents_routed']} docs; "
        f"auto bills {totals['auto_billed']} pages ≈ ${totals['auto_est_usd']:.4f}, "
        f"force bills {totals['force_billed']} pages ≈ ${totals['force_est_usd']:.4f}"
    )


WORD = re.compile(r"\w+", re.UNICODE)


def word_f1(predicted: str, reference: str) -> float:
    """Bag-of-words F1 between two texts, case-folded; Markdown syntax and
    reading order do not matter."""
    p = Counter(w.casefold() for w in WORD.findall(predicted))
    r = Counter(w.casefold() for w in WORD.findall(reference))
    if not p and not r:
        return 1.0
    overlap = sum((p & r).values())
    if not overlap:
        return 0.0
    precision = overlap / sum(p.values())
    recall = overlap / sum(r.values())
    return 2 * precision * recall / (precision + recall)


def local_env(engine: Path | None, models_root: Path | None) -> dict:
    env = {}
    if engine:
        env["DOCSTOMD_OCR_LOCAL"] = str(engine)
    if models_root:
        env["DOCSTOMD_OCR_MODELS"] = str(models_root)
    return env


def local_run(binary: Path, rows: list[dict], models: list[str], backends: list[str], page_limit: int,
              env_extra: dict) -> list[dict]:
    """Force-OCRs whole born-digital documents (no routed pages) until
    page_limit pages per model × backend; scores each OCR text against the
    document's own text layer and times it. Documents that do not fit the
    remaining budget are skipped: a capped document would keep native text
    on its uncapped pages and inflate the score."""
    born_digital = [row for row in rows if row["group"] == "corpus" and "error" not in row and not row["routed_pages"]]
    results = []
    for model in models:
        for backend in backends:
            pages, seconds, scores, errors = 0, 0.0, [], []
            for row in born_digital:
                if pages + row["pages"] > page_limit:
                    continue
                pdf = Path(row["path"])
                native = convert(binary, pdf, "off", dry=False)
                started = time.monotonic()
                ocr = convert(binary, pdf, "force", dry=False, provider="local",
                              extra=["--ocr-model", model, "--ocr-backend", backend], env_extra=env_extra)
                elapsed = time.monotonic() - started
                if "error" in ocr or "error" in native:
                    errors.append(f"{row['document']}: {ocr.get('error') or native.get('error')}")
                    continue
                pages += (ocr.get("ocr_cost") or {}).get("pages_billed", 0)
                seconds += elapsed
                scores.append(word_f1(ocr.get("markdown", ""), native.get("markdown", "")))
            results.append({
                "model": model,
                "backend": backend,
                "documents": len(scores),
                "pages": pages,
                "seconds": seconds,
                "pages_per_sec": pages / seconds if seconds else 0.0,
                "text_f1": statistics.fmean(scores) if scores else None,
                "errors": errors,
            })
    return results


def format_local_table(rows: list[dict]) -> str:
    header = f"{'model':<26} {'backend':<8} {'docs':>5} {'pages':>6} {'pages/s':>8} {'text F1':>8}"
    lines = [header, "-" * len(header)]
    for row in rows:
        f1 = "n/a" if row["text_f1"] is None else f"{row['text_f1']:.3f}"
        lines.append(f"{row['model']:<26} {row['backend']:<8} {row['documents']:>5} {row['pages']:>6} "
                     f"{row['pages_per_sec']:>8.3f} {f1:>8}")
        for error in row["errors"]:
            lines.append(f"  error: {error}")
    return "\n".join(lines)


def write_report(results: Path, payload: dict) -> Path:
    results.mkdir(parents=True, exist_ok=True)
    body = ["# OCR bench report", "", f"- mode: {payload['mode']}"]
    for label, totals in payload["aggregate"].items():
        body.append(f"- {format_aggregate(label, totals)}")
    live = payload.get("live")
    if live:
        body += [
            "",
            "## Live run",
            "",
            f"- page budget {live['page_budget']}, billed {live['pages_billed']} pages ≈ ${live['est_usd']:.4f}",
            f"- documents OCRed: {len(live['documents'])}; skipped for budget: {len(live['skipped'])}",
        ]
        quality = payload.get("quality")
        if quality:
            mean = quality["mean_delta"]
            body.append(f"- auto vs force quality delta (overall, mean): {'n/a' if mean is None else f'{mean:+.4f}'}")
            for doc_id, scores in sorted(quality["documents"].items()):
                body.append(f"  - {doc_id}: auto={scores.get('auto')} force={scores.get('force')}")
    local = payload.get("local")
    if local:
        body += ["", "## Local OCR (born-digital pages, text F1 against the text layer)", "", "```",
                 format_local_table(local), "```"]
    comparison = payload.get("local_vs_mistral")
    if comparison:
        body += ["", "## Local vs Mistral on the scanned subset (report only, never gates)", ""]
        for doc_id, scores in sorted(comparison.items()):
            body.append(f"- {doc_id}: mistral={scores.get('mistral')} local={scores.get('local')}")
    if payload.get("gate_failures"):
        body += ["", "## Gate failures", ""] + [f"- {failure}" for failure in payload["gate_failures"]]
    body += ["", "## Documents", "", "```", format_documents_table(payload["documents"]), "```"]
    report = results / "ocr-report.md"
    report.write_text("\n".join(body) + "\n", encoding="utf-8")
    (results / "ocr-results.json").write_text(json.dumps(payload, indent=2) + "\n", encoding="utf-8")
    return report


def build_engine() -> Path:
    """Builds docstomd-ocr-local from the ocr/ module."""
    repo = BENCH_DIR.parent
    engine = repo / "ocr" / "cmd" / "docstomd-ocr-local" / "docstomd-ocr-local"
    env = dict(os.environ, CGO_ENABLED="0", GOWORK="off")
    subprocess.run(["go", "build", "-o", str(engine), "./cmd/docstomd-ocr-local"], cwd=repo / "ocr", env=env, check=True)
    return engine


def local_vs_mistral(binary: Path, rows: list[dict], scored: list[str], out: Path, python: Path, corpus_repo: Path,
                     model: str, env_extra: dict, quality: dict) -> dict:
    """OCRs the documents Mistral scored with the local engine (auto mode) and
    scores both with the corpus evaluator. Reported only."""
    label = "docstomd-ocr-local"
    (out / label / "markdown").mkdir(parents=True, exist_ok=True)
    by_name = {row["document"]: row for row in rows}
    comparison = {}
    for doc_id in scored:
        payload = convert(binary, Path(by_name[doc_id]["path"]), "auto", dry=False, provider="local",
                          extra=["--ocr-model", model], env_extra=env_extra)
        entry = {"mistral": quality["documents"].get(doc_id, {}).get("auto")}
        if "error" in payload:
            entry["local"] = payload["error"]
            comparison[doc_id] = entry
            continue
        (out / label / "markdown" / f"{doc_id}.md").write_text(payload.get("markdown", ""), encoding="utf-8")
        subprocess.run([str(python), "src/evaluator.py", "--prediction-root", str(out), "--engine", label,
                        "--doc-id", doc_id, "--log-level", "ERROR"], cwd=corpus_repo, check=True, stdout=subprocess.DEVNULL)
        evaluation = json.loads((out / label / "evaluation.json").read_text(encoding="utf-8"))
        for document in evaluation.get("documents", []):
            if document["document_id"] == doc_id:
                entry["local"] = document.get("scores", {}).get("overall")
        comparison[doc_id] = entry
    return comparison


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--corpus", type=Path, help="directory of PDFs (default: the pinned PDF bench corpus)")
    parser.add_argument("--results", type=Path, default=RESULTS)
    parser.add_argument("--binary", type=Path, help="docstomd binary (default: build it)")
    parser.add_argument("--live", action="store_true", help="call the provider (bills pages)")
    parser.add_argument("--page-budget", type=int, default=10)
    parser.add_argument("--max-quality-drop", type=float, default=0.02)
    parser.add_argument("--local", action="store_true", help="bench the local engine (docstomd-ocr-local)")
    parser.add_argument("--local-models", default="pp-ocrv5-mobile", help="comma-separated installed model ids")
    parser.add_argument("--local-backends", default="go", help="comma-separated backends")
    parser.add_argument("--local-pages", type=int, default=10, help="born-digital pages per model × backend")
    parser.add_argument("--local-engine", type=Path, help="docstomd-ocr-local binary (default: build it)")
    parser.add_argument("--models-root", type=Path, help="installed models directory (default: the engine's)")
    args = parser.parse_args(argv)

    if args.live and not os.environ.get(KEY_ENV):
        print(f"live mode needs {KEY_ENV} in the environment", file=sys.stderr)
        return 2
    if args.live and args.page_budget <= 0:
        print("live mode needs a positive --page-budget", file=sys.stderr)
        return 2

    pdf_bench = load_pdf_bench()
    corpus = args.corpus if args.corpus else pdf_bench.ensure_corpus() / "pdfs"
    binary = args.binary if args.binary else pdf_bench.build_binary()

    corpus_rows = dry_run(binary, sorted(corpus.glob("*.pdf")), group="corpus")
    fixture_paths = fixtures.write_routing_fixtures(args.results / "ocr-fixtures")
    fixture_rows = dry_run(binary, list(fixture_paths.values()), group="fixture")
    rows = corpus_rows + fixture_rows
    failures = routing_gate_failures(rows)
    provider_calls = sum(row.get("provider_calls", 0) for row in rows)
    if provider_calls:
        failures.append(f"dry mode reported {provider_calls} provider calls")

    payload = {
        "mode": "live" if args.live else "dry",
        "corpus": str(corpus),
        "aggregate": {"corpus": aggregate(corpus_rows), "fixtures": aggregate(fixture_rows)},
        "documents": rows,
    }

    env_extra = {}
    if args.local:
        engine = args.local_engine or build_engine()
        env_extra = local_env(engine, args.models_root)
        payload["local"] = local_run(binary, corpus_rows, args.local_models.split(","), args.local_backends.split(","),
                                     args.local_pages, env_extra)

    if args.live:
        out = args.results / "ocr-predictions"
        live = live_run(binary, corpus_rows, args.page_budget, out)
        payload["live"] = live
        scored = [entry["document"] for entry in live["documents"]]
        if scored:
            python = pdf_bench.ensure_venv()
            summary = quality_summary(evaluate_quality(python, corpus.parent, out, scored))
            payload["quality"] = summary
            failures += quality_gate_failures(summary, args.max_quality_drop)
        if args.local and scored:
            payload["local_vs_mistral"] = local_vs_mistral(binary, corpus_rows, scored, out, python, corpus.parent,
                                                           args.local_models.split(",")[0], env_extra, payload["quality"])
        for entry in live["documents"]:
            for engine in ENGINES:
                if "error" in entry.get(engine, {}):
                    failures.append(f"live {engine} conversion of {entry['document']} failed: {entry[engine]['error']}")

    payload["gate_failures"] = failures
    report = write_report(args.results, payload)
    print("\n".join(format_aggregate(label, totals) for label, totals in payload["aggregate"].items()))
    if args.live:
        print(f"live: billed {payload['live']['pages_billed']} of {args.page_budget} budgeted pages ≈ ${payload['live']['est_usd']:.4f}")
    print(f"report: {report}")
    if failures:
        print("\nOCR bench gates failed:", file=sys.stderr)
        for failure in failures:
            print(f"  - {failure}", file=sys.stderr)
        return 1
    print("\nOCR bench gates passed.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
