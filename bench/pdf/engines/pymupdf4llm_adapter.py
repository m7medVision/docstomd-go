"""pymupdf4llm engine adapter: converts a corpus through the pymupdf4llm library.

Must run inside the bench venv where pymupdf4llm is installed.
"""

from __future__ import annotations

import argparse
import json
import shutil
import statistics
import time
from pathlib import Path


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--corpus", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--limit", type=int, default=0)
    args = parser.parse_args(argv)

    import pymupdf4llm  # noqa: PLC0415

    corpus = sorted(args.corpus.glob("*.pdf"))
    if args.limit:
        corpus = corpus[: args.limit]
    if not corpus:
        return 1

    markdown_dir = args.out / "markdown"
    if markdown_dir.exists():
        shutil.rmtree(markdown_dir)
    markdown_dir.mkdir(parents=True)

    ok = 0
    failed = 0
    start = time.perf_counter()
    for pdf in corpus:
        try:
            markdown = pymupdf4llm.to_markdown(str(pdf))
        except Exception:
            failed += 1
            continue
        ok += 1
        (markdown_dir / (pdf.stem + ".md")).write_text(markdown or "", encoding="utf-8")
    elapsed = time.perf_counter() - start
    timing = {
        "engine": "pymupdf4llm",
        "documents": len(corpus),
        "passes": [{"ok": ok, "failed": failed, "wall_seconds": elapsed}],
        "median_wall_seconds": elapsed,
    }
    (args.out / "timing.json").write_text(json.dumps(timing, indent=2) + "\n", encoding="utf-8")
    print(f"pymupdf4llm: {ok} converted, {failed} failed, {elapsed:.3f}s")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
