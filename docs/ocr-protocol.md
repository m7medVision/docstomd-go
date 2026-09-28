# docstomd OCR protocol, version 1

Any program that speaks this protocol is a docstomd OCR provider. You can wrap
any OCR engine or vendor API without changing docstomd:

```sh
docstomd convert --ocr auto --ocr-provider exec:/path/to/my-engine scan.pdf
```

docstomd's own local engine, `docstomd-ocr-local`, uses the same protocol and
gets no special treatment.

Machine-readable schema: [`schemas/ocr-protocol-v1.schema.json`](schemas/ocr-protocol-v1.schema.json).

## Transport

- docstomd starts the program and talks to it over its **stdin** and
  **stdout**, one JSON object per line (UTF-8, `\n`-terminated, no embedded
  newlines, which `json.dumps`/`json.Marshal` never emit).
- **stderr** is free-form logging. docstomd keeps the last 4 KiB and shows it
  when the engine fails.
- The program inherits docstomd's environment plus any `env` from its
  provider config.
- A message is at most 256 MiB.

## Session

```
engine → docstomd   {"type":"hello", ...}            once, first thing
docstomd → engine   {"type":"recognize", "id":"1", ...}
engine → docstomd   {"type":"result", "id":"1", ...}  or {"type":"error", ...}
docstomd → engine   {"type":"recognize", "id":"2", ...}
...
docstomd closes stdin                                  engine exits
```

- The engine speaks first: a `hello` handshake, then it reads requests.
- Requests are sent one at a time; each gets exactly one reply with the same
  `id` before the next request is sent.
- One engine process serves every document of a docstomd run. Keep loaded
  models between requests; that is the point of the session.
- **Single-shot programs are valid too**: answer one request and exit.
  docstomd starts the program again for the next request.
- When stdin closes, exit promptly. docstomd kills the engine 5 seconds after
  closing stdin, and at once if the user cancels.

## Messages

### `hello` (engine → docstomd)

```json
{"type": "hello", "protocol": 1, "engine": "my-engine", "version": "0.3.0", "page_cost": 0, "local": true}
```

| Field | Required | Meaning |
|---|---|---|
| `type` | yes | `"hello"` |
| `protocol` | yes | Protocol version the engine speaks: `1` |
| `engine` | yes | Engine name, shown in cost reports and errors |
| `version` | no | Engine version, informational |
| `page_cost` | no | Estimated USD per page, for cost reports; default 0 |
| `local` | no | `true` when the engine runs on this machine (no network, no bill) |

### `recognize` (docstomd → engine)

```json
{"type": "recognize", "id": "1", "pdf": "JVBERi0xLjcK...", "pages": [2, 5]}
```

| Field | Meaning |
|---|---|
| `id` | Request id; echo it in the reply |
| `pdf` | The whole PDF file, standard base64 |
| `pages` | 1-indexed pages to recognize, ascending, no duplicates |
| `password` | Optional; the PDF's password when it has one |

The engine renders or extracts page images itself. Answer only the listed
pages; other pages are docstomd's business.

### `result` (engine → docstomd)

```json
{"type": "result", "id": "1", "pages": [
  {"page": 2, "markdown": "# Title\n\nBody text.", "confidence": 0.93},
  {"page": 5, "width": 1275, "height": 1650, "lines": [
    {"text": "Invoice 2024-117", "box": {"x0": 150, "y0": 142, "x1": 610, "y1": 188}, "confidence": 0.98}
  ]}
]}
```

Each page answers with **either**:

- `markdown`: finished Markdown for the page, used as is; or
- `lines`: recognized text lines. docstomd lays them out with the same rules
  it uses for native PDF pages (reading order, paragraphs, headings from
  relative line height, bullet and numbered lists). Tables, bold/italic and
  code blocks are not recovered from lines.

If both are present, `markdown` wins.

Page fields:

| Field | Meaning |
|---|---|
| `page` | A page from the request |
| `markdown` | Page Markdown |
| `lines` | Array of `{text, box, confidence?}` |
| `width`, `height` | Size of the coordinate space the boxes use, e.g. the page image in pixels |
| `confidence` | Optional page confidence in [0, 1]. For lines it defaults to the mean line confidence |

**Box coordinates.** Origin at the **top-left** corner of the page, `x` to
the right, `y` **down**. `x0,y0` is the top-left corner of the line and
`x1,y1` its bottom-right corner, in a `width × height` space that covers the
whole visible page (its CropBox). docstomd scales that space onto the page. When
`width`/`height` are omitted, boxes are in PDF points from the top-left of
the visible page. Order of `lines` does not matter; docstomd sorts them.
Lines should be in **logical** (reading) order internally: for Arabic or
Hebrew, `text` is the string as typed, not as displayed.

A page may be left out of `pages`. docstomd then keeps that page's native
text and flags it `needs_review`, as it does for results with confidence
below 0.5, empty text or garbled text.

### `error` (engine → docstomd)

```json
{"type": "error", "id": "1", "code": "unavailable", "message": "model pp-ocrv5-mobile is not installed; run: docstomd ocr install pp-ocrv5-mobile"}
```

| `code` | Meaning | docstomd error |
|---|---|---|
| `unavailable` | The engine cannot run here: missing model, runtime or licence | `ocrUnavailable`, CLI exit 5 |
| `failed` | Anything else | `ocrProvider`, CLI exit 4 |

Put an actionable hint in `message`.

## What docstomd does on failure

| Situation | Error code | CLI exit |
|---|---|---|
| Program not found or not executable | `ocrUnavailable` | 5 |
| Engine replied `error` with `unavailable` | `ocrUnavailable` | 5 |
| No `hello`, wrong first message, unsupported `protocol` | `ocrProtocol` | 5 |
| Malformed JSON, wrong `id`, unknown `type`, unrequested or duplicate page, page with neither `markdown` nor `lines` | `ocrProtocol` | 5 |
| Engine exited before replying | `ocrProtocol` | 5 |
| Engine replied `error` with `failed` | `ocrProvider` | 4 |

Every message names the provider. Scanned PDFs with `--ocr off` still exit 3
(`needsOcr`).

## Versioning

- `protocol` is an integer. Within a version, docstomd may add **optional**
  fields to its messages and engines may add optional fields to theirs; both
  sides ignore fields they do not know.
- Anything that would break an engine or docstomd bumps the version.
- docstomd accepts the current and the previous protocol version for at least
  one release cycle after a bump, so adapters keep working across upgrades.
- The model manifest and catalog formats of `docstomd-ocr-local` are
  versioned separately from this protocol.

## Writing an adapter

A complete adapter in Python:

```python
import base64, json, sys

def send(msg):
    sys.stdout.write(json.dumps(msg) + "\n")
    sys.stdout.flush()

send({"type": "hello", "protocol": 1, "engine": "my-engine", "local": True})
for line in sys.stdin:
    req = json.loads(line)
    pdf = base64.b64decode(req["pdf"])
    pages = [{"page": p, "markdown": my_ocr(pdf, p)} for p in req["pages"]]
    send({"type": "result", "id": req["id"], "pages": pages})
```

Working examples in Go and Python, a Tesseract adapter and conformance tests
live in [`examples/ocr-adapter/`](../examples/ocr-adapter/).
