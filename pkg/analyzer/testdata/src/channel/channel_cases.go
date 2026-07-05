package channel

// ========== close-of-nil-channel (GCL6001) ==========

// Bad: closing a nil channel panics.
func BadCloseNil() {
	var ch chan int
	close(ch) // want "close of nil channel 'ch'"
}

// Good: the channel is made before it is closed.
func GoodCloseMade() {
	ch := make(chan int)
	close(ch)
}

// ========== close-of-closed-channel (GCL6002) ==========

// Bad: closing the same channel twice panics.
func BadDoubleClose() {
	ch := make(chan int)
	close(ch)
	close(ch) // want "close of already-closed channel 'ch'"
}

// Bad: a parameter's state is unknown on entry, but once this function has
// closed it, closing again is a definite double close.
func BadParamDoubleClose(ch chan int) {
	close(ch)
	close(ch) // want "close of already-closed channel 'ch'"
}

// Good: reassigning a fresh channel between closes is fine.
func GoodReassignAfterClose() {
	ch := make(chan int)
	close(ch)
	ch = make(chan int)
	close(ch)
}

// ========== send-on-closed-channel (GCL6003) ==========

// Bad: sending on a closed channel panics.
func BadSendOnClosed() {
	ch := make(chan int, 1)
	close(ch)
	ch <- 1 // want "send on closed channel 'ch'"
}

// Bad: a send on a closed channel panics even inside a select case.
func BadSelectSendOnClosed() {
	ch := make(chan int, 1)
	close(ch)
	select {
	case ch <- 1: // want "send on closed channel 'ch'"
	default:
	}
}

// Good: send before closing.
func GoodSendThenClose() {
	ch := make(chan int, 1)
	ch <- 1
	close(ch)
}

// ========== nil-channel-op (GCL6004) ==========

// Bad: sending on a nil channel blocks forever.
func BadSendOnNil() {
	var ch chan int
	ch <- 1 // want "send on nil channel 'ch'"
}

// Bad: an explicit nil assignment is tracked like the zero value.
func BadSendAfterNilAssign() {
	ch := make(chan int, 1)
	ch = nil
	ch <- 1 // want "send on nil channel 'ch'"
}

// Bad: receiving on a nil channel blocks forever.
func BadRecvOnNil() {
	var ch chan int
	<-ch // want "receive on nil channel 'ch'"
}

// Bad: ranging over a nil channel blocks forever.
func BadRangeOnNil() {
	var ch chan int
	for range ch { // want "receive on nil channel 'ch'"
	}
}

// Good: a made channel is fine to send and receive on.
func GoodSendRecv() {
	ch := make(chan int, 1)
	ch <- 1
	<-ch
}

// ========== false-positive guards ==========

// A close reached on only one branch is not a guaranteed double close.
func GuardConditionalClose(cond bool) {
	ch := make(chan int)
	if cond {
		close(ch)
	}
	close(ch)
}

// A nil channel inside a select is a deliberate way to disable a case, so
// neither the send nor the receive comm clause is flagged.
func GuardNilSelectComm() {
	var ch chan int
	select {
	case ch <- 1:
	default:
	}
	select {
	case <-ch:
	default:
	}
}

// A channel that escapes into a goroutine that may reassign it is not tracked,
// so the send is not flagged even though the outer flow left it nil.
func GuardClosureReassign() {
	var ch chan int
	go func() {
		ch = make(chan int, 1)
	}()
	ch <- 1
}

// A shadowing declaration must not taint the outer channel: the outer ch is
// open, so closing it once is fine and must not be read as close-of-nil.
func GuardShadowNoNilFP() {
	ch := make(chan int)
	{
		var ch chan int
		_ = ch
	}
	close(ch)
}

// A lone close of a parameter is not flagged: its incoming state is unknown.
func GuardParamSingleClose(ch chan int) {
	close(ch)
}

// A channel accessed through a struct field is not tracked (aliasing is
// unknown), so repeated closes are not flagged.
type hub struct{ ch chan int }

func GuardFieldChannel(h *hub) {
	close(h.ch)
	close(h.ch)
}

// Each loop iteration closes a different channel from the slice, so a single
// close per iteration is not a double close.
func GuardLoopSingleClose(chs []chan int) {
	for _, ch := range chs {
		close(ch)
	}
}

// A deferred close runs at return, so a send before it is on an open channel.
func GuardDeferClose() {
	ch := make(chan int, 1)
	defer close(ch)
	ch <- 1
}

// A channel variable assigned inside a select comm clause takes the received
// value, so a later receive or close on it must not be read as a nil-channel
// operation. Regression: net/http's transport_test.go uses exactly this shape.
func GuardSelectCommAssign(reqerrc chan error, putidlec chan chan struct{}) {
	var idlec chan struct{}
	select {
	case <-reqerrc:
		return
	case idlec = <-putidlec:
	}
	<-idlec
	close(idlec)
}

// An `if ch != nil` guard proves the channel is non-nil inside the branch, so
// the send is not a nil-channel operation. Regression: go-ethereum's
// chain_freezer.go and history_indexer.go guard channel ops this way.
func GuardIfNotNilThenSend(trigger chan chan struct{}) {
	var triggered chan struct{}
	if triggered != nil {
		triggered <- struct{}{}
		triggered = nil
	}
	select {
	case triggered = <-trigger:
	default:
	}
}

// An `if ch == nil { return }` guard means the code after it runs only when the
// channel is non-nil, so the later close is not close-of-nil. Regression:
// consul's leader.go closes weAreLeaderCh exactly like this.
func GuardIfNilReturnThenClose(made bool) {
	var ch chan struct{}
	if made {
		ch = make(chan struct{})
	}
	if ch == nil {
		return
	}
	close(ch)
}

// The consul leader-loop shape: a lazily-made channel closed in a different
// branch, guarded by a nil check, all inside a for/select.
func GuardLeaderLoopShape(notify <-chan bool, quit <-chan struct{}) {
	var leaderCh chan struct{}
	for {
		select {
		case isLeader := <-notify:
			if isLeader {
				if leaderCh != nil {
					continue
				}
				leaderCh = make(chan struct{})
			} else {
				if leaderCh == nil {
					continue
				}
				close(leaderCh)
				leaderCh = nil
			}
		case <-quit:
			return
		}
	}
}

// An inline ignore directive silences the diagnostic on its line.
func GuardIgnoreDirective() {
	var ch chan int
	close(ch) // goconcurrencylint:ignore GCL6001
}
