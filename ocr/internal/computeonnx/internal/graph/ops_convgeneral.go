// Copyright 2023-2026 The GoMLX Authors. SPDX-License-Identifier: Apache-2.0

package graph

import (
	"fmt"
	"slices"

	"github.com/gomlx/compute"
	onnx "github.com/gomlx/compute-onnx/support/protos"
	"github.com/gomlx/compute/dtypes"
	"github.com/gomlx/compute/shapeinference"
	"github.com/gomlx/compute/shapes"
	"github.com/pkg/errors"
)

func (f *Function) ConvGeneral(input, kernel compute.Value, axes compute.ConvolveAxesConfig, strides []int, paddings [][2]int, inputDilations []int, kernelDilations []int, channelGroupCount int, batchGroupCount int) (compute.Value, error) {
	if channelGroupCount == 0 {
		channelGroupCount = 1
	}
	if batchGroupCount == 0 {
		batchGroupCount = 1
	}

	inputNode, ok1 := input.(*Node)
	kernelNode, ok2 := kernel.(*Node)
	if !ok1 || !ok2 {
		return nil, errors.New("inputs must be valid onnxruntime nodes")
	}

	outShape, err := shapeinference.ConvGeneral(inputNode.shape, kernelNode.shape, axes, strides, paddings, inputDilations, kernelDilations, channelGroupCount, batchGroupCount)
	if err != nil {
		return nil, err
	}

	rank := inputNode.shape.Rank()
	numSpatial := rank - 2

	// ONNX Conv operator does not support BFloat16 in standard ONNX Runtime schemas.
	if inputNode.shape.DType == dtypes.BFloat16 {
		return nil, errors.Wrapf(compute.ErrNotImplemented, "ONNX doesn't support BFloat16 for Conv: standard ONNX Conv schema does not support tensor(bfloat16)")
	}

	// ONNX Conv operator does not natively support input dilations (atrous/upsampled inputs)
	// or batch grouping (batchGroupCount > 1).
	// Return compute.ErrNotImplemented directly (without wrapping) so testutil.SkipIfMissing can detect it.
	if batchGroupCount > 1 {
		return nil, errors.Wrap(compute.ErrNotImplemented, "batchGroupCount > 1 not supported by ONNX Conv")
	}

	// 1. Transpose input to NCHW / NCDHW format (Batch, Channels, Spatial...)
	inputPerm := make([]int, rank)
	inputPerm[0] = axes.InputBatch
	inputPerm[1] = axes.InputChannels
	copy(inputPerm[2:], axes.InputSpatial)

	needInputTranspose := false
	for i, p := range inputPerm {
		if p != i {
			needInputTranspose = true
			break
		}
	}

	var inpVal compute.Value = inputNode
	if needInputTranspose {
		inpVal, err = f.Transpose(inputNode, inputPerm...)
		if err != nil {
			return nil, errors.Wrap(err, "ConvGeneral: input transpose failed")
		}
	}

	// 1a. docstomd patch: an input-dilated convolution is an ONNX ConvTranspose
	// with a flipped, input/output-swapped kernel, which ONNX Runtime runs natively.
	if channelGroupCount == 1 && slices.Max(append([]int{1}, inputDilations...)) > 1 && slices.Max(append([]int{1}, strides...)) == 1 {
		kv := compute.Value(kernelNode)
		kperm := make([]int, rank)
		kperm[0], kperm[1] = axes.KernelInputChannels, axes.KernelOutputChannels
		copy(kperm[2:], axes.KernelSpatial)
		kv, err = f.Transpose(kv, kperm...)
		if err != nil {
			return nil, err
		}
		sp := make([]int, numSpatial)
		for i := range sp {
			sp[i] = 2 + i
		}
		kv, err = f.Reverse(kv, sp...)
		if err != nil {
			return nil, err
		}
		kshape := kv.(*Node).shape.Dimensions
		pads := make([]int64, 2*numSpatial)
		outPad := make([]int64, numSpatial)
		dil := make([]int64, numSpatial)
		str := make([]int64, numSpatial)
		ok := true
		for i := range numSpatial {
			kd := 1
			if i < len(kernelDilations) && kernelDilations[i] > 0 {
				kd = kernelDilations[i]
			}
			sd := 1
			if i < len(inputDilations) && inputDilations[i] > 0 {
				sd = inputDilations[i]
			}
			var pb, pe int
			if i < len(paddings) {
				pb, pe = paddings[i][0], paddings[i][1]
			}
			ke := (kshape[2+i]-1)*kd + 1
			b, e := ke-1-pb, ke-1-pe
			if e < 0 {
				outPad[i] = int64(-e)
				e = 0
			}
			if b < 0 || int(outPad[i]) >= max(sd, kd) {
				ok = false
			}
			pads[i], pads[i+numSpatial] = int64(b), int64(e)
			dil[i], str[i] = int64(kd), int64(sd)
		}
		if ok {
			ctOut := make([]int, rank)
			ctOut[0] = outShape.Dimensions[axes.OutputBatch]
			ctOut[1] = outShape.Dimensions[axes.OutputChannels]
			for i, a := range axes.OutputSpatial {
				ctOut[2+i] = outShape.Dimensions[a]
			}
			f.nodeCount++
			ct := &Node{
				name:   fmt.Sprintf("node_%d", f.nodeCount),
				opType: "ConvTranspose",
				inputs: []*Node{inpVal.(*Node), kv.(*Node)},
				shape:  shapes.Make(outShape.DType, ctOut...),
				attributes: []*onnx.AttributeProto{
					{Name: "pads", Type: onnx.AttributeProto_INTS, Ints: pads},
					{Name: "strides", Type: onnx.AttributeProto_INTS, Ints: str},
					{Name: "dilations", Type: onnx.AttributeProto_INTS, Ints: dil},
					{Name: "output_padding", Type: onnx.AttributeProto_INTS, Ints: outPad},
				},
			}
			f.nodes = append(f.nodes, ct)
			operm := make([]int, rank)
			operm[axes.OutputBatch] = 0
			operm[axes.OutputChannels] = 1
			for i, a := range axes.OutputSpatial {
				operm[a] = 2 + i
			}
			identity := true
			for i, p := range operm {
				identity = identity && p == i
			}
			if identity {
				return ct, nil
			}
			return f.Transpose(ct, operm...)
		}
	}

	// 1b. docstomd patch: other input dilations are emulated by inserting (d-1)
	// zeros along each spatial axis via Reshape -> Pad(end) -> Reshape -> Slice.
	for i, d := range inputDilations {
		if d <= 1 {
			continue
		}
		cur := inpVal.(*Node).shape.Dimensions
		a := 2 + i
		dims := append(append(append([]int{}, cur[:a+1]...), 1), cur[a+1:]...)
		v, err := f.Reshape(inpVal, dims...)
		if err != nil {
			return nil, err
		}
		var zero compute.Value
		switch inputNode.shape.DType {
		case dtypes.Float32:
			zero, err = f.Constant([]float32{0})
		case dtypes.Float64:
			zero, err = f.Constant([]float64{0})
		default:
			return nil, errors.Wrap(compute.ErrNotImplemented, "input dilations only for float32/64")
		}
		if err != nil {
			return nil, err
		}
		pads := make([]compute.PadAxis, len(dims))
		pads[a+1].End = d - 1
		v, err = f.Pad(v, zero, pads...)
		if err != nil {
			return nil, err
		}
		merged := append(append(append([]int{}, cur[:a]...), cur[a]*d), cur[a+1:]...)
		v, err = f.Reshape(v, merged...)
		if err != nil {
			return nil, err
		}
		start := make([]int, len(merged))
		limit := append([]int{}, merged...)
		limit[a] = (cur[a]-1)*d + 1
		ones := make([]int, len(merged))
		for k := range ones {
			ones[k] = 1
		}
		v, err = f.Slice(v, start, limit, ones)
		if err != nil {
			return nil, err
		}
		inpVal = v
	}

	// 2. Transpose kernel to OIHW / OIDHW format (OutputChannels, InputChannels, Spatial...)
	kernelPerm := make([]int, rank)
	kernelPerm[0] = axes.KernelOutputChannels
	kernelPerm[1] = axes.KernelInputChannels
	copy(kernelPerm[2:], axes.KernelSpatial)

	needKernelTranspose := false
	for i, p := range kernelPerm {
		if p != i {
			needKernelTranspose = true
			break
		}
	}

	var kerVal compute.Value = kernelNode
	if needKernelTranspose {
		kerVal, err = f.Transpose(kernelNode, kernelPerm...)
		if err != nil {
			return nil, errors.Wrap(err, "ConvGeneral: kernel transpose failed")
		}
	}

	// 3. Prepare Attributes for ONNX Conv (pads, strides, dilations, group)
	onnxPads := make([]int64, numSpatial*2)
	if len(paddings) > 0 {
		for i := range numSpatial {
			if i < len(paddings) {
				onnxPads[i] = int64(paddings[i][0])            // begin
				onnxPads[i+numSpatial] = int64(paddings[i][1]) // end
			}
		}
	}

	onnxStrides := make([]int64, numSpatial)
	for i := range numSpatial {
		if i < len(strides) {
			onnxStrides[i] = int64(strides[i])
		} else {
			onnxStrides[i] = 1
		}
	}

	onnxDilations := make([]int64, numSpatial)
	for i := range numSpatial {
		if i < len(kernelDilations) {
			onnxDilations[i] = int64(kernelDilations[i])
		} else {
			onnxDilations[i] = 1
		}
	}

	attributes := []*onnx.AttributeProto{
		{
			Name: "pads",
			Type: onnx.AttributeProto_INTS,
			Ints: onnxPads,
		},
		{
			Name: "strides",
			Type: onnx.AttributeProto_INTS,
			Ints: onnxStrides,
		},
		{
			Name: "dilations",
			Type: onnx.AttributeProto_INTS,
			Ints: onnxDilations,
		},
	}

	if channelGroupCount > 1 {
		attributes = append(attributes, &onnx.AttributeProto{
			Name: "group",
			Type: onnx.AttributeProto_INT,
			I:    int64(channelGroupCount),
		})
	}

	// ONNX Conv operator in ONNX Runtime computes Conv outputs using Float32 buffers.
	// Cast integer and Float64 inputs to Float32 before Conv execution and cast output back.
	origDType := outShape.DType
	needCastToFloat32 := !origDType.IsFloat() || origDType == dtypes.Float64
	if needCastToFloat32 {
		var errCast error
		inpVal, errCast = f.ConvertDType(inpVal, dtypes.Float32)
		if errCast != nil {
			return nil, errCast
		}
		kerVal, errCast = f.ConvertDType(kerVal, dtypes.Float32)
		if errCast != nil {
			return nil, errCast
		}
	}

	// 4. Construct intermediate NCHW output shape for Conv node
	convOutDType := outShape.DType
	if needCastToFloat32 {
		convOutDType = dtypes.Float32
	}
	convOutDims := make([]int, rank)
	convOutDims[0] = outShape.Dimensions[axes.OutputBatch]
	convOutDims[1] = outShape.Dimensions[axes.OutputChannels]
	for i, spatialIdx := range axes.OutputSpatial {
		convOutDims[2+i] = outShape.Dimensions[spatialIdx]
	}
	convOutShape := shapes.Make(convOutDType, convOutDims...)

	f.nodeCount++
	convNode := &Node{
		name:       fmt.Sprintf("node_%d", f.nodeCount),
		opType:     "Conv",
		inputs:     []*Node{inpVal.(*Node), kerVal.(*Node)},
		shape:      convOutShape,
		attributes: attributes,
	}
	f.nodes = append(f.nodes, convNode)

	// 5. Transpose conv output back to target layout (OutputBatch, OutputChannels, OutputSpatial...)
	// convNode layout is [0=Batch, 1=Channels, 2..2+spatialRank-1=Spatial...]
	outputPerm := make([]int, rank)
	outputPerm[axes.OutputBatch] = 0
	outputPerm[axes.OutputChannels] = 1
	for i, spatialIdx := range axes.OutputSpatial {
		outputPerm[spatialIdx] = 2 + i
	}

	needOutputTranspose := false
	for i, p := range outputPerm {
		if p != i {
			needOutputTranspose = true
			break
		}
	}

	var finalVal compute.Value = convNode
	if needOutputTranspose {
		var errT error
		finalVal, errT = f.Transpose(convNode, outputPerm...)
		if errT != nil {
			return nil, errT
		}
	}

	if needCastToFloat32 {
		return f.ConvertDType(finalVal, origDType)
	}

	return finalVal, nil
}
