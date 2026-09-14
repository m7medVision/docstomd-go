"""Generated routing fixtures: deterministic PDFs with known scanned pages."""

from __future__ import annotations

import zlib
from pathlib import Path

PAGE_WIDTH = 612
PAGE_HEIGHT = 792
SCAN_WIDTH = 850
SCAN_HEIGHT = 1100

TEXT_LINES = [
    "Quarterly operations summary for the northern distribution region",
    "Inbound freight volumes rose steadily across every monitored terminal",
    "Warehouse utilisation stayed below the planned capacity threshold",
    "Carrier on-time performance improved after the schedule revision",
    "Maintenance backlog was cleared ahead of the seasonal peak period",
    "Staffing levels matched forecast demand for the full reporting window",
]


def _text_content(page_number: int) -> bytes:
    ops = ["BT", "/F1 11 Tf", "72 720 Td", "14 TL"]
    for index, line in enumerate(TEXT_LINES):
        ops.append(f"({line} - page {page_number} line {index + 1}) Tj T*")
    ops.append("ET")
    return "\n".join(ops).encode("ascii")


def _scan_content() -> bytes:
    return f"q {PAGE_WIDTH} 0 0 {PAGE_HEIGHT} 0 0 cm /Im1 Do Q".encode("ascii")


def build_pdf(kinds: list[str]) -> bytes:
    """Build a PDF whose pages are "text" (native text layer) or "scan" (a
    full-page image with no text)."""
    scan = zlib.compress(b"\xff" * (SCAN_WIDTH * SCAN_HEIGHT), 9)
    objects: dict[int, bytes] = {
        1: b"<< /Type /Catalog /Pages 2 0 R >>",
        3: b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
        4: (
            f"<< /Type /XObject /Subtype /Image /Width {SCAN_WIDTH} /Height {SCAN_HEIGHT} /ColorSpace /DeviceGray "
            f"/BitsPerComponent 8 /Filter /FlateDecode /Length {len(scan)} >>\nstream\n"
        ).encode("ascii") + scan + b"\nendstream",
    }
    kids = []
    next_id = 5
    for page_number, kind in enumerate(kinds, start=1):
        page_id, content_id = next_id, next_id + 1
        next_id += 2
        content = _scan_content() if kind == "scan" else _text_content(page_number)
        resources = "<< /XObject << /Im1 4 0 R >> >>" if kind == "scan" else "<< /Font << /F1 3 0 R >> >>"
        objects[page_id] = (
            f"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 {PAGE_WIDTH} {PAGE_HEIGHT}] "
            f"/Resources {resources} /Contents {content_id} 0 R >>"
        ).encode("ascii")
        objects[content_id] = f"<< /Length {len(content)} >>\nstream\n".encode("ascii") + content + b"\nendstream"
        kids.append(f"{page_id} 0 R")
    objects[2] = f"<< /Type /Pages /Kids [{' '.join(kids)}] /Count {len(kinds)} >>".encode("ascii")

    out = bytearray(b"%PDF-1.4\n")
    offsets = {}
    for object_id in sorted(objects):
        offsets[object_id] = len(out)
        out += f"{object_id} 0 obj\n".encode("ascii") + objects[object_id] + b"\nendobj\n"
    xref = len(out)
    size = max(objects) + 1
    out += f"xref\n0 {size}\n0000000000 65535 f \n".encode("ascii")
    for object_id in range(1, size):
        out += f"{offsets[object_id]:010d} 00000 n \n".encode("ascii")
    out += f"trailer\n<< /Size {size} /Root 1 0 R >>\nstartxref\n{xref}\n%%EOF\n".encode("ascii")
    return bytes(out)


ROUTING_FIXTURES = {
    "text_dominant_mixed": {"kinds": ["text"] * 7 + ["scan"], "max_routed_fraction": 0.25},
    "fully_scanned": {"kinds": ["scan"] * 4, "min_routed_fraction": 0.9},
}


def write_routing_fixtures(directory: Path) -> dict[str, Path]:
    directory.mkdir(parents=True, exist_ok=True)
    paths = {}
    for name, spec in ROUTING_FIXTURES.items():
        path = directory / f"{name}.pdf"
        path.write_bytes(build_pdf(spec["kinds"]))
        paths[name] = path
    return paths
