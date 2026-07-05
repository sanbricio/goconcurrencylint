// Package channel detects misuse of Go channels: closing a nil or
// already-closed channel, sending on a closed channel, and sending or
// receiving on a nil channel.
//
// The analysis is a small abstract interpretation over one function scope. It
// tracks the state of each channel variable declared in that scope and reports
// only when a variable is provably in a bad concrete state (Nil or Closed) on
// every path reaching an operation. Any disagreement between converging paths
// collapses to Unknown, which is never reported, so the checks do not fire on
// speculative bugs.
package channel

import (
	"go/types"
	"maps"
)

// chanState is the abstract state of a tracked channel variable at a program
// point. States form a flat lattice whose top is Unknown: merging two distinct
// states yields Unknown.
type chanState int

const (
	// Unknown is the top of the lattice: the variable may hold any value, so no
	// diagnostic is emitted for it. It is the state of anything the analysis
	// cannot follow precisely — parameters, package-scope channels, struct
	// fields, values returned from calls, and variables that escape into a
	// closure or have their address taken.
	Unknown chanState = iota
	// Nil means the variable is a nil channel: declared with `var ch chan T`
	// (or explicitly assigned nil) and never assigned a channel from make on
	// this path.
	Nil
	// Open means the variable holds a channel from make that has not been
	// closed on this path.
	Open
	// Closed means close() has run on the variable on every path to this point.
	// Closed is terminal: nothing reopens a closed channel, so the only way out
	// is reassigning the variable, which the analysis observes locally.
	Closed
)

// state maps a tracked channel variable (by its types.Object, so shadowed
// declarations stay distinct) to its abstract state. A variable absent from the
// map is Unknown.
type state map[types.Object]chanState

// get returns the abstract state of obj, defaulting to Unknown.
func (s state) get(obj types.Object) chanState {
	if cs, ok := s[obj]; ok {
		return cs
	}
	return Unknown
}

// clone returns an independent copy so branches can be evaluated without
// mutating the state of the joining path.
func (s state) clone() state {
	out := make(state, len(s))
	maps.Copy(out, s)
	return out
}

// merge combines the states of two converging control-flow paths. A variable
// with the same state on both paths keeps it; any disagreement — including a
// variable tracked on only one path — becomes Unknown, so the analysis never
// reports a state that is not guaranteed on every path.
func merge(a, b state) state {
	out := make(state, len(a))
	for k, v := range a {
		if b.get(k) == v {
			out[k] = v
		} else {
			out[k] = Unknown
		}
	}
	for k := range b {
		if _, seen := a[k]; !seen {
			out[k] = Unknown
		}
	}
	return out
}
