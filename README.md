# docstomd

Convert documents (PDF, DOCX, XLSX, PPTX) to clean, LLM-ready
GitHub-Flavored Markdown, as a Go library and a single CLI binary.

- **PDF:** classified in milliseconds. Each page is routed to native text
  extraction or to OCR. The geometry-driven pipeline emits headings, lists,
  paragraphs and pipe tables.
- **OCR only where needed:** scanned or garbled pages go to a pluggable OCR
  provider: Mistral, the local CPU engine `docstomd-ocr-local`, or any
  program you write that speaks the [OCR protocol](docs/ocr-protocol.md), no
  PR needed. Page caps and a dry run keep the bill known before it is
  incurred.
- **Office formats:** DOCX, XLSX and PPTX share one intermediate
  representation and one Markdown serializer.
- **Measured:** local benchmarks gate quality and speed against committed
  baselines (`make bench-pdf`, `make bench-ocr`, `make bench-formats`).

Pure Go, no cgo, MIT licensed.

## Install

Requires Go 1.26 or newer.

```sh
go install github.com/m7medVision/docstomd-go/cmd/docstomd@latest
```

Library:

```sh
go get github.com/m7medVision/docstomd-go
```

## CLI

```text
docstomd convert [flags] <file>    convert a document to Markdown
docstomd convert --out-dir DIR [flags] <file>...   convert several documents
docstomd detect  [flags] <file>    report the detected format (and PDF classification)
docstomd ocr list | install <id>   manage local OCR models (runs docstomd-ocr-local)
docstomd help                      print usage
```

The format is detected from the file content, not the extension. Flags may
appear before or after the file.

### `docstomd convert`

| Flag | Default | Meaning |
|------|---------|---------|
| `--json` | off | Emit a JSON object with the Markdown and metadata instead of bare Markdown |
| `--ocr off\|auto\|force` | `off` | OCR mode (see [OCR](#ocr)) |
| `--ocr-dry-run` | off | With `--ocr auto` or `force`, report the pages that would be billed and the estimated cost; never calls the provider. Without one of those modes it is a usage error |
| `--ocr-max-pages N` | `0` (unlimited) | Maximum pages sent to OCR per document |
| `--ocr-max-pages-run N` | `0` (unlimited) | Maximum pages sent to OCR across all inputs of the run |
| `--ocr-provider P` | see [Providers](#providers) | `mistral`, `local`, `exec:<path>` or a provider name from the config file |
| `--ocr-model ID` | provider default | Mistral model (`mistral-ocr-latest`; pin a dated version for reproducible output) or local model id |
| `--ocr-lang L` | none | Local OCR: first installed model that reads language `L` |
| `--ocr-backend B` | `auto` | Local OCR inference backend |
| `--ocr-catalog C` | none | Local OCR: extra model catalog file or `https://` URL (repeatable) |
| `--out-dir DIR` | none | Write `<name>.md` (`<name>.json` with `--json`) per input into `DIR`; required for several inputs, which then share one OCR engine session and one page budget |
| `--items-json` | off | PDF debugging: dump positioned text items as JSON |

`--json` output (a one-page PDF with a garbled text layer, `--ocr auto --ocr-dry-run`):

```json
{
  "markdown": "",
  "format": "pdf",
  "page_count": 1,
  "pages_needing_ocr": [1],
  "ocr_reasons_by_page": {"1": ["suspected_garbled_text"]},
  "layout": {"is_complex": false, "pages_with_columns": null, "pages_with_tables": null},
  "processing_time_ms": 2,
  "has_encoding_issues": true,
  "ocr_cost": {"provider": "mistral", "pages_billed": 1, "billed_pages": [1],
               "estimated_cost_usd": 0.004, "dry_run": true, "truncated_by_caps": false}
}
```

`ocr_cost` appears only when the OCR router ran. `needs_review` appears
when OCR was weak, missing or capped for some pages, which then kept their
native text. `lost_content_pages` appears when a PDF page's content (or a
form it draws) could not be loaded or decoded. That part is skipped rather
than read as raw bytes, so its text is missing from `markdown`.

### `docstomd detect`

```sh
docstomd detect --json report.pdf
```

Prints the format and, for PDFs, the classification (`text_based`, `scanned`,
`image_based`, `mixed`), page count, confidence, and the per-page OCR routing
with reasons. Detection never calls a provider.

### Exit codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | Usage error |
| 2 | Unsupported input format |
| 3 | OCR required: the PDF is scanned or image-based and `--ocr` is `off` |
| 4 | Any other conversion error (malformed, encrypted, I/O, OCR provider) |
| 5 | OCR cannot run here (`ocrUnavailable`: no engine, runtime or model) or the OCR engine broke the protocol (`ocrProtocol`) |

With `--json`, errors are printed to stdout as
`{"error": {"code": "...", "message": "...", "pages": [...], "page_count": N}}`.
The stable codes are `unsupported`, `needsOcr`, `malformed`, `encrypted`,
`resourceLimit`, `missingPart`, `io`, and for OCR `ocrAuth` (API key missing
or rejected), `ocrRateLimited` (still rate limited after retries),
`ocrProvider` (any other provider failure), `ocrUnavailable`, `ocrProtocol`
and `canceled` (interrupted). With several inputs, each failure is reported
on stderr as `docstomd: <file>: <error>`, the other inputs still convert, and
the exit code is that of the first failure.

## Formats

### PDF

```sh
docstomd convert paper.pdf > paper.md
docstomd convert --json paper.pdf | jq -r .markdown
```

Text-based PDFs convert without any network access. A PDF classified as
`scanned` or `image_based` exits with code 3 and lists the pages, unless OCR
is enabled. On other PDFs, routed pages keep whatever native text they have
while OCR is off.

### Office formats: common behavior

DOCX, XLSX and PPTX parse into one intermediate representation and render
through a single GFM serializer, so all three share the same Markdown style:
ATX headings, `-` bullets, real list numbers, pipe tables, `[^n]` footnotes,
`$...$` / `$$...$$` math, and HTML anchors for internal links.

- **Detection:** the format comes from the package content (the main part
  and its content type), so a misnamed file still converts. The file name
  is a hint used only when the content is inconclusive, for example an empty
  or truncated zip. Then `report.docx` fails as `malformed` (exit 4) rather
  than `unsupported` (exit 2). The CLI always passes the input path; library
  callers can set `Options.FileName`.
- **Metadata:** `page_count`, `pages_needing_ocr` and `layout` apply to PDFs
  only. OCR options are ignored for office formats.
- **Recovery:** a broken part (for example a missing or unreadable styles,
  numbering or notes part) is skipped and the rest converts.
  Password-protected (encrypted) packages fail with `encrypted`.
- **Resource limits:** fixed and not configurable. Each archive is limited to
  128 MiB per entry, 512 MiB in total and 100,000 entries. XML is limited to
  a depth of 256 and 2,000,000 nodes. Table grids and merge expansion are
  limited to 4,000,000 slots each. Exceeding a limit fails with
  `resourceLimit`, the only fatal error.
- **Images, OLE objects, charts and SmartArt:** images and embedded objects
  contribute their alt text or a short placeholder. Charts and SmartArt
  contribute their text. Binary payloads are not written out.

### DOCX

```sh
docstomd convert report.docx > report.md
```

From `testdata/docx/text.docx` (excerpt):

```markdown
# Fixture Document

Plain paragraph with **bold**, *italic*, and ~~struck~~ runs.

## Lists

1. First numbered

2. Second numbered

   - a) Alpha sub one

   - b) Alpha sub two

        - i. Roman sub sub

3. Third numbered

Interrupting paragraph between lists.

4. Fourth, continuing the count

## Notes and special text

Music clef 𝄞 appears before this footnote[^1] reference.

External link to [example](https://example.com/page).

<a id="plainmark"></a>This plain paragraph carries a bookmark.

Jump to [the bookmarked paragraph](#plainmark).

[^1]: Footnote after an astral character.
```

- **Headings:** from heading styles and outline levels. The heading style's
  own bold or italic is not repeated as emphasis.
- **Lists:** numbered and bulleted, resolved through the document's
  numbering definitions. Counters continue across interrupting paragraphs,
  and restarts and start overrides are honored. Composite labels (for
  example `a)`, `IV.`) are kept as literal text where Markdown cannot express
  them.
- **Tables:** merged cells (horizontal spans and vertical merges) are
  expanded onto a regular grid. The header row is detected.
- **Notes and links:** footnotes and endnotes are renumbered by first
  reference. Hyperlinks resolve external, relative and bookmark targets,
  including `HYPERLINK` fields.
- **Math and text boxes:** equations (OMML) become LaTeX, inline or display.
  Text boxes follow their anchor paragraph. `Quote` and code styles become
  quote and code blocks.
- `.docm` (macro-enabled) packages convert the same way; macros are ignored.

### XLSX

```sh
docstomd convert workbook.xlsx > workbook.md
```

From `testdata/detect/sheet.xlsx`:

```markdown
## Values

| Kind | Value | Note |
| --- | --- | --- |
| Percent | 15.5% | fifteen and a half |
| Currency | $1,234.50 | dollars |
| Thousands | 9,876,543 | grouped |
| Date | 2026-03-15 | ides of March |
| Duration | 26:30:15 | over a day |
| Tiny | 0.0000004 | four ten-millionths |
| Boolean | TRUE | yes |

## Merged Grid

|  |  |  |
| --- | --- | --- |
| Span two |  | Right |
| 1 | 2 | 3 |
```

- **Sheets:** each visible sheet becomes one table, in workbook order. Sheet
  names become level-2 headings when there is more than one sheet. Hidden
  sheets, rows and columns are omitted, and empty trailing rows and columns
  are trimmed.
- **Values:** cell values are formatted through the cell's number format:
  multi-section formats with conditions, currency and locale tags, grouping,
  percent, scientific, fractions, and builtin format ids. Dates render
  ISO-like and elapsed durations as `[h]:mm:ss`. A format outside the
  supported grammar renders as General instead of being approximated.
- **Numbers and errors:** floats keep 15 significant digits. Error literals
  (`#DIV/0!` and so on) and booleans render as Excel shows them.
- **Tables:** merged ranges are widened across their full extent. A header row
  is detected when the first row looks like column labels. Otherwise the
  table gets an empty header, as in the merged-grid example above.

### PPTX

```sh
docstomd convert deck.pptx > deck.md
```

From `testdata/pptx/pres.pptx`:

```markdown
Deck Title Slide

- Top level point

  - Nested detail

- Second point with emphasis

> Speaker note for the intro slide.

Numbers Slide

| Region | Total |
| --- | --- |
| North | 42 |

Grouped shapes below.

Inside a group shape.

> Second slide notes mention the table.
```

- **Slide order:** slides convert in presentation order. Title placeholders
  become level-2 headings. Text in other shapes (as in this deck) stays
  paragraphs.
- **Text styling:** styling follows the full inheritance chain: run,
  paragraph, shape, layout placeholder, master, then presentation defaults.
  Bullets and auto-numbering become nested lists.
- **Speaker notes:** they follow each slide as a quote block.
- **Links:** a link to another slide targets an anchor emitted on that slide:

  ```markdown
  [Jump to the second slide](#slide-2)

  <a id="slide-2"></a>
  ```

- **Furniture:** slide number, date and footer placeholders are skipped.
  Tables, grouped shapes, charts and SmartArt contribute their text, and
  equations become LaTeX.

## OCR

OCR applies to PDFs only. Each page is routed to OCR when the detector finds
it `scanned`, `no_text` or `vector_text`, or when the text-quality check
finds its text layer garbled (`suspected_garbled_text`).

| Mode | Pages sent to the provider |
|------|----------------------------|
| `off` (default) | None. A scanned or image-based document fails with `needsOcr` (exit 3) |
| `auto` | Exactly the routed pages, batched into one provider call per document |
| `force` | Every page of the document, in one call |

OCR output replaces a page's native Markdown only when it passes the same
text-quality checks. If the answer is empty, low-confidence or garbled, the
native text is kept and the page is listed in `needs_review`.

### Providers

| `--ocr-provider` | What runs |
|---|---|
| `mistral` | Mistral OCR API (needs `MISTRAL_API_KEY`) |
| `local` | `docstomd-ocr-local`, the local CPU engine, found via `$DOCSTOMD_OCR_LOCAL`, next to the `docstomd` binary, then `PATH` |
| `exec:<path>` | Any program speaking the [docstomd OCR protocol](docs/ocr-protocol.md) |
| `<name>` or `exec:<name>` | A provider named in the config file |

Without the flag, docstomd uses the config file's `default`, else `mistral`
when `MISTRAL_API_KEY` is set, else `local`. A missing engine fails with
`ocrUnavailable` (exit 5) and an install hint.

### Local OCR

`docstomd-ocr-local` runs OCR models on your CPU: no API key, no network
after install, no cgo. It lives in its own Go module (`ocr/`, Go 1.27) so
the core library keeps its tiny dependency set.

```sh
go install github.com/m7medVision/docstomd-go/ocr/cmd/docstomd-ocr-local@latest
docstomd ocr list                      # catalog models, sizes, licences, what is installed
docstomd ocr install pp-ocrv5-mobile   # download and SHA-256-verify a model
docstomd convert --ocr auto --ocr-provider local scan.pdf
```

Nothing is downloaded behind your back: models arrive only through
`docstomd ocr install`, into `$DOCSTOMD_OCR_MODELS` or the user data directory
(`~/.local/share/docstomd/ocr/models` on Linux). Every file is pinned to a
Hugging Face commit (`hf://<org>/<repo>@<commit>/<file>`, honouring
`HF_ENDPOINT`) or an `https://` URL, and checked against its SHA-256. A failed
download leaves no partial model.

- **Pick a model** with `--ocr-model <id>` or a language with
  `--ocr-lang <code>`. Otherwise docstomd uses the only installed model, else
  the first installed one in catalog order.
- **Pick a backend** with `--ocr-backend`: `go` (pure Go, the reference; a
  few seconds per page) or `auto`.
- **Bring your own models** with a catalog file or URL
  (`--ocr-catalog`, or `"catalogs": [...]` in `providers.json`), following
  [`docs/schemas/ocr-catalog-v1.schema.json`](docs/schemas/ocr-catalog-v1.schema.json).
  Each model is a [manifest](docs/schemas/ocr-manifest-v1.schema.json) plus
  its files. `HF_TOKEN` is sent for user catalogs' Hugging Face sources. The
  built-in catalog lists permissively licensed models only; a model under any
  other licence needs `docstomd ocr install --accept-license <licence> <id>`.

| Model | Languages | Size | Licence |
|---|---|---|---|
| `pp-ocrv5-mobile` (default) | Chinese, English, Japanese | 27 MB | Apache-2.0 |
| `pp-ocrv5-server` | Chinese, English, Japanese | 171 MB | Apache-2.0 |

### Bring your own engine

Wrap any OCR engine or vendor API in a small program that speaks the
[OCR protocol](docs/ocr-protocol.md): JSON lines over stdin/stdout, the
whole PDF plus a page list in, Markdown or text lines with boxes out. Lines
are laid out with the same rules as native PDF pages. No docstomd PR, fork
or rebuild is needed.

1. Copy a template: [`examples/ocr-adapter/template-go`](examples/ocr-adapter/template-go) or
   [`examples/ocr-adapter/template-python`](examples/ocr-adapter/template-python) (standard
   library only), and replace `recognize` with your engine.
2. Try it: `docstomd convert --ocr auto --ocr-provider exec:./my-adapter scan.pdf`.
3. Register it by name in `$XDG_CONFIG_HOME/docstomd/providers.json`
   (`~/.config/docstomd/providers.json`):

   ```json
   {
     "version": 1,
     "default": "tesseract",
     "providers": {
       "tesseract": {"command": "docstomd-ocr-tesseract", "args": ["--lang", "eng+deu"], "local": true},
       "my-vendor": {"command": "/opt/ocr/vendor-adapter", "env": {"VENDOR_REGION": "eu"}, "page_cost": 0.002}
     }
   }
   ```

   `command` is looked up on `PATH`, or resolved against the config file's
   directory when it is a relative path. `env` adds to the inherited
   environment. `page_cost` (USD) feeds dry-run estimates. `local` marks an
   engine that runs on this machine. Then use `--ocr-provider my-vendor`.

A ready adapter for Tesseract lives in
[`examples/ocr-adapter/docstomd-ocr-tesseract`](examples/ocr-adapter/docstomd-ocr-tesseract) (needs
`tesseract` and poppler's `pdftoppm`):

```sh
go install github.com/m7medVision/docstomd-go/examples/ocr-adapter/docstomd-ocr-tesseract@latest
docstomd convert --ocr auto --ocr-provider exec:docstomd-ocr-tesseract scan.pdf
```

**Compatibility promise.** The protocol is versioned. Within a version only
optional fields are added, and both sides ignore fields they do not know. A
breaking change bumps the version, and docstomd keeps accepting the previous
version for at least one release cycle. The conformance checks that the
examples pass live in `examples/ocr-adapter/conformance_test.go`.

### Mistral setup

```sh
export MISTRAL_API_KEY=...        # read from the environment only; never logged
docstomd convert --ocr auto scan.pdf
```

A missing key produces an actionable `ocrAuth` error before any request is
sent. Rate limits (HTTP 429) and server errors are retried with backoff,
waiting at most 60 seconds per retry even when `Retry-After` asks for more.
An authentication failure fails fast. Each request times out after 120
seconds unless you pass your own `MistralOptions.HTTPClient`.

### Cost control

- **Dry run first.** `--ocr-dry-run` prints `ocr_cost` with the exact page
  list and estimated dollars, and needs no API key:

  ```sh
  docstomd convert --json --ocr auto --ocr-dry-run big-scan.pdf | jq .ocr_cost
  ```

- **Cap the bill.** `--ocr-max-pages N` bills at most `N` pages per document.
  Pages past the cap keep their native content, are listed in
  `needs_review`, and the cost report sets `truncated_by_caps`.
- **Prefer `auto` over `force`.** On text-dominant documents `auto` bills
  only the few scanned pages. `force` bills every page.
- **Estimate.** The Mistral provider estimates $0.004 per page, the published
  price of $4 per 1,000 pages at the time of writing. Check current pricing;
  the estimate is a planning aid, not an invoice.
- **Pin the model.** Output can change when `mistral-ocr-latest` moves. Pass a
  dated model id with `--ocr-model` (or `MistralOptions.Model`) when
  reproducibility matters.

## Library

```go
import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/m7medVision/docstomd-go"
)

f, err := os.Open("report.pdf")
if err != nil {
	return err
}
defer f.Close()

result, err := docstomd.Convert(context.Background(), f, docstomd.Options{
	OCR: docstomd.OCROptions{
		Mode:           docstomd.OCRAuto, // nil Provider means Mistral from MISTRAL_API_KEY
		MaxPagesPerDoc: 20,
		DryRun:         false,
	},
})
var derr *docstomd.Error
if errors.As(err, &derr) && derr.Code == docstomd.CodeNeedsOcr {
	// derr.Pages lists the scanned pages
}
if err != nil {
	return err
}
fmt.Println(result.Markdown, result.OCRCost, result.NeedsReview)
```

`docstomd.Detect(ctx, r, name)` classifies a document without converting it.
Set `Options.FileName` (and pass `name` to `Detect`) when you know the file
name. Its extension is consulted only when the content is inconclusive, so an
empty or truncated office package reports `malformed` instead of
`unsupported`. `Options.Format` skips detection entirely.
`Options.Markdown` tunes PDF Markdown reconstruction. Leave it nil for the
defaults. To change one flag, start from the defaults so the others stay on;
a zero `MarkdownOptions` turns every flag off:

```go
md := docstomd.DefaultMarkdownOptions()
md.IncludeLinks = true
result, err := docstomd.Convert(ctx, f, docstomd.Options{Markdown: &md})
```

OCR failures return a `*docstomd.Error` with an OCR code, and `errors.Is`
still matches `docstomd.ErrOCRMissingKey`, `ErrOCRUnauthorized` and
`ErrOCRRateLimited`.

`OCROptions.Provider` accepts any `docstomd.OCRProvider` implementation
(`Name`, `EstPageCost`, `Recognize`). The same interface is how you plug in
another vendor or a fake in tests. `Recognize` returns `OCRPageResult`s with
either `Markdown` or `Lines` (`OCRLine` text plus an `OCRRect` box, origin
top-left, in a `Width`×`Height` space). `NewExternalOCRProvider` drives an
engine speaking the OCR protocol and `NewLocalOCRProvider` the local engine;
both keep one engine process for their lifetime, so `Close` them when done.

`MaxPagesPerDoc` caps each document. `MaxPagesPerRun` caps a run, which is
one `Convert` call unless you share an `OCRRun` across calls. Calls carrying
the same `*OCRRun` draw on one page budget, including concurrent calls:

```go
run := &docstomd.OCRRun{}
for _, doc := range docs {
	result, err := docstomd.Convert(ctx, doc, docstomd.Options{
		OCR: docstomd.OCROptions{Mode: docstomd.OCRAuto, MaxPagesPerRun: 500, Run: run},
	})
	// once 500 pages are billed, later documents report truncated_by_caps
}
```

## Benchmarks

Benchmarks are local only, and numbers compare only on the same machine;
every baseline records a machine fingerprint. See [bench/README.md](bench/README.md)
for the protocols.

### PDF (`make bench-pdf`)

<!-- The rows below come from bench/pdf/baselines/*.json and bench/results/report.md. -->

Pinned public 200-PDF corpus, scored by the corpus's own evaluator (OCR off).
Scores are the committed baselines in `bench/pdf/baselines/`; higher is better.

| Engine | Overall | Reading order (NID) | Tables (TEDS) | Headings (MHS) | Missing |
|--------|---------|---------------------|---------------|----------------|---------|
| docstomd | 0.6679 | 0.8309 | 0.2489 | 0.3686 | 3 (scanned; need OCR) |
| markitdown 0.1.7 | 0.5885 | 0.8437 | 0.2729 | 0.0000 | 0 |
| pymupdf4llm 1.28.2 | 0.8687 | 0.9069 | 0.7896 | 0.7829 | 0 |

Speed: docstomd's committed baseline converts the full corpus in a median of
1.63 s, with one CLI process per document on a 12-CPU laptop (five
alternating passes after a warm-up). Speeds for the other engines depend on
the Python environment; run `make bench-pdf` for same-machine numbers.

### OCR (`make bench-ocr`)

Dry mode over the same 200-document corpus (zero provider calls, key removed
from the environment), 2026-09-14:

| Set | Documents | Pages | Routed pages | Routing | `auto` bill | `force` bill |
|-----|-----------|-------|--------------|---------|-------------|--------------|
| PDF corpus | 200 | 200 | 6 | 3.0% | 6 pages ≈ $0.024 | 200 pages ≈ $0.80 |
| Generated: 7 text pages + 1 scan | 1 | 8 | 1 | 12.5% | 1 page ≈ $0.004 | 8 pages ≈ $0.032 |
| Generated: fully scanned | 1 | 4 | 4 | 100% | 4 pages ≈ $0.016 | 4 pages ≈ $0.016 |

The live mode (`--live --page-budget N`) OCRs the routed documents in both
modes under a hard page budget, and gates the auto-vs-force quality delta
with the corpus evaluator.

**Full live run** (2026-09-15; `mistral-ocr-latest`; `--page-budget 14`):

- All 6 routed corpus documents were OCRed in both modes, and none were
  skipped.
- 12 pages were billed, an estimated $0.048.
- No page was flagged for review.
- The quality gate passed.

| Document | PDF class / routing reason | Overall score, `auto` | Overall score, `force` |
|----------|----------------------------|-----------------------|------------------------|
| 01030000000056 | text_based / suspected_garbled_text | 0.9014 | 0.9014 |
| 01030000000058 | text_based / suspected_garbled_text | 0.9488 | 0.9488 |
| 01030000000059 | text_based / suspected_garbled_text | 0.7539 | 0.7539 |
| 01030000000086 | scanned / no_text | 0.9898 | 0.9898 |
| 01030000000113 | scanned / no_text | 0.6883 | 0.6883 |
| 01030000000141 | image_based / vector_text | 0.9266 | 0.9266 |

The mean auto-minus-force delta is +0.0000. Every corpus document is one
page, so both modes OCR the same page: the live run shows no quality lost to
routing. The dry table above shows the cost gap between the modes.

### Formats (`make bench-formats`)

The corpus holds 35 documents: the vendored DOCX/XLSX/PPTX fixtures plus
generated documents at three sizes. Both engines convert it: docstomd, and a
baseline converter built locally from the path in the uncommitted
`bench.env`. Containment, targets and counts come from a fresh
`make bench-formats` run on 2026-09-15; all three format gates passed.

| Format | Documents | Trigram containment (ours in baseline / baseline in ours) | Target | Headings / table rows / list items / links / footnotes |
|--------|-----------|-----------------------------------------------------------|--------|--------------------------------------------------------|
| DOCX | 20 | 1.0000 / 1.0000 | ≥ 0.99 | 199 / 390 / 612 / 102 / 100 |
| XLSX | 5 | 1.0000 / 1.0000 | ≥ 0.99 | 8 / 1,881 / 0 / 0 / 0 |
| PPTX | 10 | 1.0000 / 1.0000 | ≥ 0.99 | 284 / 2 / 1,122 / 281 / 0 |

- **Containment:** every document the baseline converts comes out
  byte-identical from docstomd. The 2 DOCX documents neither engine converts
  are intentionally broken packages, and both engines report them as errors.
- **Counts:** the structural counts are identical for both engines.

Speed after the office-formats performance pass, from ticket 18's notes:
`make bench-formats`, median of 3 alternating runs with 7 passes each, on a
12-thread i5-12450HX. The machine was shared with other jobs, and before
and after runs were interleaved.

| Format | docstomd median ms/doc | docstomd MB/s | Baseline median ms/doc | Baseline MB/s |
|--------|------------------------|---------------|------------------------|---------------|
| DOCX | 3.19 | 0.98 | 1.60 | 1.57 |
| XLSX | 3.80 | 1.90 | 2.33 | 2.10 |
| PPTX | 3.29 | 8.94 | 1.67 | 13.50 |

- **Timings:** one CLI process per document, so on these small files they
  mostly measure process startup (about 3.9 ms for `docstomd --help`).
- **Conversion itself:** 0.05 to 1 ms per document in-process.
- **Throughput:** MB/s, which the larger documents weight, rose 16% (DOCX),
  42% (XLSX) and 23% (PPTX) in that pass.
- **Large documents, in-process parse and render:** about 2x faster than
  before the pass. A 40,000-row XLSX takes 320 ms, down from 650 ms.

## Development

```sh
make build test lint             # go build, go test, go vet + golangci-lint
make bench-test                  # bench harness unit tests: fakes and tiny inputs, no binaries, seconds
make bench-test-integration      # bench tests that build and run the real docstomd binary on fixtures
cp bench.env.example bench.env   # formats bench: path to the baseline converter checkout (git-ignored)
make bench-pdf                   # PDF bench and gates (clones the pinned corpus; needs uv)
make bench-formats               # formats bench and gates (needs bench.env and the baseline toolchain)
make bench-ocr                   # OCR routing and cost in dry mode (no API key needed)
```

`DESIGN.md` records the architecture and the project's binding conventions.

## License

MIT; see [LICENSE](LICENSE). Third-party notices are in [NOTICE.md](NOTICE.md).
