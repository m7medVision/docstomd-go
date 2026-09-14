"""docstomd engine adapter: converts a corpus through the docstomd binary.

Writes predictions as <out>/markdown/<doc-id>.md for every document the
binary converts successfully; documents that fail conversion are left
missing so the evaluator counts them. Timing is recorded per pass.
"""

from __future__ import annotations

import argparse
import json
import shutil
import statistics
import subprocess
import sys
import time
from pathlib import Path


def convert_one(binary: str, pdf: Path) -> tuple[str, str, float]:
    start = time.perf_counter()
    completed = subprocess.run(
        [binary, "convert", "--json", str(pdf)],
        capture_output=True,
        text=True,
        check=False,
    )
    elapsed = time.perf_counter() - start
    if completed.returncode != 0:
        return "", f"exit {completed.returncode}: {completed.stderr.strip()[:200]}", elapsed
    try:
        payload = json.loads(completed.stdout)
    except json.JSONDecodeError as exc:
        return "", f"bad json: {exc}", elapsed
    markdown = payload.get("markdown")
    if not isinstance(markdown, str):
        return "", "payload missing markdown", elapsed
    return markdown, "", elapsed


def run_pass(binary: str, corpus: list[Path], out_dir: Path) -> dict:
    markdown_dir = out_dir / "markdown"
    if markdown_dir.exists():
        shutil.rmtree(markdown_dir)
    markdown_dir.mkdir(parents=True)
    ok = 0
    errors: dict[str, str] = {}
    wall_start = time.perf_counter()
    for pdf in corpus:
        markdown, error, _elapsed = convert_one(binary, pdf)
        if error:
            errors[pdf.name] = error
            continue
        ok += 1
        (markdown_dir / (pdf.stem + ".md")).write_text(markdown, encoding="utf-8")
    return {
        "ok": ok,
        "failed": len(errors),
        "wall_seconds": time.perf_counter() - wall_start,
        "errors": errors,
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True)
    parser.add_argument("--corpus", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--limit", type=int, default=0)
    parser.add_argument("--speed-passes", type=int, default=1)
    args = parser.parse_args(argv)

    corpus = sorted(args.corpus.glob("*.pdf"))
    if args.limit:
        corpus = corpus[: args.limit]
    if not corpus:
        print(f"no PDFs found in {args.corpus}", file=sys.stderr)
        return 1

    passes = []
    for _ in range(max(1, args.speed_passes)):
        passes.append(run_pass(args.binary, corpus, args.out))
    timing = {
        "engine": "docstomd",
        "documents": len(corpus),
        "passes": passes,
        "median_wall_seconds": statistics.median(p["wall_seconds"] for p in passes),
    }
    (args.out / "timing.json").write_text(json.dumps(timing, indent=2) + "\n", encoding="utf-8")
    print(
        f"docstomd: {passes[-1]['ok']} converted, {passes[-1]['failed']} failed, "
        f"median pass {timing['median_wall_seconds']:.3f}s over {len(passes)} pass(es)"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
