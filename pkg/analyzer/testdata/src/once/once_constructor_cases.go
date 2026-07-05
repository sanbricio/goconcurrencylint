package once

import "sync"

// ========== once-constructor-nil (GCL3003) ==========

// Bad: OnceFunc(nil) — the returned function invokes a nil func and panics.
func BadOnceFuncNil() {
	handle := sync.OnceFunc(nil) // want "sync.OnceFunc called with nil function"
	handle()
}

// Bad: OnceValue[int](nil) — a generic instantiation (IndexExpr) still resolves
// to sync.OnceValue.
func BadOnceValueNil() {
	get := sync.OnceValue[int](nil) // want "sync.OnceValue called with nil function"
	_ = get()
}

// Bad: OnceValues[int, string](nil) — two type arguments (IndexListExpr).
func BadOnceValuesNil() {
	get := sync.OnceValues[int, string](nil) // want "sync.OnceValues called with nil function"
	_, _ = get()
}

// Bad: the nil literal survives redundant parentheses.
func BadOnceFuncNilParen() {
	_ = sync.OnceFunc((nil)) // want "sync.OnceFunc called with nil function"
}

// Good: a real function is memoized.
func GoodOnceFunc() {
	handle := sync.OnceFunc(func() { initResource() })
	handle()
}

// Good: OnceValue with a real function.
func GoodOnceValue() {
	get := sync.OnceValue(func() int { return 42 })
	_ = get()
}

// Precision: a nil-typed variable is not the literal nil, so it is not flagged;
// only the statically-known nil literal is reported.
func OnceFuncNilVarNotFlagged() {
	var f func()
	_ = sync.OnceFunc(f)
}

// Precision: a same-named method on another type must not be flagged — only the
// real sync constructors are.
type fakeOnce struct{}

func (fakeOnce) OnceFunc(f func()) func() { return f }

func OnceFuncLookalikeNotFlagged() {
	var x fakeOnce
	_ = x.OnceFunc(nil)
}

func initResource() {}
