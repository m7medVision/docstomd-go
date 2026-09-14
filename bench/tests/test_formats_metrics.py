import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "formats"))

from metrics import (  # noqa: E402
    containment,
    format_summary,
    speed_summary,
    structural_counts,
    word_trigrams,
)

SAMPLE = """# Title

Intro with a [link](https://example.com) and an image ![alt](x.png).

## Section

- first
- second
  * nested
1. one
2) two

| a | b |
| --- | :-: |
| 1 | 2 |
| 3 | 4 |

Text with a note[^1].

[^1]: The note body.

```
# not a heading
- not a list
| not | a table |
```
"""


class TestStructuralCounts:
    def test_counts_markdown_structures_outside_code(self):
        counts = structural_counts(SAMPLE)
        assert counts == {"headings": 2, "table_rows": 3, "list_items": 5, "links": 1, "footnotes": 1}

    def test_empty_markdown_counts_zero(self):
        assert set(structural_counts("").values()) == {0}


class TestTrigrams:
    def test_trigrams_are_lowercased_word_windows(self):
        grams = word_trigrams("The quick, brown FOX jumps")
        assert grams == {("the", "quick", "brown"): 1, ("quick", "brown", "fox"): 1, ("brown", "fox", "jumps"): 1}

    def test_markdown_punctuation_does_not_split_words_differently(self):
        assert word_trigrams("| **Bold** text | here |") == word_trigrams("Bold text here")

    def test_short_text_is_one_gram(self):
        assert word_trigrams("two words") == {("two", "words"): 1}
        assert word_trigrams("") == {}


class TestContainment:
    def test_identical_is_full_both_ways(self):
        grams = word_trigrams("alpha beta gamma delta")
        assert containment(grams, grams) == (2, 2)

    def test_counts_are_multiset_intersections(self):
        ours = word_trigrams("a b c a b c")
        theirs = word_trigrams("a b c")
        assert containment(ours, theirs) == (1, 4)

    def test_directional_ratios_in_summary(self):
        docs = [
            {"id": "d.docx", "format": "docx", "status": {"docstomd": "ok", "baseline": "ok"},
             "grams": {"docstomd": word_trigrams("one two three four"), "baseline": word_trigrams("one two three five six")},
             "counts": {"docstomd": structural_counts("# h"), "baseline": structural_counts("# h\n# i")},
             "bytes": 1000},
        ]
        summary = format_summary(docs, engines=("docstomd", "baseline"))["docx"]
        assert summary["ours_in_baseline"] == pytest.approx(1 / 2)
        assert summary["baseline_in_ours"] == pytest.approx(1 / 3)
        assert summary["counts"]["docstomd"]["headings"] == 1
        assert summary["counts"]["baseline"]["headings"] == 2
        assert summary["documents"] == 1
        assert summary["per_document"] == {"d.docx": {"ours_in_baseline": pytest.approx(1 / 2), "baseline_in_ours": pytest.approx(1 / 3)}}

    def test_small_document_loss_is_visible_per_document(self):
        big = " ".join(f"w{i}" for i in range(1000))
        docs = [
            {"id": "big.pptx", "format": "pptx", "status": {"docstomd": "ok", "baseline": "ok"},
             "grams": {"docstomd": word_trigrams(big), "baseline": word_trigrams(big)},
             "counts": {"docstomd": structural_counts(""), "baseline": structural_counts("")}, "bytes": 1},
            {"id": "math.pptx", "format": "pptx", "status": {"docstomd": "ok", "baseline": "ok"},
             "grams": {"docstomd": word_trigrams("solve with"), "baseline": word_trigrams("solve with x equals the formula")},
             "counts": {"docstomd": structural_counts(""), "baseline": structural_counts("")}, "bytes": 1},
        ]
        summary = format_summary(docs, engines=("docstomd", "baseline"))["pptx"]
        assert summary["baseline_in_ours"] > 0.99
        assert summary["per_document"]["math.pptx"]["baseline_in_ours"] == 0.0

    def test_unsupported_documents_are_counted_and_contribute_no_ours_grams(self):
        docs = [
            {"id": "s.xlsx", "format": "xlsx", "status": {"docstomd": "unsupported", "baseline": "ok"},
             "grams": {"docstomd": {}, "baseline": word_trigrams("sheet one cell values")},
             "counts": {"docstomd": structural_counts(""), "baseline": structural_counts("| a |")},
             "bytes": 10},
        ]
        summary = format_summary(docs, engines=("docstomd", "baseline"))["xlsx"]
        assert summary["status"]["docstomd"] == {"unsupported": 1}
        assert summary["baseline_in_ours"] == 0.0
        assert summary["ours_in_baseline"] is None
        assert summary["per_document"] == {"s.xlsx": {"ours_in_baseline": None, "baseline_in_ours": 0.0}}

    def test_no_baseline_engine_leaves_containment_unset(self):
        docs = [
            {"id": "p.pptx", "format": "pptx", "status": {"docstomd": "ok"}, "grams": {"docstomd": word_trigrams("a b c")},
             "counts": {"docstomd": structural_counts("")}, "bytes": 5},
        ]
        summary = format_summary(docs, engines=("docstomd",))["pptx"]
        assert summary["ours_in_baseline"] is None and summary["baseline_in_ours"] is None
        assert summary["per_document"] == {}


class TestSpeedSummary:
    def test_median_ms_and_throughput_per_format(self):
        docs = [
            {"format": "docx", "bytes": 1_000_000, "seconds": {"docstomd": [0.2, 0.1, 0.3]}, "status": {"docstomd": "ok"}},
            {"format": "docx", "bytes": 3_000_000, "seconds": {"docstomd": [0.5, 0.5, 0.5]}, "status": {"docstomd": "ok"}},
        ]
        speed = speed_summary(docs, engines=("docstomd",))["docx"]["docstomd"]
        assert speed["median_ms"] == pytest.approx(350.0)
        assert speed["mb_per_s"] == pytest.approx(4.0 / 0.7)
        assert speed["documents"] == 2

    def test_failed_documents_are_excluded_from_speed(self):
        docs = [
            {"format": "xlsx", "bytes": 100, "seconds": {"docstomd": [0.01]}, "status": {"docstomd": "unsupported"}},
        ]
        speed = speed_summary(docs, engines=("docstomd",))["xlsx"]["docstomd"]
        assert speed == {"documents": 0, "median_ms": None, "mb_per_s": None}
