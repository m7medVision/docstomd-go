# Bench

Local benchmark harness. Numbers are machine-comparable only: baselines
record a fingerprint and refuse to compare across machines.

## PDF bench

```sh
make bench-pdf                    # full 200-doc corpus, all engines
make bench-pdf BENCH_ARGS="--limit 5 --speed-passes 1 --speed-warmup 0"  # quick subset
make bench-pdf BENCH_ARGS="--update-baselines"          # record/refresh all baselines
make bench-pdf BENCH_ARGS="--update-baselines docstomd" # refresh one engine's baseline
make bench-pdf-selftest           # gate logic + engine adapters, no corpus
```

What it does:

1. Clones the pinned public 200-PDF corpus revision into `bench/.cache/`
   (git-ignored) and verifies the document count.
2. Prepares an isolated Python environment (uv) with the evaluator
   dependencies plus the baseline engines.
3. Runs three engines over the corpus through the same adapter interface:
   `docstomd` (the real binary, `convert --json` per document, OCR off),
   `markitdown`, and `pymupdf4llm`. Failed conversions — including
   documents docstomd refuses with `needsOcr` — leave missing predictions;
   nothing is special-cased.
4. Speed protocol, identical for every engine: one excluded warm-up round,
   then five measured full-corpus passes alternating between engines
   (sequential, single process); the report shows each engine's median.
   Predictions from the last pass are scored.
5. Scores every engine with the corpus's real evaluator
   (overall / NID / TEDS / MHS + missing predictions).
6. Writes `bench/results/report.md`: machine fingerprint, corpus revision,
   the score table, docstomd's deltas and speedup against each baseline
   engine, and the gate outcome per engine.
7. Gates: compares each engine against `bench/pdf/baselines/<engine>.json`
   when present — fails on overall regression, any document regressing more
   than 0.02, or missing predictions beyond the baseline's count. Baselines
   record corpus revision, engine versions, speed and fingerprint, and are
   refreshed only deliberately via `--update-baselines`.

Re-verify gates without re-running engines:

```sh
make bench-pdf BENCH_ARGS="--gates-only"
```

Unit tests for the gate logic and engine adapters live in `bench/tests`
(`make bench-test`, fakes and tiny inputs only). Tests marked `integration`
build the real docstomd binary once per session and run it over a few
fixtures; `make bench-test-integration` runs them.

## OCR bench

```sh
make bench-ocr                                          # dry: zero provider calls
make bench-ocr BENCH_ARGS="--live --page-budget 10"     # live: bills at most 10 pages
make bench-ocr BENCH_ARGS="--corpus testdata/detect"    # any directory of PDFs
```

Dry mode (the default) converts every PDF in the pinned corpus, plus two
generated routing fixtures, with `--ocr auto --ocr-dry-run` and
`--ocr force --ocr-dry-run`. `MISTRAL_API_KEY` is removed from the
subprocess environment, so no call can reach the provider. For each
document and in aggregate it reports total pages, routed pages, routing
percentage, pages billed and estimated dollars for auto and force.

Routing gates (dry and live):

- `text_dominant_mixed`: 7 text pages plus 1 full-page scan. At most 25% of
  its pages may be routed.
- `fully_scanned`: 4 scanned pages. At least 90% must be routed.

Live mode needs `MISTRAL_API_KEY`. It OCRs the routed corpus documents in
auto and force modes. Before each document, the planned cost (auto plus
force pages, taken from the dry run) is checked against the remaining
`--page-budget`; documents that don't fit are skipped and listed. Every
call also passes `--ocr-max-pages` with the remaining budget, so actual
spend can never exceed it. Both modes' Markdown is scored against the
corpus ground truth with the corpus's real evaluator (overall score). The
target fails when the mean or any single document's auto-minus-force
delta falls below `-max-quality-drop` (default 0.02), or when a live
conversion fails.

Output: `bench/results/ocr-report.md` and `bench/results/ocr-results.json`.

## Formats bench

```sh
cp bench.env.example bench.env                      # once: point at the baseline checkout
make bench-formats                                  # corpus, both engines, report, gates
make bench-formats BENCH_ARGS="--passes 1 --warmup 0"   # quick run
make bench-formats BENCH_ARGS="--update-baseline"   # record bench/formats/baselines/docstomd.json
```

What it does:

1. Assembles the corpus from every `.docx`, `.xlsx` and `.pptx` under
   `testdata/` (deduplicated by content hash; `abuse/` directories are
   excluded), plus generated documents at three sizes per format
   (`bench/formats/generate.py`, deterministic). The generated documents
   contain headings, nested and numbered lists, tables, links, footnotes
   and speaker notes.
2. Builds docstomd, then builds the baseline engine as the git-ignored
   `bench.env` describes: `FORMATS_BASELINE_BUILD` runs inside
   `FORMATS_BASELINE_SRC` with every key in the file exported to the build,
   and `FORMATS_BASELINE_BIN` must print Markdown for `<binary> <file>`.
   A missing env file, key, toolchain or build prints a warning and the
   run continues with docstomd alone. No committed file names the baseline
   or its location.
3. Speed: after the warm-up rounds, each measured pass converts the whole
   corpus with each engine in turn. Every document is one process
   invocation, so small documents mostly measure process startup. The
   per-format table reports the median milliseconds per document and MB/s
   (input bytes over the sum of per-document medians).
4. Metrics per format and engine: statuses (`ok`, `unsupported`, `error`),
   structural counts (headings, table rows, list items, links, footnote
   definitions; fenced code excluded), and word-trigram containment in both
   directions (multiset, lower-cased `\w+` words) over documents the
   baseline converted.
5. Writes `bench/results/formats-report.md` (with the lowest per-document
   containment per format) and `formats-results.json`.
6. Gates per format against `bench/formats/baselines/docstomd.json`:
   - **Targets:** containment must meet the committed `targets`
     (`min_ours_in_baseline`, `min_baseline_in_ours`). These apply on any
     machine.
   - **Regression:** containment must not drop more than `--max-regression`
     (default 0.02) below the recorded value, for the format as a whole and
     for every recorded document. The per-document check matters because the
     format ratio is weighted by trigram count: one small document losing
     all its content barely moves it. This check runs only on the
     fingerprinted machine and the same corpus digest.
   - Without a committed baseline, or without the baseline engine, the gate
     is skipped and the report says why. `--update-baseline` rewrites the
     recorded values and keeps the targets.
