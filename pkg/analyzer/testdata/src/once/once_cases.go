package once

import "sync"

var globalOnce sync.Once

type server struct {
	once  sync.Once
	other sync.Once
}

// ========== once-do-deadlock ==========

// Bad: Do called again on the same Once inside its own Do literal.
func BadReentrantDoLiteral() {
	var once sync.Once
	once.Do(func() {
		once.Do(func() {}) // want "once 'once' Do called inside its own Do function"
	})
}

// Bad: the re-entrant Do hides behind control flow inside the literal.
func BadReentrantDoInsideIf(cond bool) {
	var once sync.Once
	once.Do(func() {
		if cond {
			once.Do(func() {}) // want "once 'once' Do called inside its own Do function"
		}
	})
}

// Bad: a deferred Do still runs while the outer Do is in flight.
func BadReentrantDoInDefer() {
	var once sync.Once
	once.Do(func() {
		defer once.Do(func() {}) // want "once 'once' Do called inside its own Do function"
	})
}

// Bad: the function passed to Do is a named top-level function that calls
// Do on the same package-level Once again.
func BadReentrantDoNamedFunc() {
	globalOnce.Do(reentrantSetup)
}

func reentrantSetup() {
	globalOnce.Do(func() {}) // want "once 'globalOnce' Do called inside its own Do function"
}

// Bad: method value on the same receiver field.
func (s *server) BadReentrantDoMethodValue() {
	s.once.Do(s.setup)
}

func (s *server) setup() {
	s.once.Do(func() {}) // want "once 's.once' Do called inside its own Do function"
}

// Bad: a local function literal closing over the same Once.
func BadReentrantDoLocalLiteral() {
	var once sync.Once
	f := func() {
		once.Do(func() {}) // want "once 'once' Do called inside its own Do function"
	}
	once.Do(f)
}

// Bad: an in-place invocation (IIFE) inside the Do function executes
// synchronously.
func BadReentrantDoIIFE() {
	var once sync.Once
	once.Do(func() {
		func() {
			once.Do(func() {}) // want "once 'once' Do called inside its own Do function"
		}()
	})
}

// ========== once-do-deadlock: control flow inside the Do body ==========

// Bad: re-entrant Do inside a for loop.
func BadReentrantDoInForLoop() {
	var once sync.Once
	once.Do(func() {
		for i := 0; i < 1; i++ {
			once.Do(func() {}) // want "once 'once' Do called inside its own Do function"
		}
	})
}

// Bad: re-entrant Do inside a range loop.
func BadReentrantDoInRange() {
	var once sync.Once
	once.Do(func() {
		for range []int{1} {
			once.Do(func() {}) // want "once 'once' Do called inside its own Do function"
		}
	})
}

// Bad: re-entrant Do inside a switch case.
func BadReentrantDoInSwitch(x int) {
	var once sync.Once
	once.Do(func() {
		switch x {
		case 1:
			once.Do(func() {}) // want "once 'once' Do called inside its own Do function"
		}
	})
}

// Bad: re-entrant Do inside a type switch case.
func BadReentrantDoInTypeSwitch(v any) {
	var once sync.Once
	once.Do(func() {
		switch v.(type) {
		case int:
			once.Do(func() {}) // want "once 'once' Do called inside its own Do function"
		}
	})
}

// Bad: re-entrant Do inside a select communication clause.
func BadReentrantDoInSelect(ch chan int) {
	var once sync.Once
	once.Do(func() {
		select {
		case <-ch:
			once.Do(func() {}) // want "once 'once' Do called inside its own Do function"
		}
	})
}

// Bad: the re-entrant Do hides in the else branch.
func BadReentrantDoInElse(cond bool) {
	var once sync.Once
	once.Do(func() {
		if cond {
			_ = cond
		} else {
			once.Do(func() {}) // want "once 'once' Do called inside its own Do function"
		}
	})
}

// Bad: re-entrant Do inside a bare nested block.
func BadReentrantDoInBlock() {
	var once sync.Once
	once.Do(func() {
		{
			once.Do(func() {}) // want "once 'once' Do called inside its own Do function"
		}
	})
}

// Bad: re-entrant Do inside a labeled loop.
func BadReentrantDoInLabeledLoop() {
	var once sync.Once
	once.Do(func() {
	loop:
		for {
			once.Do(func() {}) // want "once 'once' Do called inside its own Do function"
			break loop
		}
	})
}

// Bad: a var initializer that runs an IIFE re-entering Do.
func BadReentrantDoInVarDecl() {
	var once sync.Once
	once.Do(func() {
		x := func() int {
			once.Do(func() {}) // want "once 'once' Do called inside its own Do function"
			return 0
		}()
		_ = x
	})
}

// ========== once-do-deadlock: function resolution variants ==========

// Bad: method value whose declared receiver name (r) differs from the call
// site (s). The Once path is rewritten s.once -> r.once to find the inner
// call; the diagnostic still names it from the caller's perspective (s.once).
func (s *server) BadReentrantDoMethodValueRenamedRecv() {
	s.once.Do(s.setupRenamed)
}

func (r *server) setupRenamed() {
	r.once.Do(func() {}) // want "once 's.once' Do called inside its own Do function"
}

// Bad: a method value that re-enters a package-level Once (not a receiver field).
func (s *server) BadReentrantDoPkgOnceViaMethod() {
	globalOnce.Do(s.touchGlobalOnce)
}

func (s *server) touchGlobalOnce() {
	globalOnce.Do(func() {}) // want "once 'globalOnce' Do called inside its own Do function"
}

// Bad: same path but through an *unnamed* receiver. The Once is package-level,
// not a receiver field, so resolution must not require a receiver name.
func (s *server) BadReentrantDoPkgOnceViaUnnamedRecv() {
	globalOnce.Do(s.touchGlobalUnnamed)
}

func (*server) touchGlobalUnnamed() {
	globalOnce.Do(func() {}) // want "once 'globalOnce' Do called inside its own Do function"
}

// Bad: re-entrant Do through a *sync.Once parameter (pointer is dereferenced).
func BadReentrantDoPointerParam(o *sync.Once) {
	o.Do(func() {
		o.Do(func() {}) // want "once 'o' Do called inside its own Do function"
	})
}

// ========== once-do-nil ==========

// Bad: Do(nil) panics when the function is invoked.
func BadDoNil() {
	var once sync.Once
	once.Do(nil) // want "once 'once' Do called with nil function"
}

// ========== good cases ==========

// Good: nesting two different Onces is fine.
func GoodDifferentOnces() {
	var a, b sync.Once
	a.Do(func() {
		b.Do(func() {})
	})
}

// Good: different Once fields on the same receiver.
func (s *server) GoodDifferentFields() {
	s.once.Do(func() {
		s.other.Do(func() {})
	})
}

// Good: a goroutine launched inside Do runs after the outer Do completes,
// so it blocks but does not deadlock.
func GoodGoroutineInsideDo() {
	var once sync.Once
	once.Do(func() {
		go func() {
			once.Do(func() {})
		}()
	})
}

// Good: a literal that is defined but never invoked does not execute.
func GoodUninvokedLiteral() {
	var once sync.Once
	once.Do(func() {
		helper := func() { once.Do(func() {}) }
		_ = helper
	})
}

// Good: method values on different objects target different Onces.
func GoodDifferentReceivers(s, t *server) {
	s.once.Do(t.setup)
}

// Good: Do has a .Do method but is not a sync.Once.
type httpishClient struct{}

func (c *httpishClient) Do(req string) {}

func GoodNonOnceDo() {
	c := &httpishClient{}
	c.Do("request")
}

// Good: calling Do repeatedly (idempotent init) is the intended use.
func GoodRepeatedDo() {
	var once sync.Once
	for i := 0; i < 3; i++ {
		once.Do(func() {})
	}
}

// Good: method value where the method initializes without re-entering.
func (s *server) initialize() {}

func (s *server) GoodMethodValue() {
	s.once.Do(s.initialize)
}

// Good: a named top-level function passed to Do that never re-enters.
func GoodNamedFuncNoReentry() {
	globalOnce.Do(safeSetup)
}

func safeSetup() {
	_ = 1 + 1
}

// Good: a method value that touches a *different* Once field is not a deadlock.
func (s *server) initOther() {
	s.other.Do(func() {})
}

func (s *server) GoodMethodValueDifferentField() {
	s.once.Do(s.initOther)
}

// Good: a deferred Do on a different Once does not deadlock the outer one.
func GoodDeferDifferentOnce() {
	var a, b sync.Once
	a.Do(func() {
		defer b.Do(func() {})
	})
}

// Good: a goroutine launched from inside a branch still runs after Do returns.
func GoodGoroutineInBranch(cond bool) {
	var once sync.Once
	once.Do(func() {
		if cond {
			go once.Do(func() {})
		}
	})
}

// ========== copy-by-value ==========

func takesOnceByValue(o sync.Once) {} // want "once 'o' is copied by value"

func BadCopyOnce() {
	var once sync.Once
	copied := once // want "once 'once' is copied by value"
	_ = copied
}
