package engine

import (
	"golang.org/x/text/unicode/bidi"
	"golang.org/x/text/unicode/norm"
)

// visualToLogical turns a line read left to right off the image (visual
// order) into logical order. Lines with no right-to-left letters are left
// alone. Otherwise the line is taken as a right-to-left paragraph: runs of
// Latin letters and numbers (with the separators and neutrals between them,
// such as "1,250", "50%" or "SAP R/3") keep their order, everything else is
// reversed with bracket mirroring, and the run order is reversed. For such
// lines the Unicode bidi reordering is its own inverse, so this undoes it.
func visualToLogical(s string) string {
	runes := []rune(s)
	classes := make([]bidi.Class, len(runes))
	rtl := false
	for i, r := range runes {
		p, _ := bidi.LookupRune(r)
		classes[i] = p.Class()
		if classes[i] == bidi.R || classes[i] == bidi.AL {
			rtl = true
		}
	}
	if !rtl {
		return s
	}
	ltr := make([]bool, len(runes))
	strong := make([]int8, len(runes)) // 1 LTR-ish, -1 RTL, 0 neutral
	for i, c := range classes {
		switch c {
		case bidi.L, bidi.EN, bidi.AN:
			strong[i] = 1
		case bidi.R, bidi.AL:
			strong[i] = -1
		}
	}
	// W5: European terminators (%, $, …) next to digits join the number.
	for i, c := range classes {
		if c != bidi.ET {
			continue
		}
		for j := i - 1; j >= 0 && (classes[j] == bidi.ET || classes[j] == bidi.EN); j-- {
			if classes[j] == bidi.EN {
				strong[i] = 1
				break
			}
		}
		for j := i + 1; j < len(runes) && strong[i] == 0 && (classes[j] == bidi.ET || classes[j] == bidi.EN); j++ {
			if classes[j] == bidi.EN {
				strong[i] = 1
			}
		}
	}
	for i := range runes {
		switch {
		case strong[i] != 0:
			ltr[i] = strong[i] > 0
		case classes[i] == bidi.NSM && i > 0:
			ltr[i] = ltr[i-1]
		default:
			// A neutral is LTR only between two LTR neighbours.
			left, right := int8(0), int8(0)
			for j := i - 1; j >= 0 && left == 0; j-- {
				left = strong[j]
			}
			for j := i + 1; j < len(runes) && right == 0; j++ {
				right = strong[j]
			}
			ltr[i] = left > 0 && right > 0
		}
	}
	out := make([]rune, 0, len(runes))
	for end := len(runes); end > 0; {
		start := end - 1
		for start > 0 && ltr[start-1] == ltr[end-1] {
			start--
		}
		run := string(runes[start:end])
		if !ltr[end-1] {
			run = bidi.ReverseString(run)
		}
		out = append(out, []rune(run)...)
		end = start
	}
	return string(out)
}

// normalize folds compatibility forms (Arabic presentation forms, full-width
// Latin) as docstomd does for native PDF text.
func normalize(s string) string {
	return norm.NFKC.String(s)
}
