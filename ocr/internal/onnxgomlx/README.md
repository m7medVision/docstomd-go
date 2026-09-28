# Vendored onnx-gomlx

A copy of [github.com/gomlx/onnx-gomlx](https://github.com/gomlx/onnx-gomlx)
(Apache License 2.0, see `LICENSE`) at upstream commit
`1d439e72d50d76df42f61cf64ea11128c4b54b13` (2026-09-13), with import paths
rewritten and tests, benchmarks and notebooks left out.

It is vendored, not required through a `replace` directive, so that
`go install github.com/m7medVision/docstomd-go/ocr/cmd/docstomd-ocr-local@latest`
works. Our patches are marked `docstomd patch` in `internal/onnxgomlx/ops.go`
and will be offered upstream; once merged, this copy is replaced by a
pinned upstream version.

| Patch | Why |
|---|---|
| `ConvTranspose` (input-dilated convolution with a flipped kernel) | PP-OCRv5 detectors (DB head) |
| `Conv`/`MaxPool` `auto_pad` `SAME_UPPER`/`SAME_LOWER` | PaddlePaddle exports |
| Opset 1–9 `Slice` (starts/ends/axes attributes) | PP-LCNet text-line orientation classifier (opset 7) |
| `BatchNormalization` as `x*k + (bias - mean*k)` | Constants fold once; faster on every backend |
| Integer-scale nearest `Resize` as reshape + broadcast | Faster on every backend |
| Linear `Resize` of NCHW spatial axes as two constant-weight matrix products | docTR detectors; no `Gather`, which ONNX Runtime through compute-onnx cannot express |
| `MaxPool` with explicit unit `dilations` | docTR CRNN |
| Constant sub-expressions evaluated on the pure-Go backend, returned as host tensors | docTR PARSeq shape computations; avoids a runtime session per constant |
