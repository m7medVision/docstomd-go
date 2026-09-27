# OCR adapters

Programs that plug an OCR engine into docstomd through the
[OCR protocol](../../docs/ocr-protocol.md). None of them imports docstomd.

| Directory | What it is |
|---|---|
| `template-go/` | Minimal Go adapter (standard library only). Copy it and replace `recognize` |
| `template-python/` | Minimal Python adapter (standard library only). Copy it and replace `recognize` |
| `docstomd-ocr-tesseract/` | Tesseract adapter: renders pages with `pdftoppm`, runs `tesseract`, answers with lines |

`conformance_test.go` runs each of them through the protocol conformance
checks (`go test ./examples/...`). The Tesseract tests skip when `tesseract`
or `pdftoppm` is missing.
