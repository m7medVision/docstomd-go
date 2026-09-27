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
	// shorter side ("min") is scaled to LimitSide.
	LimitSide int    `json:"limit_side"`
	LimitType string `json:"limit_type,omitempty"`
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
	Batch     int     `json:"batch,omitempty"`
}

// Recognizer reads the text of one line image.
type Recognizer struct {
	Model string `json:"model"`
	Input Input  `json:"input"`
	// Height is the fixed input height; widths follow the aspect ratio.
	Height   int `json:"height"`
	MinWidth int `json:"min_width,omitempty"`
	MaxWidth int `json:"max_width,omitempty"`
	// Widths are ascending width buckets compiled once per session; wider
	// lines use the next multiple of the first bucket.
	Widths  []int   `json:"widths,omitempty"`
	Batch   int     `json:"batch,omitempty"`
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
}

// Decoder turns recognizer outputs into text.
type Decoder struct {
	// Type is "ctc".
	Type string `json:"type"`
	// Blank is the CTC blank class index; characters follow it in order.
	Blank int `json:"blank"`
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
	case m.Detector.LimitType != "" && m.Detector.LimitType != "max" && m.Detector.LimitType != "min":
		return fmt.Errorf("detector limit_type %q (max, min)", m.Detector.LimitType)
	case m.Recognizer.Height <= 0:
		return fmt.Errorf("recognizer height must be positive")
	case m.Recognizer.Decoder.Type != "ctc":
		return fmt.Errorf("recognizer decoder %q is not supported (ctc)", m.Recognizer.Decoder.Type)
	case (m.Recognizer.Charset.YAML == "") == (m.Recognizer.Charset.File == ""):
		return fmt.Errorf("recognizer charset needs exactly one of yaml or file")
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
	return nil
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
