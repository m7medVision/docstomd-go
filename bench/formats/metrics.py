"""Formats bench metrics: structural counts, word-trigram containment, speed."""

from __future__ import annotations

import re
import statistics
from collections import Counter

HEADING = re.compile(r"^#{1,6}\s+\S")
TABLE_ROW = re.compile(r"^\s*\|.*\|\s*$")
TABLE_SEPARATOR = re.compile(r"^\s*\|(\s*:?-+:?\s*\|)+\s*$")
LIST_ITEM = re.compile(r"^\s*(?:[-*+]|\d+[.)])\s+\S")
LINK = re.compile(r"(?<!!)\[[^\]^][^\]]*\]\([^)\s]+(?:\s+\"[^\"]*\")?\)")
FOOTNOTE_DEFINITION = re.compile(r"^\[\^[^\]]+\]:")
FENCE = re.compile(r"^\s*(```|~~~)")
WORD = re.compile(r"\w+")

COUNT_KEYS = ("headings", "table_rows", "list_items", "links", "footnotes")


def structural_counts(markdown: str) -> dict[str, int]:
    counts = dict.fromkeys(COUNT_KEYS, 0)
    in_fence = False
    for line in markdown.splitlines():
        if FENCE.match(line):
            in_fence = not in_fence
            continue
        if in_fence:
            continue
        if HEADING.match(line):
            counts["headings"] += 1
        elif TABLE_ROW.match(line):
            if not TABLE_SEPARATOR.match(line):
                counts["table_rows"] += 1
        elif LIST_ITEM.match(line):
            counts["list_items"] += 1
        if FOOTNOTE_DEFINITION.match(line):
            counts["footnotes"] += 1
        counts["links"] += len(LINK.findall(line))
    return counts


def word_trigrams(markdown: str) -> dict[tuple[str, ...], int]:
    words = [word.lower() for word in WORD.findall(markdown)]
    if not words:
        return {}
    if len(words) < 3:
        return {tuple(words): 1}
    return dict(Counter(tuple(words[i : i + 3]) for i in range(len(words) - 2)))


def containment(ours: dict, theirs: dict) -> tuple[int, int]:
    """Multiset intersection size and the size of `ours`."""
    shared = sum(min(count, theirs.get(gram, 0)) for gram, count in ours.items())
    return shared, sum(ours.values())


def ratio(shared: int, total: int) -> float | None:
    return shared / total if total else None


def format_summary(documents: list[dict], engines: tuple[str, ...]) -> dict[str, dict]:
    subject = engines[0]
    baseline = engines[1] if len(engines) > 1 else None
    summaries: dict[str, dict] = {}
    for document in documents:
        summary = summaries.setdefault(document["format"], {
            "documents": 0,
            "bytes": 0,
            "status": {engine: {} for engine in engines},
            "counts": {engine: dict.fromkeys(COUNT_KEYS, 0) for engine in engines},
            "grams": {"ours_shared": 0, "ours_total": 0, "baseline_shared": 0, "baseline_total": 0},
            "per_document": {},
        })
        summary["documents"] += 1
        summary["bytes"] += document["bytes"]
        for engine in engines:
            status = document["status"][engine]
            summary["status"][engine][status] = summary["status"][engine].get(status, 0) + 1
            for key, value in document["counts"][engine].items():
                summary["counts"][engine][key] += value
        if baseline is None or document["status"][baseline] != "ok":
            continue
        grams = summary["grams"]
        ours, theirs = document["grams"][subject], document["grams"][baseline]
        ours_shared, ours_total = containment(ours, theirs)
        grams["ours_shared"] += ours_shared
        grams["ours_total"] += ours_total
        baseline_shared, baseline_total = containment(theirs, ours)
        grams["baseline_shared"] += baseline_shared
        grams["baseline_total"] += baseline_total
        summary["per_document"][document["id"]] = {
            "ours_in_baseline": ratio(ours_shared, ours_total),
            "baseline_in_ours": ratio(baseline_shared, baseline_total),
        }
    for summary in summaries.values():
        grams = summary.pop("grams")
        summary["ours_in_baseline"] = ratio(grams["ours_shared"], grams["ours_total"]) if baseline else None
        summary["baseline_in_ours"] = ratio(grams["baseline_shared"], grams["baseline_total"]) if baseline else None
    return summaries


def speed_summary(documents: list[dict], engines: tuple[str, ...]) -> dict[str, dict]:
    per_format: dict[str, dict[str, dict]] = {}
    for document in documents:
        engines_speed = per_format.setdefault(document["format"], {engine: {"ms": [], "bytes": 0, "seconds": 0.0} for engine in engines})
        for engine in engines:
            if document["status"][engine] != "ok":
                continue
            median = statistics.median(document["seconds"][engine])
            engines_speed[engine]["ms"].append(median * 1000)
            engines_speed[engine]["seconds"] += median
            engines_speed[engine]["bytes"] += document["bytes"]
    return {
        fmt: {
            engine: {
                "documents": len(data["ms"]),
                "median_ms": statistics.median(data["ms"]) if data["ms"] else None,
                "mb_per_s": data["bytes"] / 1e6 / data["seconds"] if data["seconds"] else None,
            }
            for engine, data in engines_speed.items()
        }
        for fmt, engines_speed in per_format.items()
    }
