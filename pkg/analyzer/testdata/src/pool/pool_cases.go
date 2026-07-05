package pool

import (
	"bytes"
	"sync"
	"unsafe"
)

// ========== pool-non-pointer-value (GCL5001) ==========
//
// sync.Pool.Put(v) stores v in the pool's internal interface. If v is not
// pointer-shaped it is boxed with a heap allocation on every Put, defeating the
// pool. The fix is always to store a pointer (or another pointer-shaped value).

// --- Bad: non-pointer values box and allocate on every Put ---

// A slice ([]byte) is the canonical case: store *[]byte instead.
func BadPutSlice() {
	var p sync.Pool
	buf := make([]byte, 1024)
	p.Put(buf) // want "sync.Pool.Put stores non-pointer value"
}

func BadPutString() {
	var p sync.Pool
	p.Put("value") // want "sync.Pool.Put stores non-pointer value"
}

func BadPutInt() {
	var p sync.Pool
	p.Put(42) // want "sync.Pool.Put stores non-pointer value"
}

// A multi-field struct value is not pointer-shaped.
func BadPutStructValue() {
	var p sync.Pool
	var b bytes.Buffer
	p.Put(b) // want "sync.Pool.Put stores non-pointer value"
}

// --- Good: pointer-shaped values are stored directly, no allocation ---

func GoodPutPointerToSlice() {
	var p sync.Pool
	buf := make([]byte, 1024)
	p.Put(&buf)
}

func GoodPutPointerToStruct() {
	var p sync.Pool
	p.Put(new(bytes.Buffer))
}

func GoodPutMap() {
	var p sync.Pool
	p.Put(make(map[string]int))
}

func GoodPutChan() {
	var p sync.Pool
	p.Put(make(chan int))
}

func GoodPutFunc() {
	var p sync.Pool
	p.Put(func() {})
}

// nil stores a nil interface; no allocation.
func GoodPutNil() {
	var p sync.Pool
	p.Put(nil)
}

// An interface value is assigned to the any parameter without re-boxing.
func GoodPutInterface(err error) {
	var p sync.Pool
	p.Put(err)
}

// --- Precision: a same-named Put on another type must not be flagged ---

type fakePool struct{}

func (fakePool) Put(v any) {}

func PutLookalikeNotFlagged() {
	var fp fakePool
	fp.Put(42)
}

// A Pool reached through a struct field is still a sync.Pool.
type server struct {
	pool sync.Pool
}

func (s *server) BadPutViaField() {
	buf := make([]byte, 8)
	s.pool.Put(buf) // want "sync.Pool.Put stores non-pointer value"
}

// ========== pointer-shape classification (hardening) ==========

// An array of more than one element is not pointer-shaped.
func BadPutArray() {
	var p sync.Pool
	p.Put([2]int{1, 2}) // want "sync.Pool.Put stores non-pointer value"
}

// A single-element array of a pointer-shaped type is itself pointer-shaped.
func GoodPutSingleElemPointerArray() {
	var p sync.Pool
	x := 1
	p.Put([1]*int{&x})
}

type ptrBox struct{ p *int }

// A single-field struct whose field is a pointer is pointer-shaped (Go's
// runtime stores it directly in the interface).
func GoodPutSingleFieldPointerStruct() {
	var p sync.Pool
	x := 1
	p.Put(ptrBox{&x})
}

type intBox struct{ n int }

// A single-field struct whose field is a value is not pointer-shaped.
func BadPutSingleFieldValueStruct() {
	var p sync.Pool
	p.Put(intBox{1}) // want "sync.Pool.Put stores non-pointer value"
}

// unsafe.Pointer is pointer-shaped.
func GoodPutUnsafePointer() {
	var p sync.Pool
	x := 1
	p.Put(unsafe.Pointer(&x))
}

// A zero-size struct shares runtime.zerobase and never allocates, regardless
// of boxing — the common semaphore-token idiom must not be flagged.
func GoodPutEmptyStruct() {
	var p sync.Pool
	p.Put(struct{}{})
}

type token struct{}

// A named zero-size struct is zero-size too.
func GoodPutNamedEmptyStruct() {
	var p sync.Pool
	p.Put(token{})
}

// A zero-length array is zero-size for the same reason.
func GoodPutZeroLengthArray() {
	var p sync.Pool
	p.Put([0]int{})
}

// ========== New returning a non-pointer value ==========

// Bad: New returns a slice, boxed into the pool interface on every miss.
func BadNewReturnsSlice() {
	p := sync.Pool{New: func() any {
		return make([]byte, 1024) // want "sync.Pool.New returns non-pointer value"
	}}
	_ = p
}

// Bad: the returned value flows through a local variable first.
func BadNewReturnsVarSlice() {
	p := sync.Pool{New: func() any {
		b := make([]byte, 8)
		return b // want "sync.Pool.New returns non-pointer value"
	}}
	_ = p
}

// Bad: New assigned as a field returns a struct value.
func BadNewAssignReturnsStruct() {
	var p sync.Pool
	p.New = func() any {
		return bytes.Buffer{} // want "sync.Pool.New returns non-pointer value"
	}
	_ = p.Get()
}

// Good: New returns a pointer.
func GoodNewReturnsPointer() {
	p := sync.Pool{New: func() any {
		return new(bytes.Buffer)
	}}
	_ = p
}

// Good: New assigned as a field returns a pointer.
func GoodNewAssignReturnsPointer() {
	var p sync.Pool
	p.New = func() any {
		return &bytes.Buffer{}
	}
	_ = p.Get()
}

// Conservative: a New with more than one return path is left alone, even though
// one branch returns a non-pointer — the intent is ambiguous.
func NewMultiReturnNotFlagged(cond bool) {
	p := sync.Pool{New: func() any {
		if cond {
			return new(bytes.Buffer)
		}
		return make([]byte, 8)
	}}
	_ = p
}
