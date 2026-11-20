package mutex

import (
	"go/ast"
	"go/token"
	"slices"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// A goroutine can take a lock for the function that started it and tell it so
// over a channel; the function waits for the signal and then owns the lock:
//
//	go func() {
//		mu.Lock()
//		close(locked)      // or `locked <- true`, or a deferred close
//		...
//	}()
//	<-locked
//	...
//	mu.Unlock()
//
// A sync mutex is not tied to the goroutine that locked it, so this is a plain
// hand-off. Read one side at a time, the goroutine leaks the lock and the
// function releases one it never took. What ties them together is the
// channel: the goroutine signals on it after taking the lock, and the function
// receives from it before releasing the same lock.

// goroutineHandoff is a lock a goroutine literal hands to the function that
// started it.
type goroutineHandoff struct {
	lit      *ast.FuncLit
	lock     string
	acquires []token.Pos
	release  []string
	received []token.Pos
}

// goroutineHandoffs returns the hand-offs of fn, computed once.
func (c *Checker) goroutineHandoffs() []goroutineHandoff {
	if c.funcAnalysis == nil || c.function == nil || c.function.Body == nil {
		return nil
	}
	if c.handoffs != nil {
		return *c.handoffs
	}
	found := findGoroutineHandoffs(c.function.Body)
	c.handoffs = &found
	return found
}

func findGoroutineHandoffs(body *ast.BlockStmt) []goroutineHandoff {
	var handoffs []goroutineHandoff
	ast.Inspect(body, func(n ast.Node) bool {
		stmt, ok := n.(*ast.GoStmt)
		if !ok {
			return true
		}
		lit, ok := common.UnwrapParenExpr(stmt.Call.Fun).(*ast.FuncLit)
		if !ok || lit.Body == nil {
			return true
		}
		signals := channelSignals(lit.Body)
		for _, pattern := range []LockPattern{WriteLockPattern, ReadLockPattern} {
			for lock, acquires := range acquisitionsIn(lit.Body, pattern.LockMethods) {
				for _, signal := range signals {
					if !signal.deferred && signal.pos < acquires[0] {
						continue
					}
					received := receivesOutside(body, lit, signal.channel)
					if len(received) == 0 || !releasedOutsideAfter(body, lit, lock, pattern.UnlockMethods, received[0]) {
						continue
					}
					handoffs = append(handoffs, goroutineHandoff{
						lit: lit, lock: lock, acquires: acquires, release: pattern.UnlockMethods, received: received,
					})
					break
				}
			}
		}
		return true
	})
	return handoffs
}

// channelSignal is a send on, or a close of, a channel.
type channelSignal struct {
	channel  string
	pos      token.Pos
	deferred bool
}

// channelSignals returns the signals body sends, outside nested literals.
func channelSignals(body *ast.BlockStmt) []channelSignal {
	var signals []channelSignal
	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.SendStmt:
			signals = append(signals, channelSignal{channel: common.GetVarName(node.Chan), pos: node.Pos()})
		case *ast.DeferStmt:
			if name, ok := closedChannel(node.Call); ok {
				signals = append(signals, channelSignal{channel: name, pos: node.Pos(), deferred: true})
			}
			return false
		case *ast.CallExpr:
			if name, ok := closedChannel(node); ok {
				signals = append(signals, channelSignal{channel: name, pos: node.Pos()})
			}
		}
		return true
	})
	return slices.DeleteFunc(signals, func(s channelSignal) bool { return s.channel == "?" })
}

// closedChannel reports whether call is close(ch), returning ch's spelling.
func closedChannel(call *ast.CallExpr) (string, bool) {
	ident, ok := common.UnwrapParenExpr(call.Fun).(*ast.Ident)
	if !ok || ident.Name != "close" || len(call.Args) != 1 {
		return "", false
	}
	return common.GetVarName(call.Args[0]), true
}

// acquisitionsIn maps each lock body acquires with one of methods, outside
// nested literals, to the positions of those calls.
func acquisitionsIn(body *ast.BlockStmt, methods []string) map[string][]token.Pos {
	acquired := make(map[string][]token.Pos)
	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.CallExpr:
			sel, ok := common.UnwrapParenExpr(node.Fun).(*ast.SelectorExpr)
			if !ok || !slices.Contains(methods, sel.Sel.Name) || len(node.Args) != 0 {
				return true
			}
			if name := common.GetVarName(sel.X); name != "?" {
				acquired[name] = append(acquired[name], node.Pos())
			}
		}
		return true
	})
	return acquired
}

// receivesOutside returns the positions of the receives from channel in body
// outside lit.
func receivesOutside(body *ast.BlockStmt, lit *ast.FuncLit, channel string) []token.Pos {
	var received []token.Pos
	ast.Inspect(body, func(n ast.Node) bool {
		if n == lit {
			return false
		}
		if unary, ok := n.(*ast.UnaryExpr); ok && unary.Op == token.ARROW && common.GetVarName(unary.X) == channel {
			received = append(received, unary.Pos())
		}
		return true
	})
	return received
}

// releasedOutsideAfter reports whether body releases lock after pos, outside
// lit.
func releasedOutsideAfter(body *ast.BlockStmt, lit *ast.FuncLit, lock string, methods []string, pos token.Pos) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found || n == lit {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok || call.Pos() <= pos {
			return true
		}
		sel, ok := common.UnwrapParenExpr(call.Fun).(*ast.SelectorExpr)
		if ok && slices.Contains(methods, sel.Sel.Name) && common.GetVarName(sel.X) == lock {
			found = true
		}
		return !found
	})
	return found
}

// dropHandedOffAcquisitions removes from a goroutine's final state the
// acquisitions it hands to the function that started it.
func (c *Checker) dropHandedOffAcquisitions(lit *ast.FuncLit, stats map[string]*Stats) {
	for _, handoff := range c.goroutineHandoffs() {
		if handoff.lit != lit {
			continue
		}
		st := stats[handoff.lock]
		if st == nil {
			continue
		}
		for _, pos := range handoff.acquires {
			if i := slices.Index(st.lockPos, pos); i >= 0 && st.lock > 0 {
				st.lock--
				st.lockPos = slices.Delete(st.lockPos, i, i+1)
			}
			if i := slices.Index(st.rlockPos, pos); i >= 0 && st.rlock > 0 {
				st.rlock--
				st.rlockPos = slices.Delete(st.rlockPos, i, i+1)
			}
		}
	}
}

// releasesHandedOffLock reports whether the release at pos gives back a lock
// a goroutine handed over, after the signal that it holds it.
func (c *Checker) releasesHandedOffLock(varName string, pos token.Pos, method string) bool {
	for _, handoff := range c.goroutineHandoffs() {
		if handoff.lock != varName || !slices.Contains(handoff.release, method) {
			continue
		}
		if pos < handoff.lit.Pos() || pos >= handoff.lit.End() {
			if handoff.received[0] < pos {
				return true
			}
		}
	}
	return false
}
