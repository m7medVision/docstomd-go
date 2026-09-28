// Package engine runs text detection, line orientation and text recognition
// models described by a manifest, on a GoMLX backend.
//
// A manifest composes reusable building blocks (image preprocessing, a
// detector post-processor, a decoder) around a model family's ONNX files, so
// a new family needs a manifest, not Go code.
package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// ManifestSchema is the manifest format version this package reads.
const ManifestSchema = 1

// ManifestFile is the manifest's file name inside a model directory.
const ManifestFile = "manifest.json"

// Manifest describes one OCR model: a detector, an optional line
// orientation classifier and a recognizer. Paths are relative to the model
// directory.
type Manifest struct {
	Schema    int      `json:"schema"`
	ID        string   `json:"id"`
	Family    string   `json:"family"`
	Languages []string `json:"languages"`
	License   string   `json:"license"`
	// OutputOrder is "logical" (default) when the recognizer emits text in
	// reading order, "visual" when it emits right-to-left scripts in display
	// order and needs bidi reordering.
	OutputOrder string      `json:"output_order,omitempty"`
	Detector    Detector    `json:"detector"`
	Classifier  *Classifier `json:"classifier,omitempty"`
	Recognizer  Recognizer  `json:"recognizer"`
}

// Input is how an image becomes a model input: channel order, then
// value*scale, then (value-mean)/std per channel, laid out NCHW.
type Input struct {
	// Color is "bgr" (default) or "rgb".
	Color string    `json:"color,omitempty"`
	Scale float64   `json:"scale"`
	Mean  []float64 `json:"mean"`
	Std   []float64 `json:"std"`
}

// Detector finds text regions.
type Detector struct {
	Model string `json:"model"`
	Input Input  `json:"input"`
	// LimitSide bounds the input: its longer side (limit_type "max") or its
	// shorter side ("min") is scaled to LimitSide; "fit" scales the image,
	// up or down, to fit a LimitSide square keeping its aspect ratio.
	LimitSide int    `json:"limit_side"`
	LimitType string `json:"limit_type,omitempty"`
	// Pad places the image at the top-left of its bucket ("end", default)
	// or centres it ("center"); PadValue is the padding gray level (default
	// 255, white).
	Pad      string `json:"pad,omitempty"`
	PadValue *int   `json:"pad_value,omitempty"`
	// Multiple rounds input sides (32 for DB-style detectors).
	Multiple int `json:"multiple,omitempty"`
	// Buckets are input sizes [height, width] compiled once per session;
	// inputs are padded to the smallest bucket that holds them.
	Buckets     [][2]int    `json:"buckets,omitempty"`
	PostProcess PostProcess `json:"postprocess"`
}

// PostProcess turns a detector output into text boxes.
type PostProcess struct {
	// Type is "db" (differentiable binarization probability map).
	Type          string  `json:"type"`
	Thresh        float64 `json:"thresh"`
	BoxThresh     float64 `json:"box_thresh"`
	UnclipRatio   float64 `json:"unclip_ratio"`
	MaxCandidates int     `json:"max_candidates,omitempty"`
	MinSize       float64 `json:"min_size,omitempty"`
	// Activation is applied to the raw output first: "" (already a
	// probability map) or "sigmoid" (logits).
	Activation string `json:"activation,omitempty"`
	// Boxes are "rotated" (minimum-area rectangles, default) or "straight"
	// (axis-aligned).
	Boxes string `json:"boxes,omitempty"`
}

// Classifier decides whether a text line is upside down.
type Classifier struct {
	Model string `json:"model"`
	Input Input  `json:"input"`
	// Height and Width are the fixed input size.
	Height int `json:"height"`
	Width  int `json:"width"`
	// Angles lists the rotation, in degrees, each class stands for.
	Angles    []int   `json:"angles"`
	Threshold float64 `json:"threshold"`
	// Batches are batch-size buckets; lines are grouped greedily into the
	// largest bucket that fits (default [1]).
	Batches []int `json:"batches,omitempty"`
}

// Recognizer reads the text of one line image.
type Recognizer struct {
	Model string `json:"model"`
	Input Input  `json:"input"`
	// Height is the fixed input height; widths follow the aspect ratio
	// unless Width fixes them.
	Height int `json:"height"`
	// Width, when set, fixes the input width: lines are resized keeping
	// their aspect ratio (squeezed if wider) and padded at the end.
	Width int `json:"width,omitempty"`
	// PadValue is the gray level of padding in pixel space; unset pads with
	// 0 after normalization.
	PadValue *int `json:"pad_value,omitempty"`
	MinWidth int  `json:"min_width,omitempty"`
	MaxWidth int  `json:"max_width,omitempty"`
	// Widths are ascending width buckets compiled once per session; wider
	// lines use the next multiple of the first bucket.
	Widths []int `json:"widths,omitempty"`
	// Batches are batch-size buckets; lines of similar width are grouped
	// greedily into the largest bucket that fits (default [1]).
	Batches []int   `json:"batches,omitempty"`
	Charset Charset `json:"charset"`
	Decoder Decoder `json:"decoder"`
}

// Charset is the recognizer's character list, one class per character.
type Charset struct {
	// YAML reads a list under Key from a YAML file (for example PaddleOCR's
	// inference.yml, key "character_dict").
	YAML string `json:"yaml,omitempty"`
	Key  string `json:"key,omitempty"`
	// File reads one character per line.
	File string `json:"file,omitempty"`
	// JSON reads a string under Key from a JSON object (for example docTR's
	// config.json, key "vocab"); each character is one class.
	JSON string `json:"json,omitempty"`
}

// Decoder turns recognizer outputs into text.
type Decoder struct {
	// Type is "ctc" (per-step classes, repeats merged, blanks dropped) or
	// "attention" (one class per output position until the end token).
	Type string `json:"type"`
	// Blank is the CTC blank class index; characters take the other
	// indices in order. -1 means the class after the characters.
	Blank int `json:"blank"`
	// EOS is the attention decoder's end-of-sequence class; -1 means the
	// class after the characters.
	EOS int `json:"eos,omitempty"`
	// Softmax turns raw logits into probabilities before decoding.
	Softmax bool `json:"softmax,omitempty"`
	// Confidence is the mean ("mean", default) or the minimum ("min") of
	// the kept characters' probabilities.
	Confidence string `json:"confidence,omitempty"`
	// AppendSpace adds a space class after the character list.
	AppendSpace bool `json:"append_space,omitempty"`
}

// LoadManifest reads and validates dir/manifest.json.
func LoadManifest(dir string) (*Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%s: %v", filepath.Join(dir, ManifestFile), err)
	}
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %v", filepath.Join(dir, ManifestFile), err)
	}
	return &m, nil
}

// Validate checks the fields the engine relies on.
func (m *Manifest) Validate() error {
	switch {
	case m.Schema != ManifestSchema:
		return fmt.Errorf("manifest schema %d, want %d", m.Schema, ManifestSchema)
	case m.ID == "":
		return fmt.Errorf("manifest has no id")
	case m.OutputOrder != "" && m.OutputOrder != "logical" && m.OutputOrder != "visual":
		return fmt.Errorf("output_order %q (logical, visual)", m.OutputOrder)
	case m.Detector.Model == "" || m.Recognizer.Model == "":
		return fmt.Errorf("detector and recognizer models are required")
	case m.Detector.PostProcess.Type != "db":
		return fmt.Errorf("detector postprocess %q is not supported (db)", m.Detector.PostProcess.Type)
	case m.Detector.LimitSide <= 0:
		return fmt.Errorf("detector limit_side must be positive")
	case m.Detector.LimitType != "" && m.Detector.LimitType != "max" && m.Detector.LimitType != "min" && m.Detector.LimitType != "fit":
		return fmt.Errorf("detector limit_type %q (max, min, fit)", m.Detector.LimitType)
	case m.Detector.Pad != "" && m.Detector.Pad != "end" && m.Detector.Pad != "center":
		return fmt.Errorf("detector pad %q (end, center)", m.Detector.Pad)
	case m.Detector.PostProcess.Activation != "" && m.Detector.PostProcess.Activation != "sigmoid":
		return fmt.Errorf("detector activation %q (sigmoid)", m.Detector.PostProcess.Activation)
	case m.Detector.PostProcess.Boxes != "" && m.Detector.PostProcess.Boxes != "rotated" && m.Detector.PostProcess.Boxes != "straight":
		return fmt.Errorf("detector boxes %q (rotated, straight)", m.Detector.PostProcess.Boxes)
	case m.Recognizer.Decoder.Confidence != "" && m.Recognizer.Decoder.Confidence != "mean" && m.Recognizer.Decoder.Confidence != "min":
		return fmt.Errorf("decoder confidence %q (mean, min)", m.Recognizer.Decoder.Confidence)
	case m.Recognizer.Height <= 0:
		return fmt.Errorf("recognizer height must be positive")
	case m.Recognizer.Decoder.Type != "ctc" && m.Recognizer.Decoder.Type != "attention":
		return fmt.Errorf("recognizer decoder %q is not supported (ctc, attention)", m.Recognizer.Decoder.Type)
	case countSet(m.Recognizer.Charset.YAML, m.Recognizer.Charset.File, m.Recognizer.Charset.JSON) != 1:
		return fmt.Errorf("recognizer charset needs exactly one of yaml, file or json")
	}
	for name, in := range map[string]Input{"detector": m.Detector.Input, "recognizer": m.Recognizer.Input} {
		if err := in.validate(); err != nil {
			return fmt.Errorf("%s input: %v", name, err)
		}
	}
	if c := m.Classifier; c != nil {
		if err := c.Input.validate(); err != nil {
			return fmt.Errorf("classifier input: %v", err)
		}
		if c.Model == "" || c.Height <= 0 || c.Width <= 0 || len(c.Angles) == 0 {
			return fmt.Errorf("classifier needs model, height, width and angles")
		}
		for _, a := range c.Angles {
			if a != 0 && a != 180 {
				return fmt.Errorf("classifier angle %d (0, 180)", a)
			}
		}
	}
	for _, b := range m.Detector.Buckets {
		if b[0] <= 0 || b[1] <= 0 {
			return fmt.Errorf("detector bucket %v", b)
		}
	}
	if !slices.IsSorted(m.Recognizer.Widths) {
		return fmt.Errorf("recognizer widths must be ascending")
	}
	batches := [][]int{m.Recognizer.Batches}
	if m.Classifier != nil {
		batches = append(batches, m.Classifier.Batches)
	}
	for _, b := range batches {
		for _, n := range b {
			if n <= 0 {
				return fmt.Errorf("batch sizes must be positive")
			}
		}
	}
	return nil
}

func countSet(values ...string) int {
	n := 0
	for _, v := range values {
		if v != "" {
			n++
		}
	}
	return n
}

func (in Input) validate() error {
	if in.Color != "" && in.Color != "bgr" && in.Color != "rgb" {
		return fmt.Errorf("color %q (bgr, rgb)", in.Color)
	}
	if len(in.Mean) != 3 || len(in.Std) != 3 {
		return fmt.Errorf("mean and std need 3 values")
	}
	for _, s := range in.Std {
		if s == 0 {
			return fmt.Errorf("std must not be 0")
		}
	}
	if in.Scale == 0 {
		return fmt.Errorf("scale must not be 0")
	}
	return nil
}

// Files lists the files the manifest refers to, relative to the model
// directory.
func (m *Manifest) Files() []string {
	files := []string{m.Detector.Model, m.Recognizer.Model}
	if m.Classifier != nil {
		files = append(files, m.Classifier.Model)
	}
	if m.Recognizer.Charset.YAML != "" {
		files = append(files, m.Recognizer.Charset.YAML)
	}
	if m.Recognizer.Charset.File != "" {
		files = append(files, m.Recognizer.Charset.File)
	}
	if m.Recognizer.Charset.JSON != "" {
		files = append(files, m.Recognizer.Charset.JSON)
	}
	return files
}
