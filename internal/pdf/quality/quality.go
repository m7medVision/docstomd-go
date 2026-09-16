// Package quality detects garbled extracted text: replacement runs, private
// use, C1 mojibake, dollar-as-space, non-alphanumeric dominance, and
// substitution-cipher statistics.
package quality

import (
	"math"
	"slices"
	"strings"
	"unicode"

	pdfdetect "github.com/m7medVision/docstomd-go/internal/pdf/detect"
	"github.com/m7medVision/docstomd-go/internal/pdf/extract"
)

var ReasonSuspectedGarble = pdfdetect.ReasonSuspectedGarble

type Report struct {
	PagesNeedingOCR []int
}

// Analyze is the single-string convenience over AnalyzeItems.
func Analyze(text string) bool {
	report := AnalyzeItems([]extract.TextItem{{Page: 1, Text: text, ItemType: extract.ItemText}})
	return len(report.PagesNeedingOCR) > 0
}

type evidence struct {
	chars                 int
	replacementChars      int
	replacementSpans      int
	longestReplacementRun int
	cipher                cipherStats
}

// AnalyzeItems aggregates per-page evidence from text items and reports the
// pages whose extracted output is broken.
func AnalyzeItems(items []extract.TextItem) Report {
	byPage := map[int]*evidence{}
	reasons := map[int][]string{}
	for _, item := range items {
		if item.ItemType != extract.ItemText {
			continue
		}
		ev := byPage[item.Page]
		if ev == nil {
			ev = &evidence{}
			byPage[item.Page] = ev
		}
		for _, ch := range item.Text {
			if !unicode.IsSpace(ch) {
				ev.chars++
			}
		}
		ev.cipher.addText(item.Text)
		switch spanIssueKind(item.Text) {
		case spanStrong:
			reasons[item.Page] = AppendReason(reasons[item.Page], ReasonSuspectedGarble)
		case spanReplacement:
			count, longest := replacementStats(item.Text)
			ev.replacementChars += count
			ev.replacementSpans++
			if longest > ev.longestReplacementRun {
				ev.longestReplacementRun = longest
			}
		}
	}
	for page, ev := range byPage {
		if _, flagged := reasons[page]; flagged {
			continue
		}
		if pageReplacementNeedsOCR(ev) || ev.cipher.looksGarbled() {
			reasons[page] = AppendReason(reasons[page], ReasonSuspectedGarble)
		}
	}
	pages := make([]int, 0, len(reasons))
	for p := range reasons {
		pages = append(pages, p)
	}
	slices.Sort(pages)
	return Report{PagesNeedingOCR: pages}
}

// AppendReason adds reason to list unless already present.
func AppendReason(list []string, reason string) []string {
	for _, r := range list {
		if r == reason {
			return list
		}
	}
	return append(list, reason)
}

type spanKind int

const (
	spanNone spanKind = iota
	spanReplacement
	spanStrong
)

func spanIssueKind(text string) spanKind {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return spanNone
	}
	if hasDollarAsSpace(trimmed) || hasPrivateUseRun(trimmed) || isCIDGarbage(trimmed) || hasCIDControlToken(trimmed) {
		return spanStrong
	}
	if hasReplacementRun(trimmed) {
		return spanReplacement
	}
	return spanNone
}

func replacementStats(text string) (count, longest int) {
	run := 0
	for _, ch := range text {
		if ch == 0xFFFD {
			count++
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}
	return count, longest
}

func hasReplacementRun(text string) bool {
	count, longest := replacementStats(text)
	return longest >= 2 || count >= 3
}

func hasPrivateUseRun(text string) bool {
	total, private, run, longest := 0, 0, 0, 0
	for _, ch := range text {
		if unicode.IsSpace(ch) {
			run = 0
			continue
		}
		total++
		if isPrivateUse(ch) {
			private++
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}
	if private == 0 {
		return false
	}
	return longest >= 3 || (total >= 5 && private >= 2 && private*2 >= total)
}

func isPrivateUse(ch rune) bool {
	return ch >= 0xE000 && ch <= 0xF8FF ||
		ch >= 0xF0000 && ch <= 0xFFFFD ||
		ch >= 0x100000 && ch <= 0x10FFFD
}

func hasCIDControlToken(text string) bool {
	for _, token := range strings.Fields(text) {
		total, c1 := 0, 0
		for _, ch := range token {
			total++
			if ch >= 0x80 && ch <= 0x9F {
				c1++
			}
		}
		if total >= 5 && c1 >= 2 && c1*20 >= total {
			return true
		}
	}
	return false
}

func hasDollarAsSpace(text string) bool {
	totalDollars := 0
	for i := 0; i < len(text); i++ {
		if text[i] == '$' {
			totalDollars++
		}
	}
	if totalDollars <= 10 {
		return false
	}
	letterDollar := 0
	for i := 1; i+1 < len(text); i++ {
		if text[i] == '$' && isASCIILetter(text[i-1]) && isASCIILetter(text[i+1]) {
			letterDollar++
		}
	}
	return letterDollar > 20 || letterDollar*2 > totalDollars
}

func isASCIILetter(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isCIDGarbage(text string) bool {
	if isGarbageText(text) {
		return true
	}
	total, c1, highLatin := 0, 0, 0
	asciiLetters := 0
	for _, ch := range text {
		if unicode.IsSpace(ch) {
			continue
		}
		if ch == '·' {
			continue
		}
		total++
		if ch >= 0x80 && ch <= 0x9F {
			c1++
		}
		if ch >= 0xA0 && ch <= 0xFF {
			highLatin++
		}
		if isASCIILetter(byte(ch)) && ch < 128 {
			asciiLetters++
		}
	}
	if total < 5 {
		return false
	}
	if c1 >= 2 && c1*20 >= total {
		return true
	}
	return total >= 20 && highLatin*5 >= total*2 && asciiLetters*3 < total
}

func isGarbageText(text string) bool {
	chars := []rune(text)
	alnum, nonAlnum := 0, 0
	i := 0
	for i < len(chars) {
		ch := chars[i]
		runEnd := i + 1
		for runEnd < len(chars) && chars[runEnd] == ch {
			runEnd++
		}
		leaderChar := ch == '.' || ch == '_' || ch == '·'
		decorativeLeader := leaderChar && runEnd-i >= 3
		if leaderChar && runEnd == i+1 {
			// Typeset leaders often alternate the glyph with single spaces
			// (". . . ."); they are as decorative as a solid run.
			spacedEnd := i + 1
			for spacedEnd+1 < len(chars) && chars[spacedEnd] == ' ' && chars[spacedEnd+1] == ch {
				spacedEnd += 2
			}
			if (spacedEnd-i+1)/2 >= 3 {
				decorativeLeader = true
				runEnd = spacedEnd
			}
		}
		if !decorativeLeader {
			for _, runCh := range chars[i:runEnd] {
				if unicode.IsSpace(runCh) {
					continue
				}
				switch runCh {
				case '#', '*', '|', '-', '\n':
					continue
				}
				if unicode.IsLetter(runCh) || unicode.IsDigit(runCh) {
					alnum++
				} else {
					nonAlnum++
				}
			}
		}
		i = runEnd
	}
	total := alnum + nonAlnum
	return total >= 50 && alnum*2 < total
}

func pageReplacementNeedsOCR(ev *evidence) bool {
	if ev.replacementChars == 0 || ev.chars == 0 {
		return false
	}
	if ev.chars <= 80 && ev.longestReplacementRun >= 2 {
		return true
	}
	densityBps := ev.replacementChars * 10000 / ev.chars
	enoughBad := ev.replacementChars >= 12 && densityBps >= 500
	repeatedSpans := ev.replacementSpans >= 3 && densityBps >= 250
	longRun := ev.longestReplacementRun >= 8 && densityBps >= 250
	return enoughBad || repeatedSpans || longRun
}

var englishLetterFreq = [26]float64{
	8.2, 1.5, 2.8, 4.3, 12.7, 2.2, 2.0, 6.1, 7.0, 0.15,
	0.8, 4.0, 2.4, 6.7, 7.5, 1.9, 0.1, 6.0, 6.3, 9.1,
	2.8, 1.0, 2.4, 0.15, 2.0, 0.07,
}

type cipherStats struct {
	letterCounts     [26]int
	asciiLetters     int
	asciiVowels      int
	latinExt         int
	nonLatin         int
	bigrams          int
	caseShiftBigrams int
}

func (c *cipherStats) addText(text string) {
	var prev rune
	havePrev := false
	for _, ch := range text {
		if ch < 128 && unicode.IsLetter(rune(ch)) {
			idx := ch | 0x20
			c.letterCounts[int(idx-'a')]++
			c.asciiLetters++
			switch idx {
			case 'a', 'e', 'i', 'o', 'u':
				c.asciiVowels++
			}
			if havePrev {
				c.bigrams++
				if prev >= 'a' && prev <= 'z' && ch >= 'A' && ch <= 'Z' {
					c.caseShiftBigrams++
				}
			}
			prev, havePrev = ch, true
		} else {
			if unicode.IsLetter(ch) {
				if ch >= 0xC0 && ch <= 0x24F || ch >= 0x1E00 && ch <= 0x1EFF {
					c.latinExt++
				} else {
					c.nonLatin++
				}
			}
			havePrev = false
		}
	}
}

func (c *cipherStats) englishCosine() float64 {
	if c.asciiLetters == 0 {
		return 1
	}
	n := float64(c.asciiLetters)
	dot, normObs := 0.0, 0.0
	for i, count := range c.letterCounts {
		p := float64(count) / n
		dot += p * englishLetterFreq[i]
		normObs += p * p
	}
	normEn := 0.0
	for _, f := range englishLetterFreq {
		normEn += f * f
	}
	normEn = math.Sqrt(normEn)
	return dot / (math.Sqrt(normObs) * normEn)
}

func (c *cipherStats) englishShapeCosine() float64 {
	if c.asciiLetters == 0 {
		return 1
	}
	n := float64(c.asciiLetters)
	obs := make([]float64, 26)
	for i, count := range c.letterCounts {
		obs[i] = float64(count) / n
	}
	slices.Sort(obs)
	slices.Reverse(obs)
	sortedEn := slices.Clone(englishLetterFreq[:])
	slices.Sort(sortedEn)
	slices.Reverse(sortedEn)
	dot, normObs, normEn := 0.0, 0.0, 0.0
	for i := range obs {
		dot += obs[i] * sortedEn[i]
		normObs += obs[i] * obs[i]
		normEn += sortedEn[i] * sortedEn[i]
	}
	return dot / (math.Sqrt(normObs) * math.Sqrt(normEn))
}

func (c *cipherStats) looksGarbled() bool {
	if c.asciiLetters < 200 || c.nonLatin > c.asciiLetters+c.latinExt {
		return false
	}
	vowelRatio := float64(c.asciiVowels) / float64(c.asciiLetters)
	if vowelRatio > 0.30 {
		return false
	}
	caseShifts := c.bigrams >= 100 && float64(c.caseShiftBigrams) >= float64(c.bigrams)*0.10
	permuted := c.englishCosine() < 0.60 && c.englishShapeCosine() >= 0.90
	return caseShifts || permuted
}
