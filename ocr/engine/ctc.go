package engine

import "strings"

// ctcDecode greedily decodes one sequence of class probabilities (steps ×
// classes, row-major): argmax per step, merge repeats, drop blanks. It
// returns the text and the mean probability of the kept characters.
func ctcDecode(probs []float32, steps, classes int, chars []string, blank int) (string, float64) {
	var sb strings.Builder
	sum, kept := 0.0, 0
	prev := -1
	for t := range steps {
		row := probs[t*classes : (t+1)*classes]
		best, bestP := 0, row[0]
		for c, p := range row {
			if p > bestP {
				best, bestP = c, p
			}
		}
		if best != blank && best != prev {
			idx := best - blank - 1
			if best < blank {
				idx = best
			}
			if idx >= 0 && idx < len(chars) {
				sb.WriteString(chars[idx])
				sum += float64(bestP)
				kept++
			}
		}
		prev = best
	}
	if kept == 0 {
		return "", 0
	}
	return sb.String(), sum / float64(kept)
}
