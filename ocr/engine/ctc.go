package engine

import (
	"math"
	"strings"
)

// decode turns one sequence of per-step class scores (steps × classes,
// row-major) into text and a confidence, as the manifest's decoder says.
func decode(scores []float32, steps, classes int, chars []string, d Decoder) (string, float64) {
	if d.Softmax {
		scores = softmax(scores, steps, classes)
	}
	if d.Type == "attention" {
		eos := d.EOS
		if eos < 0 {
			eos = len(chars)
		}
		return attentionDecode(scores, steps, classes, chars, eos, d.Confidence)
	}
	blank := d.Blank
	if blank < 0 {
		blank = len(chars)
	}
	return ctcDecode(scores, steps, classes, chars, blank, d.Confidence)
}

func softmax(scores []float32, steps, classes int) []float32 {
	out := make([]float32, len(scores))
	for t := range steps {
		row := scores[t*classes : (t+1)*classes]
		m := row[0]
		for _, v := range row {
			m = max(m, v)
		}
		sum := 0.0
		for c, v := range row {
			e := math.Exp(float64(v - m))
			out[t*classes+c] = float32(e)
			sum += e
		}
		for c := range row {
			out[t*classes+c] /= float32(sum)
		}
	}
	return out
}

func argmax(row []float32) (int, float32) {
	best, bestP := 0, row[0]
	for c, p := range row {
		if p > bestP {
			best, bestP = c, p
		}
	}
	return best, bestP
}

// ctcDecode greedily decodes CTC output: argmax per step, merge repeats,
// drop blanks. Characters take the non-blank indices in order.
func ctcDecode(probs []float32, steps, classes int, chars []string, blank int, confidence string) (string, float64) {
	var sb strings.Builder
	var kept []float64
	prev := -1
	for t := range steps {
		best, bestP := argmax(probs[t*classes : (t+1)*classes])
		if best != blank && best != prev {
			idx := best
			if best > blank {
				idx--
			}
			if idx >= 0 && idx < len(chars) {
				sb.WriteString(chars[idx])
				kept = append(kept, float64(bestP))
			}
		}
		prev = best
	}
	return sb.String(), aggregate(kept, confidence)
}

// attentionDecode reads one class per position until the end token.
func attentionDecode(probs []float32, steps, classes int, chars []string, eos int, confidence string) (string, float64) {
	var sb strings.Builder
	var kept []float64
	for t := range steps {
		best, bestP := argmax(probs[t*classes : (t+1)*classes])
		if best == eos || best >= len(chars) {
			break
		}
		sb.WriteString(chars[best])
		kept = append(kept, float64(bestP))
	}
	return sb.String(), aggregate(kept, confidence)
}

func aggregate(probs []float64, how string) float64 {
	if len(probs) == 0 {
		return 0
	}
	if how == "min" {
		m := probs[0]
		for _, p := range probs {
			m = math.Min(m, p)
		}
		return m
	}
	sum := 0.0
	for _, p := range probs {
		sum += p
	}
	return sum / float64(len(probs))
}
