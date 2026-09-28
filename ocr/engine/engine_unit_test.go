package engine

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/gomlx/compute-onnx/support/protos"
)

func TestYAMLList(t *testing.T) {
	src := []byte(`Global:
  model_name: x
PostProcess:
  name: CTCLabelDecode
  character_dict:
  - ` + "\u3000" + `
  - a
  - ''''
  - '#'
  - "é"
  - \
  other: 1
`)
	got, err := yamlList(src, "character_dict")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"　", "a", "'", "#", "é", `\`}
	if len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("item %d = %q, want %q", i, got[i], want[i])
		}
	}
	if _, err := yamlList(src, "missing"); err == nil {
		t.Error("missing key must fail")
	}
}

func TestCTCDecode(t *testing.T) {
	chars := []string{"a", "b", " "}
	// classes: blank, a, b, space
	steps := [][]float32{
		{0.1, 0.8, 0.05, 0.05}, // a
		{0.1, 0.8, 0.05, 0.05}, // a (repeat, merged)
		{0.9, 0.05, 0.05, 0},   // blank
		{0.1, 0.7, 0.1, 0.1},   // a (after blank, kept)
		{0.1, 0.1, 0.1, 0.7},   // space
		{0.1, 0.1, 0.6, 0.2},   // b
	}
	var flat []float32
	for _, s := range steps {
		flat = append(flat, s...)
	}
	text, conf := ctcDecode(flat, len(steps), 4, chars, 0, "")
	if text != "aa b" {
		t.Errorf("text = %q", text)
	}
	if want := (0.8 + 0.7 + 0.7 + 0.6) / 4; math.Abs(conf-want) > 1e-6 {
		t.Errorf("confidence = %v, want %v", conf, want)
	}
}

func TestDBBoxes(t *testing.T) {
	// Two text blobs on a 100×40 map: a wide line and a noise speck.
	const w, h = 100, 40
	prob := make([]float32, w*h)
	for y := 10; y < 20; y++ {
		for x := 10; x < 80; x++ {
			prob[y*w+x] = 0.9
		}
	}
	prob[35*w+95] = 0.9
	boxes := dbBoxes(prob, w, w, h, PostProcess{Type: "db", Thresh: 0.3, BoxThresh: 0.6, UnclipRatio: 1.5}, 2, 2, 200, 80)
	if len(boxes) != 1 {
		t.Fatalf("boxes = %v, want one", boxes)
	}
	x0, y0, x1, y1 := boxes[0].bounds()
	// 70×10 blob, widened by half a pixel, unclipped by 71·11·1.5/164 ≈ 7.1
	// on every side, scaled by 2.
	if math.Abs(x0-(9.5-7.1)*2) > 1.5 || math.Abs(x1-(79.5+7.1)*2) > 1.5 || math.Abs(y0-(9.5-7.1)*2) > 1.5 || math.Abs(y1-(19.5+7.1)*2) > 1.5 {
		t.Errorf("box = %.1f,%.1f-%.1f,%.1f", x0, y0, x1, y1)
	}
}

func TestMinAreaRectRotated(t *testing.T) {
	// Points along a line tilted by ~10°.
	var pts []pt
	for i := range 50 {
		x := float64(i)
		for d := range 5 {
			pts = append(pts, pt{x, x*math.Tan(10*math.Pi/180) + float64(d)})
		}
	}
	q, short := minAreaRect(convexHull(pts))
	w, h := q.size()
	if w < 45 || h > 8 || short > 8 {
		t.Errorf("rect %vx%v short %v quad %v", w, h, short, q)
	}
	if q[0].x > q[1].x || q[0].y > q[3].y {
		t.Errorf("quad not ordered tl,tr,br,bl: %v", q)
	}
}

func TestFlushDenormals(t *testing.T) {
	raw := make([]byte, 12)
	binary.LittleEndian.PutUint32(raw[0:], math.Float32bits(1.5))
	binary.LittleEndian.PutUint32(raw[4:], 0x00000123) // positive subnormal
	binary.LittleEndian.PutUint32(raw[8:], 0x80000001) // negative subnormal
	float := int32(protos.TensorProto_FLOAT)
	mp := &protos.ModelProto{Graph: &protos.GraphProto{
		Initializer: []*protos.TensorProto{
			{DataType: float, RawData: raw},
			{DataType: float, FloatData: []float32{math.Float32frombits(1), 2}},
		},
		Node: []*protos.NodeProto{{OpType: "Constant", Attribute: []*protos.AttributeProto{{Name: "value", T: &protos.TensorProto{DataType: float, FloatData: []float32{math.Float32frombits(7)}}}}}},
	}}
	if n := flushDenormals(mp); n != 4 {
		t.Errorf("flushed %d, want 4", n)
	}
	if got := math.Float32frombits(binary.LittleEndian.Uint32(raw[0:])); got != 1.5 {
		t.Errorf("normal weight changed to %v", got)
	}
	if binary.LittleEndian.Uint32(raw[4:]) != 0 || binary.LittleEndian.Uint32(raw[8:]) != 0x80000000 {
		t.Error("subnormals not zeroed")
	}
	if mp.Graph.Initializer[1].FloatData[0] != 0 || mp.Graph.Initializer[1].FloatData[1] != 2 {
		t.Errorf("float_data = %v", mp.Graph.Initializer[1].FloatData)
	}
}

func TestManifestValidate(t *testing.T) {
	good := Manifest{
		Schema: 1, ID: "x",
		Detector:   Detector{Model: "d.onnx", LimitSide: 960, Input: Input{Scale: 1, Mean: []float64{0, 0, 0}, Std: []float64{1, 1, 1}}, PostProcess: PostProcess{Type: "db"}},
		Recognizer: Recognizer{Model: "r.onnx", Height: 48, Input: Input{Scale: 1, Mean: []float64{0, 0, 0}, Std: []float64{1, 1, 1}}, Charset: Charset{File: "c.txt"}, Decoder: Decoder{Type: "ctc"}},
	}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []func(*Manifest){
		func(m *Manifest) { m.Schema = 2 },
		func(m *Manifest) { m.OutputOrder = "sideways" },
		func(m *Manifest) { m.Detector.PostProcess.Type = "east" },
		func(m *Manifest) { m.Recognizer.Decoder.Type = "beam" },
		func(m *Manifest) { m.Recognizer.Charset.YAML = "x.yml" },
		func(m *Manifest) { m.Recognizer.Input.Std = []float64{1, 0, 1} },
		func(m *Manifest) { m.Recognizer.Widths = []int{640, 320} },
	}
	for i, mutate := range bad {
		m := good
		m.Recognizer.Input.Std = append([]float64{}, good.Recognizer.Input.Std...)
		mutate(&m)
		if err := m.Validate(); err == nil {
			t.Errorf("case %d: invalid manifest accepted", i)
		}
	}
}

func TestAttentionDecode(t *testing.T) {
	chars := []string{"a", "b"}
	// classes: a, b, eos
	logits := []float32{
		5, 0, 0, // a
		0, 5, 0, // b
		0, 0, 5, // eos
		5, 0, 0, // after eos: ignored
	}
	text, conf := decode(logits, 4, 3, chars, Decoder{Type: "attention", EOS: -1, Softmax: true})
	if text != "ab" || conf < 0.98 || conf > 1 {
		t.Errorf("attention = %q %v", text, conf)
	}
}

func TestCTCBlankLastAndMinConfidence(t *testing.T) {
	chars := []string{"a", "b"}
	// classes: a, b, blank(last)
	probs := []float32{
		0.9, 0.05, 0.05,
		0.1, 0.1, 0.8,
		0.2, 0.6, 0.2,
	}
	text, conf := decode(probs, 3, 3, chars, Decoder{Type: "ctc", Blank: -1, Confidence: "min"})
	if text != "ab" || math.Abs(conf-0.6) > 1e-6 {
		t.Errorf("ctc = %q %v", text, conf)
	}
}

func TestStraightBoxes(t *testing.T) {
	const w, h = 60, 30
	prob := make([]float32, w*h)
	for y := 10; y < 14; y++ {
		for x := 10; x < 40; x++ {
			prob[y*w+x] = 0.9
		}
	}
	boxes := dbBoxes(prob, w, w, h, PostProcess{Type: "db", Thresh: 0.3, BoxThresh: 0.1, UnclipRatio: 1.5, MinSize: 2, Boxes: "straight"}, 1, 1, w, h)
	if len(boxes) != 1 {
		t.Fatalf("boxes = %v", boxes)
	}
	q := boxes[0]
	if q[0].y != q[1].y || q[0].x != q[3].x {
		t.Errorf("not axis aligned: %v", q)
	}
}
