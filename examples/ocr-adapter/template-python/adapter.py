#!/usr/bin/env python3
"""Minimal docstomd OCR adapter in Python (standard library only).

Copy it, replace ``recognize`` with a call to your engine or vendor API, and run

    docstomd convert --ocr auto --ocr-provider exec:./adapter.py scan.pdf

The protocol is documented in docs/ocr-protocol.md.
"""

import base64
import json
import sys


def send(msg):
    sys.stdout.write(json.dumps(msg) + "\n")
    sys.stdout.flush()


def recognize(pdf: bytes, page: int) -> dict:
    """Answer one page with {"markdown": ...} or {"lines": [...]}.

    This placeholder returns Markdown. Line boxes use a top-left origin with
    y down, in a width x height space covering the page.
    """
    return {"page": page, "markdown": f"Page {page} of a {len(pdf)}-byte PDF", "confidence": 0.99}


def main():
    send({"type": "hello", "protocol": 1, "engine": "example-python", "version": "1.0.0", "local": True})
    for line in sys.stdin:
        if not line.strip():
            continue
        req = json.loads(line)
        try:
            pdf = base64.b64decode(req["pdf"])
            pages = [recognize(pdf, p) for p in req["pages"]]
        except Exception as exc:  # report, keep the session alive
            send({"type": "error", "id": req.get("id", ""), "code": "failed", "message": str(exc)})
            continue
        send({"type": "result", "id": req["id"], "pages": pages})


if __name__ == "__main__":
    main()
