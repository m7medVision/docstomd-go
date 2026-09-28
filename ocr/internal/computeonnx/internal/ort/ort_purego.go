// Package ort drives the ONNX Runtime C API through purego: the shared
// library is loaded at run time and its OrtApi function table is called
// directly, so builds need no cgo. CPU execution only; GPU entry points
// report errors.
package ort

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/gomlx/compute/dtypes/bfloat16"
	"github.com/gomlx/compute/dtypes/float16"
)

// Offsets (in pointers) of the OrtApi functions used, from
// offsetof(OrtApi, X)/sizeof(void*). The table is append-only across ONNX
// Runtime releases, so these are stable.
const (
	fnGetErrorMessage                  = 2
	fnCreateEnv                        = 3
	fnCreateSessionFromArray           = 8
	fnRun                              = 9
	fnCreateSessionOptions             = 10
	fnSetSessionExecutionMode          = 13
	fnEnableMemPattern                 = 16
	fnDisableMemPattern                = 17
	fnEnableCpuMemArena                = 18
	fnDisableCpuMemArena               = 19
	fnSetSessionLogSeverityLevel       = 22
	fnSetSessionGraphOptimizationLevel = 23
	fnSetIntraOpNumThreads             = 24
	fnSetInterOpNumThreads             = 25
	fnSessionGetInputCount             = 30
	fnSessionGetOutputCount            = 31
	fnSessionGetInputName              = 36
	fnSessionGetOutputName             = 37
	fnCreateTensorAsOrtValue           = 48
	fnCreateTensorWithDataAsOrtValue   = 49
	fnGetTensorMutableData             = 51
	fnGetTensorElementType             = 60
	fnGetDimensionsCount               = 61
	fnGetDimensions                    = 62
	fnGetTensorTypeAndShape            = 65
	fnCreateCpuMemoryInfo              = 69
	fnAllocatorFree                    = 76
	fnGetAllocatorWithDefaultOptions   = 78
	fnReleaseEnv                       = 92
	fnReleaseStatus                    = 93
	fnReleaseMemoryInfo                = 94
	fnReleaseSession                   = 95
	fnReleaseValue                     = 96
	fnReleaseTensorTypeAndShapeInfo    = 99
	fnReleaseSessionOptions            = 100
	fnAddInitializer                   = 150
)

// errGPU is returned by GPU entry points, which this CPU-only port leaves
// out.
var errGPU = errors.New("ort: GPU execution providers are not available in the purego (CPU-only) build")

var (
	// libc malloc and free: tensor data handed to ONNX Runtime lives in C
	// memory, freed only by Destroy, as in the cgo original. Go memory would
	// be reclaimed as soon as the Go wrapper became unreachable, even while
	// ONNX Runtime still referenced it.
	cMalloc, cFree uintptr
	ortLibPath     string
	ortVersion     string
	ortAPI         uintptr
	ortHandle      uintptr
	ortEnv         *Env
)

const (
	maxAPIVersion = 26
	minAPIVersion = 17
)

func SetSharedLibraryPath(path string) { ortLibPath = path }

// GetVersion returns the loaded ONNX Runtime library version string.
func GetVersion() string { return ortVersion }

// cPtr turns an address in C-owned memory into a pointer. C memory is never
// moved or collected by Go, so holding it as an unsafe.Pointer is safe.
func cPtr(addr uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&addr))
}

// call invokes OrtApi function idx. Pointer arguments must be converted
// inline, uintptr(unsafe.Pointer(p)), in the call expression: uintptrescapes
// then moves what they point to off the goroutine stack and keeps it alive
// for the call. A pointer to a stack variable would otherwise go stale when
// the stack grows before ONNX Runtime writes through it.
//
//go:uintptrescapes
func call(idx int, args ...uintptr) uintptr {
	fn := *(*uintptr)(cPtr(ortAPI + uintptr(idx)*unsafe.Sizeof(uintptr(0))))
	r, _, _ := purego.SyscallN(fn, args...)
	return r
}

func InitializeEnvironment() error {
	if ortAPI != 0 {
		return nil
	}
	if ortLibPath == "" {
		return fmt.Errorf("shared library path not set")
	}
	if err := loadLibc(); err != nil {
		return err
	}
	handle, err := purego.Dlopen(ortLibPath, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return fmt.Errorf("failed to dlopen %q: %v", ortLibPath, err)
	}
	getBase, err := purego.Dlsym(handle, "OrtGetApiBase")
	if err != nil {
		_ = purego.Dlclose(handle)
		return fmt.Errorf("OrtGetApiBase not found in %q: %v", ortLibPath, err)
	}
	base, _, _ := purego.SyscallN(getBase)
	// OrtApiBase: { const OrtApi* (*GetApi)(uint32_t); const char* (*GetVersionString)(void); }
	getAPI := *(*uintptr)(cPtr(base))
	getVersion := *(*uintptr)(cPtr(base + unsafe.Sizeof(uintptr(0))))
	if v, _, _ := purego.SyscallN(getVersion); v != 0 {
		ortVersion = goString(v)
	}
	for version := uintptr(maxAPIVersion); ; version-- {
		if api, _, _ := purego.SyscallN(getAPI, version); api != 0 {
			ortAPI = api
			break
		}
		if version <= minAPIVersion {
			_ = purego.Dlclose(handle)
			return fmt.Errorf("failed to get an ONNX Runtime API between versions %d and %d", minAPIVersion, maxAPIVersion)
		}
	}
	ortHandle = handle
	env, err := NewEnv("gomlx_ort_env")
	if err != nil {
		ortAPI = 0
		_ = purego.Dlclose(handle)
		return err
	}
	ortEnv = env
	return nil
}

func loadLibc() error {
	name := "libc.so.6"
	if runtime.GOOS == "darwin" {
		name = "/usr/lib/libSystem.B.dylib"
	}
	libc, err := purego.Dlopen(name, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return fmt.Errorf("failed to dlopen %s: %v", name, err)
	}
	if cMalloc, err = purego.Dlsym(libc, "malloc"); err != nil {
		return err
	}
	cFree, err = purego.Dlsym(libc, "free")
	return err
}

// goString copies a NUL-terminated C string.
func goString(p uintptr) string {
	if p == 0 {
		return ""
	}
	n := 0
	for *(*byte)(cPtr(p + uintptr(n))) != 0 {
		n++
	}
	return string(unsafe.Slice((*byte)(cPtr(p)), n))
}

// cString is a NUL-terminated copy of s in Go memory; keep it alive while C
// uses it.
func cString(s string) *byte {
	b := make([]byte, len(s)+1)
	copy(b, s)
	return &b[0]
}

func statusToError(status uintptr) error {
	if status == 0 {
		return nil
	}
	msg := goString(call(fnGetErrorMessage, status))
	call(fnReleaseStatus, status)
	return fmt.Errorf("ONNX Runtime error: %s", msg)
}

type Env struct{ env uintptr }

func NewEnv(logID string) (*Env, error) {
	id := cString(logID)
	var env uintptr
	const loggingLevelError = 3
	err := statusToError(call(fnCreateEnv, loggingLevelError, uintptr(unsafe.Pointer(id)), uintptr(unsafe.Pointer(&env))))
	runtime.KeepAlive(id)
	if err != nil {
		return nil, err
	}
	return &Env{env: env}, nil
}

func (e *Env) Destroy() error {
	if e.env != 0 {
		call(fnReleaseEnv, e.env)
		e.env = 0
	}
	return nil
}

type SessionOptions struct{ options uintptr }

func NewSessionOptions() (*SessionOptions, error) {
	var options uintptr
	if err := statusToError(call(fnCreateSessionOptions, uintptr(unsafe.Pointer(&options)))); err != nil {
		return nil, err
	}
	return &SessionOptions{options: options}, nil
}

func (so *SessionOptions) Destroy() error {
	if so.options != 0 {
		call(fnReleaseSessionOptions, so.options)
		so.options = 0
	}
	return nil
}

func (so *SessionOptions) AppendExecutionProviderCUDA(*CUDAProviderOptions) error { return errGPU }

func (so *SessionOptions) AddInitializer(name string, val Value) error {
	if val == nil || val.cValue() == 0 {
		return fmt.Errorf("cannot add nil OrtValue initializer for %s", name)
	}
	n := cString(name)
	err := statusToError(call(fnAddInitializer, so.options, uintptr(unsafe.Pointer(n)), val.cValue()))
	runtime.KeepAlive(n)
	return err
}

func (so *SessionOptions) SetSessionLogSeverityLevel(level int) error {
	return statusToError(call(fnSetSessionLogSeverityLevel, so.options, uintptr(level)))
}

func (so *SessionOptions) SetIntraOpNumThreads(threads int) error {
	return statusToError(call(fnSetIntraOpNumThreads, so.options, uintptr(threads)))
}

func (so *SessionOptions) SetInterOpNumThreads(threads int) error {
	return statusToError(call(fnSetInterOpNumThreads, so.options, uintptr(threads)))
}

func (so *SessionOptions) SetExecutionMode(mode int) error {
	return statusToError(call(fnSetSessionExecutionMode, so.options, uintptr(mode)))
}

func (so *SessionOptions) SetCpuMemArena(enable bool) error {
	if enable {
		return statusToError(call(fnEnableCpuMemArena, so.options))
	}
	return statusToError(call(fnDisableCpuMemArena, so.options))
}

func (so *SessionOptions) SetMemPattern(enable bool) error {
	if enable {
		return statusToError(call(fnEnableMemPattern, so.options))
	}
	return statusToError(call(fnDisableMemPattern, so.options))
}

func (so *SessionOptions) SetGraphOptimizationLevel(level int) error {
	return statusToError(call(fnSetSessionGraphOptimizationLevel, so.options, uintptr(level)))
}

type CUDAProviderOptions struct{}

func NewCUDAProviderOptions() (*CUDAProviderOptions, error)   { return nil, errGPU }
func (c *CUDAProviderOptions) Update(map[string]string) error { return errGPU }
func (c *CUDAProviderOptions) Destroy() error                 { return nil }

type MIGraphXProviderOptions struct {
	DeviceID   int
	FP16Enable bool
	Int8Enable bool
}

func (so *SessionOptions) AppendExecutionProviderMIGraphX(*MIGraphXProviderOptions) error {
	return errGPU
}

func HasMIGraphXSupport() bool { return false }

type Session struct{ session uintptr }

func NewSessionWithONNXData(env *Env, modelBytes []byte, options *SessionOptions) (*Session, error) {
	var session, opts uintptr
	if options != nil {
		opts = options.options
	}
	err := statusToError(call(fnCreateSessionFromArray, env.env, uintptr(unsafe.Pointer(&modelBytes[0])), uintptr(len(modelBytes)), opts, uintptr(unsafe.Pointer(&session))))
	runtime.KeepAlive(modelBytes)
	if err != nil {
		return nil, err
	}
	return &Session{session: session}, nil
}

func (s *Session) Destroy() error {
	if s.session != 0 {
		call(fnReleaseSession, s.session)
		s.session = 0
	}
	return nil
}

func (s *Session) GetInputCount() (int, error) {
	var n uintptr
	err := statusToError(call(fnSessionGetInputCount, s.session, uintptr(unsafe.Pointer(&n))))
	return int(n), err
}

func (s *Session) GetOutputCount() (int, error) {
	var n uintptr
	err := statusToError(call(fnSessionGetOutputCount, s.session, uintptr(unsafe.Pointer(&n))))
	return int(n), err
}

func (s *Session) name(fn, index int, allocator *Allocator) (string, error) {
	var p uintptr
	if err := statusToError(call(fn, s.session, uintptr(index), allocator.allocator, uintptr(unsafe.Pointer(&p)))); err != nil {
		return "", err
	}
	name := goString(p)
	call(fnAllocatorFree, allocator.allocator, p)
	return name, nil
}

func (s *Session) GetInputName(index int, allocator *Allocator) (string, error) {
	return s.name(fnSessionGetInputName, index, allocator)
}

func (s *Session) GetOutputName(index int, allocator *Allocator) (string, error) {
	return s.name(fnSessionGetOutputName, index, allocator)
}

type Allocator struct{ allocator uintptr }

func NewDefaultAllocator() (*Allocator, error) {
	var a uintptr
	if err := statusToError(call(fnGetAllocatorWithDefaultOptions, uintptr(unsafe.Pointer(&a)))); err != nil {
		return nil, err
	}
	return &Allocator{allocator: a}, nil
}

// Destroy is a no-op: the default allocator is owned by ONNX Runtime.
func (a *Allocator) Destroy() error { return nil }

type MemoryInfo struct{ info uintptr }

func NewCpuMemoryInfo() (*MemoryInfo, error) {
	var info uintptr
	const deviceAllocator, memTypeDefault = 0, 0
	if err := statusToError(call(fnCreateCpuMemoryInfo, deviceAllocator, memTypeDefault, uintptr(unsafe.Pointer(&info)))); err != nil {
		return nil, err
	}
	return &MemoryInfo{info: info}, nil
}

func NewCUDAMemoryInfo() (*MemoryInfo, error) { return nil, errGPU }

func (mi *MemoryInfo) Destroy() error {
	if mi.info != 0 {
		call(fnReleaseMemoryInfo, mi.info)
		mi.info = 0
	}
	return nil
}

type DataType int

const (
	TensorElementDataTypeFloat    DataType = 1
	TensorElementDataTypeUint8    DataType = 2
	TensorElementDataTypeInt8     DataType = 3
	TensorElementDataTypeUint16   DataType = 4
	TensorElementDataTypeInt16    DataType = 5
	TensorElementDataTypeInt32    DataType = 6
	TensorElementDataTypeInt64    DataType = 7
	TensorElementDataTypeBool     DataType = 9
	TensorElementDataTypeFloat16  DataType = 10
	TensorElementDataTypeDouble   DataType = 11
	TensorElementDataTypeUint32   DataType = 12
	TensorElementDataTypeUint64   DataType = 13
	TensorElementDataTypeBFloat16 DataType = 16
)

type Shape []int64

func NewShape(dims ...int64) Shape { return Shape(dims) }

type TensorData interface {
	~float32 | ~float64 | ~int32 | ~int64 | ~bool | ~int8 | ~uint8 | ~int16 | ~uint16 | ~uint32 | ~uint64
}

type Value interface {
	GetTensorMutableData() (unsafe.Pointer, error)
	Destroy() error
	cValue() uintptr
}

// RawValue wraps an OrtValue owned by the caller.
type RawValue struct{ val uintptr }

func WrapRawOrtValue(v uintptr) Value { return &RawValue{val: v} }

func tensorData(v uintptr) (unsafe.Pointer, error) {
	var out uintptr
	if err := statusToError(call(fnGetTensorMutableData, v, uintptr(unsafe.Pointer(&out)))); err != nil {
		return nil, err
	}
	return cPtr(out), nil
}

func (r *RawValue) GetTensorMutableData() (unsafe.Pointer, error) { return tensorData(r.val) }

func (r *RawValue) Destroy() error {
	if r.val != 0 {
		call(fnReleaseValue, r.val)
		r.val = 0
	}
	return nil
}

func (r *RawValue) cValue() uintptr { return r.val }

type Tensor[T TensorData] struct {
	val   uintptr
	shape Shape
	// data is C memory ONNX Runtime reads in place, freed by Destroy.
	data uintptr
}

func (t *Tensor[T]) GetShape() Shape                               { return t.shape }
func (t *Tensor[T]) cValue() uintptr                               { return t.val }
func (t *Tensor[T]) GetTensorMutableData() (unsafe.Pointer, error) { return tensorData(t.val) }
func (t *Tensor[T]) updateVal(v uintptr)                           { t.val = v }

func (t *Tensor[T]) GetData() []T {
	p, err := t.GetTensorMutableData()
	if err != nil {
		panic(err)
	}
	size := 1
	for _, d := range t.shape {
		size *= int(d)
	}
	if size == 0 {
		return []T{}
	}
	return unsafe.Slice((*T)(p), size)
}

func (t *Tensor[T]) Destroy() error {
	if t.val != 0 {
		call(fnReleaseValue, t.val)
		t.val = 0
	}
	if t.data != 0 {
		purego.SyscallN(cFree, t.data)
		t.data = 0
	}
	return nil
}

func dataTypeOf[T any]() (DataType, error) {
	var dummy T
	switch any(dummy).(type) {
	case float32:
		return TensorElementDataTypeFloat, nil
	case float64:
		return TensorElementDataTypeDouble, nil
	case int32:
		return TensorElementDataTypeInt32, nil
	case int64:
		return TensorElementDataTypeInt64, nil
	case bool:
		return TensorElementDataTypeBool, nil
	case int8:
		return TensorElementDataTypeInt8, nil
	case uint8:
		return TensorElementDataTypeUint8, nil
	case int16:
		return TensorElementDataTypeInt16, nil
	case uint16:
		return TensorElementDataTypeUint16, nil
	case uint32:
		return TensorElementDataTypeUint32, nil
	case uint64:
		return TensorElementDataTypeUint64, nil
	case float16.Float16:
		return TensorElementDataTypeFloat16, nil
	case bfloat16.BFloat16:
		return TensorElementDataTypeBFloat16, nil
	}
	return 0, fmt.Errorf("unsupported tensor type %T", dummy)
}

// first points at s[0], or is nil for an empty slice.
func first[T any](s []T) unsafe.Pointer {
	if len(s) == 0 {
		return nil
	}
	return unsafe.Pointer(&s[0])
}

func NewTensor[T TensorData](shape Shape, data []T) (*Tensor[T], error) {
	dtype, err := dataTypeOf[T]()
	if err != nil {
		return nil, err
	}
	memInfo, err := NewCpuMemoryInfo()
	if err != nil {
		return nil, err
	}
	defer memInfo.Destroy()
	var dummy T
	n := len(data) * int(unsafe.Sizeof(dummy))
	var ptr uintptr
	if n > 0 {
		ptr, _, _ = purego.SyscallN(cMalloc, uintptr(n))
		if ptr == 0 {
			return nil, fmt.Errorf("out of memory allocating %d bytes", n)
		}
		copy(unsafe.Slice((*T)(cPtr(ptr)), len(data)), data)
	}
	var val uintptr
	err = statusToError(call(fnCreateTensorWithDataAsOrtValue, memInfo.info, ptr, uintptr(n), uintptr(first(shape)), uintptr(len(shape)), uintptr(dtype), uintptr(unsafe.Pointer(&val))))
	runtime.KeepAlive(shape)
	if err != nil {
		if ptr != 0 {
			purego.SyscallN(cFree, ptr)
		}
		return nil, err
	}
	return &Tensor[T]{val: val, shape: shape, data: ptr}, nil
}

func NewEmptyTensor[T TensorData](shape Shape) (*Tensor[T], error) {
	dtype, err := dataTypeOf[T]()
	if err != nil {
		return nil, err
	}
	allocator, err := NewDefaultAllocator()
	if err != nil {
		return nil, err
	}
	var val uintptr
	err = statusToError(call(fnCreateTensorAsOrtValue, allocator.allocator, uintptr(first(shape)), uintptr(len(shape)), uintptr(dtype), uintptr(unsafe.Pointer(&val))))
	runtime.KeepAlive(shape)
	if err != nil {
		return nil, err
	}
	return &Tensor[T]{val: val, shape: shape}, nil
}

// run calls OrtApi Run with names already NUL-terminated.
func run(session uintptr, names []*byte, inputs []Value, outNames []*byte, outputs []Value) error {
	inVals := make([]uintptr, len(inputs))
	for i, v := range inputs {
		if v == nil || v.cValue() == 0 {
			return fmt.Errorf("input %d is nil", i)
		}
		inVals[i] = v.cValue()
	}
	outVals := make([]uintptr, len(outputs))
	for i, v := range outputs {
		if v != nil {
			outVals[i] = v.cValue()
		}
	}
	err := statusToError(call(fnRun, session, 0, uintptr(first(names)), uintptr(first(inVals)), uintptr(len(inVals)), uintptr(first(outNames)), uintptr(len(outVals)), uintptr(first(outVals))))
	runtime.KeepAlive(names)
	runtime.KeepAlive(outNames)
	runtime.KeepAlive(inVals)
	runtime.KeepAlive(inputs)
	if err != nil {
		return err
	}
	for i := range outputs {
		if outputs[i] != nil {
			if t, ok := outputs[i].(interface{ updateVal(uintptr) }); ok {
				t.updateVal(outVals[i])
			}
			continue
		}
		v, err := createGoValueFromOrtValue(outVals[i])
		if err != nil {
			return err
		}
		outputs[i] = v
	}
	return nil
}

func cStrings(names []string) []*byte {
	out := make([]*byte, len(names))
	for i, n := range names {
		out[i] = cString(n)
	}
	return out
}

func (s *Session) Run(inputNames []string, inputValues []Value, outputNames []string, outputValues []Value) error {
	return run(s.session, cStrings(inputNames), inputValues, cStrings(outputNames), outputValues)
}

type DynamicAdvancedSession struct {
	session      *Session
	inputNames   []string
	outputNames  []string
	cInputNames  []*byte
	cOutputNames []*byte
}

func NewDynamicAdvancedSessionWithONNXData(modelBytes []byte, inputNames []string, outputNames []string, options *SessionOptions) (*DynamicAdvancedSession, error) {
	session, err := NewSessionWithONNXData(ortEnv, modelBytes, options)
	if err != nil {
		return nil, err
	}
	return &DynamicAdvancedSession{session: session, inputNames: inputNames, outputNames: outputNames,
		cInputNames: cStrings(inputNames), cOutputNames: cStrings(outputNames)}, nil
}

func (s *DynamicAdvancedSession) CreateIoBinding() (*IoBinding, error) { return nil, errGPU }
func (s *DynamicAdvancedSession) CInputNames() []*byte                 { return s.cInputNames }
func (s *DynamicAdvancedSession) COutputNames() []*byte                { return s.cOutputNames }

func (s *DynamicAdvancedSession) Destroy() error {
	s.cInputNames, s.cOutputNames = nil, nil
	return s.session.Destroy()
}

func (s *DynamicAdvancedSession) Run(inputs []Value, outputs []Value) error {
	return run(s.session.session, s.cInputNames, inputs, s.cOutputNames, outputs)
}

func shapeInfo(v uintptr) (DataType, Shape, error) {
	if v == 0 {
		return 0, nil, fmt.Errorf("nil OrtValue")
	}
	var info uintptr
	if err := statusToError(call(fnGetTensorTypeAndShape, v, uintptr(unsafe.Pointer(&info)))); err != nil {
		return 0, nil, err
	}
	defer call(fnReleaseTensorTypeAndShapeInfo, info)
	var elem int32
	if err := statusToError(call(fnGetTensorElementType, info, uintptr(unsafe.Pointer(&elem)))); err != nil {
		return 0, nil, err
	}
	var n uintptr
	if err := statusToError(call(fnGetDimensionsCount, info, uintptr(unsafe.Pointer(&n)))); err != nil {
		return 0, nil, err
	}
	shape := make(Shape, n)
	if n > 0 {
		if err := statusToError(call(fnGetDimensions, info, uintptr(unsafe.Pointer(&shape[0])), n)); err != nil {
			return 0, nil, err
		}
	}
	return DataType(elem), shape, nil
}

func GetOrtValueShape(v uintptr) (Shape, error) {
	_, shape, err := shapeInfo(v)
	return shape, err
}

func ShapeOf(v Value) (Shape, error) { return GetOrtValueShape(v.cValue()) }

func createGoValueFromOrtValue(v uintptr) (Value, error) {
	dt, shape, err := shapeInfo(v)
	if err != nil {
		return nil, err
	}
	switch dt {
	case TensorElementDataTypeFloat:
		return &Tensor[float32]{val: v, shape: shape}, nil
	case TensorElementDataTypeDouble:
		return &Tensor[float64]{val: v, shape: shape}, nil
	case TensorElementDataTypeInt32:
		return &Tensor[int32]{val: v, shape: shape}, nil
	case TensorElementDataTypeInt64:
		return &Tensor[int64]{val: v, shape: shape}, nil
	case TensorElementDataTypeBool:
		return &Tensor[bool]{val: v, shape: shape}, nil
	case TensorElementDataTypeInt8:
		return &Tensor[int8]{val: v, shape: shape}, nil
	case TensorElementDataTypeUint8:
		return &Tensor[uint8]{val: v, shape: shape}, nil
	case TensorElementDataTypeInt16:
		return &Tensor[int16]{val: v, shape: shape}, nil
	case TensorElementDataTypeUint16:
		return &Tensor[uint16]{val: v, shape: shape}, nil
	case TensorElementDataTypeUint32:
		return &Tensor[uint32]{val: v, shape: shape}, nil
	case TensorElementDataTypeUint64:
		return &Tensor[uint64]{val: v, shape: shape}, nil
	}
	return nil, fmt.Errorf("unsupported tensor element type: %d", dt)
}

// IoBinding is GPU-only; the CPU-only port cannot create one.
type IoBinding struct{}

func (b *IoBinding) Destroy()                                    {}
func (b *IoBinding) BindInput(*byte, Value) error                { return errGPU }
func (b *IoBinding) BindOutput(*byte, Value) error               { return errGPU }
func (b *IoBinding) BindOutputToDevice(*byte, *MemoryInfo) error { return errGPU }
func (b *IoBinding) RunWithBinding() error                       { return errGPU }
func (b *IoBinding) SynchronizeBoundOutputs() error              { return errGPU }
func (b *IoBinding) GetBoundOutputValues() ([]uintptr, error)    { return nil, errGPU }
func (b *IoBinding) ClearBoundInputs()                           {}
func (b *IoBinding) ClearBoundOutputs()                          {}

func CopyGPUToHost(Value, unsafe.Pointer, int) error { return errGPU }
func CopyHostToGPU(Value, unsafe.Pointer, int) error { return errGPU }
func HipDeviceSynchronize() error                    { return errGPU }
func LoadHIPLibrary() bool                           { return false }
