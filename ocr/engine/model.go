package engine

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"sync/atomic"

	"github.com/gomlx/compute"
	"github.com/gomlx/compute-onnx/support/protos"
	. "github.com/gomlx/gomlx/core/graph" //nolint:revive // GoMLX graph DSL
	"github.com/gomlx/gomlx/core/tensors"
	"github.com/gomlx/gomlx/ml/model"
	"google.golang.org/protobuf/proto"

	"github.com/m7medVision/docstomd-go/ocr/internal/onnxgomlx/onnx/parser"
)

// minNormal is the smallest positive normal float32.
const minNormal = 0x1p-126

// net is one ONNX model compiled for a backend. It is not safe for
// concurrent use.
type net struct {
	name   string
	exec   *model.Exec
	store  *model.Store
	input  string
	output string
	// builds counts graph builds: one per distinct input shape.
	builds atomic.Int64
	// flushed counts the denormal weights zeroed at load.
	flushed int
}

// loadNet reads an ONNX file, zeroes its denormal weights and prepares it
// for backend.
func loadNet(backend compute.Backend, path string) (*net, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var mp protos.ModelProto
	if err := proto.Unmarshal(raw, &mp); err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	flushed := flushDenormals(&mp)
	raw, err = proto.Marshal(&mp)
	if err != nil {
		return nil, err
	}
	m, err := parser.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	inputs, _ := m.Inputs()
	outputs, _ := m.Outputs()
	if len(inputs) != 1 || len(outputs) < 1 {
		return nil, fmt.Errorf("%s: want one input and at least one output, got %v → %v", path, inputs, outputs)
	}
	store := model.NewStore()
	if err := m.VariablesToScope(store.RootScope()); err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	n := &net{name: path, store: store, input: inputs[0], output: outputs[0], flushed: flushed}
	exec, err := model.NewExec(backend, store, func(scope *model.Scope, x *Node) *Node {
		n.builds.Add(1)
		return m.CallGraph(scope, x.Graph(), map[string]*Node{n.input: x}, n.output)[0]
	})
	if err != nil {
		return nil, err
	}
	exec.SetMaxCache(64)
	n.exec = exec
	return n, nil
}

// run executes the model on a float32 NCHW input and returns the flat
// output and its dimensions.
func (n *net) run(data []float32, dims ...int) (out []float32, outDims []int, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%s: %v", n.name, r)
		}
	}()
	x := tensors.FromFlatDataAndDimensions(data, dims...)
	y, err := n.exec.Call1(x)
	x.FinalizeAll()
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %v", n.name, err)
	}
	defer y.FinalizeAll()
	out, err = tensors.CopyFlatData[float32](y)
	if err != nil {
		return nil, nil, err
	}
	return out, y.Shape().Dimensions, nil
}

// denormals counts subnormal float32 values in the loaded weights.
func (n *net) denormals() int {
	count := 0
	for v := range n.store.IterVariables() {
		t, err := v.Value()
		if err != nil || t == nil {
			continue
		}
		data, err := tensors.CopyFlatData[float32](t)
		if err != nil {
			continue
		}
		for _, f := range data {
			if f != 0 && math.Abs(float64(f)) < minNormal {
				count++
			}
		}
	}
	return count
}

func (n *net) close() {
	n.exec.Finalize()
}

// flushDenormals zeroes subnormal float32 weights in initializers and
// constant nodes. CPUs process subnormals through slow microcode paths; some
// exported models carry millions of them, slowing inference by 20× or more.
func flushDenormals(mp *protos.ModelProto) int {
	if mp.Graph == nil {
		return 0
	}
	count := 0
	for _, t := range mp.Graph.Initializer {
		count += flushTensor(t)
	}
	for _, node := range mp.Graph.Node {
		for _, attr := range node.Attribute {
			if attr.T != nil {
				count += flushTensor(attr.T)
			}
		}
	}
	return count
}

func flushTensor(t *protos.TensorProto) int {
	if t.DataType != int32(protos.TensorProto_FLOAT) {
		return 0
	}
	count := 0
	for i, f := range t.FloatData {
		if f != 0 && math.Abs(float64(f)) < minNormal {
			t.FloatData[i] = 0
			count++
		}
	}
	raw := t.RawData
	for i := 0; i+4 <= len(raw); i += 4 {
		bits := binary.LittleEndian.Uint32(raw[i:])
		// Exponent bits all zero and a non-zero mantissa: subnormal.
		if bits&0x7f800000 == 0 && bits&0x007fffff != 0 {
			binary.LittleEndian.PutUint32(raw[i:], bits&0x80000000)
			count++
		}
	}
	return count
}
