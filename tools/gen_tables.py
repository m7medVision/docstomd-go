#!/usr/bin/env python3
"""Generate Go data tables from the reference Rust sources.

Usage: DOCSTOMD_TABLES_SRC=/path/to/rust/src python3 tools/gen_tables.py
Writes internal/pdf/extract/tables_base14.go, tables_glyphs.go and
tables_stdenc.go under the given source tree. The source directory comes from
the DOCSTOMD_TABLES_SRC environment variable (required).
"""
import os
import re
import sys
from pathlib import Path

_src = os.environ.get("DOCSTOMD_TABLES_SRC")
if not _src:
    raise SystemExit("DOCSTOMD_TABLES_SRC must point at the Rust source tree")
SRC = Path(_src)
OUT = Path("internal/pdf/extract")


def rust_char_to_go(lit):
    if lit.startswith("\\u{"):
        cp = int(lit[3:-1], 16)
        return f"0x{cp:04X}"
    if lit.startswith("\\u"):
        cp = int(lit[2:6], 16)
        return f"0x{cp:04X}"
    if lit.startswith("\\") and len(lit) == 2:
        return {
            "\\": "0x5C",
            "'": "0x27",
            '"': "0x22",
            "n": "0x0A",
            "r": "0x0D",
            "t": "0x09",
            "0": "0x00",
        }.get(lit[1], None) or f"0x{ord(lit[1]):04X}"
    return f"0x{ord(lit):04X}"


def gen_base14():
    text = (SRC / "extractor" / "base14.rs").read_text()
    out = ["// Code generated from the reference base-14 metrics. DO NOT EDIT.", "", "package extract", ""]
    aliases = {}
    for m in re.finditer(r"static (\w+): &\[\(char, u16\)\] = (\w+);", text):
        aliases[m.group(1)] = m.group(2)
    for m in re.finditer(r"static (\w+): &\[\(char, u16\)\] = &\[(.*?)\];", text, re.S):
        name, body = m.group(1), m.group(2)
        pairs = re.findall(r"'((?:\\.|[^'])*)',\s*(\d+)", body)
        if not pairs:
            continue
        entries = ", ".join(f"{{rune({rust_char_to_go(c)}), {w}}}" for c, w in pairs)
        out.append(f"var base14{name} = []charWidth{{{entries}}}")
        out.append("")
    for m in re.finditer(r"static (\w+): &\[\(u8, char\)\] = &\[(.*?)\];", text, re.S):
        name, body = m.group(1), m.group(2)
        pairs = re.findall(r"0x([0-9A-Fa-f]{2}),\s*'((?:\\.|[^'])*)'\)", body)
        if not pairs:
            continue
        entries = ", ".join(f"{{0x{b}, rune({rust_char_to_go(c)})}}" for b, c in pairs)
        goname = "symbolEncodingTable" if "SYMBOL" in name else "zapfEncodingTable"
        out.append(f"var {goname} = []codeChar{{{entries}}}")
        out.append("")
    for alias, target in aliases.items():
        out.append(f"var base14{alias} = base14{target}")
        out.append("")
    (OUT / "tables_base14.go").write_text("\n".join(out))


def gen_glyphs():
    text = (SRC / "glyph_names.rs").read_text()
    pairs = re.findall(
        r'm\.insert\("([^"]+)",\s*\'((?:\\u\{[0-9A-Fa-f]+\}|\\.|[^\\\']))\'\);', text
    )
    seen = {}
    for name, ch in pairs:
        seen[name] = ch
    out = ["// Code generated from the reference glyph-name table. DO NOT EDIT.", "", "package extract", ""]
    out.append("var glyphToUnicode = map[string]rune{")
    for name in sorted(seen):
        out.append(f"\t{name!r}: rune({rust_char_to_go(seen[name])}),".replace("'", '"'))
    out.append("}")
    (OUT / "tables_glyphs.go").write_text("\n".join(out) + "\n")


if __name__ == "__main__":
    OUT.mkdir(parents=True, exist_ok=True)
    gen_base14()
    gen_glyphs()
    print("generated", list(OUT.glob("tables_*.go")))
