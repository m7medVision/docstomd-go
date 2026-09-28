# docstomd-ocr-local

docstomd's local OCR engine: CPU-only, pure Go (no cgo), a separate module so
the core `docstomd` module keeps zero heavy dependencies and Go 1.26.

It speaks the [docstomd OCR protocol](../docs/ocr-protocol.md), and docstomd
reaches it like any third-party engine (`--ocr-provider local`).

```sh
go install github.com/m7medVision/docstomd-go/ocr/cmd/docstomd-ocr-local@latest
docstomd ocr install pp-ocrv5-mobile
docstomd convert --ocr auto --ocr-provider local scan.pdf
```

| Package | What it does |
|---|---|
| `cmd/docstomd-ocr-local` | The engine binary: protocol session, model selection |
| `pageimage` | Extracts a scanned page's image in pure Go (JPEG, Flate) with its placement, or renders it with `pdftoppm` (CCITT, JBIG2, vector text); reusable by adapter authors |
| `engine` | Manifest-driven pipeline: detect → orient → recognize (DB post-processing, CTC decoding), denormal weights flushed at load |
| `backend` | GoMLX backend selection: `go` (pure Go), `onnx` (ONNX Runtime via purego), `auto` |
| `catalog` | Model and runtime catalog, verified installs, model selection |

Models are described by a [manifest](../docs/schemas/ocr-manifest-v1.schema.json)
next to their ONNX files.

## Tests

`go test ./...` runs offline. Tests that need real models skip unless
`DOCSTOMD_OCR_TEST_MODELS` names a models root with the model installed.
`DOCSTOMD_OCR_TEST_BACKEND=onnx` (with `DOCSTOMD_OCR_RUNTIMES` pointing at an
installed runtime) runs the same goldens on ONNX Runtime.

Vendored upstream code, with docstomd patches: `internal/onnxgomlx`
(onnx-gomlx) and `internal/computeonnx` (compute-onnx); see their READMEs.
