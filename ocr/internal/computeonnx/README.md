# Vendored compute-onnx

A copy of [github.com/gomlx/compute-onnx](https://github.com/gomlx/compute-onnx)
(Apache License 2.0, see `LICENSE`) at upstream commit
`cf83f1d79b30f0ef471bda69576e15493dbbb3a1`: the GoMLX backend that runs graphs
on ONNX Runtime. Import paths are rewritten; tests, commands, the WebAssembly
engine and the generated protobuf package (still imported from upstream) are
left out.

Patches, marked `docstomd patch`:

| Patch | Why |
|---|---|
| `internal/ort` rewritten on [purego](https://github.com/ebitengine/purego) | No cgo: ONNX Runtime is loaded with `dlopen` at run time and its `OrtApi` table called by offset. CPU only; GPU entry points return errors. Tensor data lives in libc `malloc` memory, and every pointer argument is converted inline in a `//go:uintptrescapes` call so stack variables cannot move under ONNX Runtime. Outputs are bit-identical to the cgo binding at the same speed (ticket 11). |
| Input-dilated convolution emitted as `ConvTranspose` | ONNX Runtime has no input dilation on `Conv`; the fallback zero-insertion is kept for other shapes |
| `Clamp` as `Min(Max(x, lo), hi)` | docTR detectors (ONNX `Clip`) |
| Auto-install off | compute-onnx downloads ONNX Runtime on first use by default; docstomd never downloads behind the user's back (`docstomd ocr install --backend onnx` does it explicitly) |

The patches and a purego mode will be offered upstream; once upstream has
them, this copy is replaced by a pinned upstream version.
