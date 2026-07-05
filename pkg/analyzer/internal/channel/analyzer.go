package channel

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/report"
)

// checker runs the channel state machine over one function scope (a FuncDecl or
// FuncLit body). It tracks only channel variables declared inside that scope;
// variables from an enclosing scope, parameters and package-scope channels are
// left Unknown and never reported.
type checker struct {
	ec       report.Reporter
	info     *types.Info
	poisoned map[types.Object]bool
}

func newChecker(ec report.Reporter, info *types.Info) *checker {
	return &checker{ec: ec, info: info}
}

// analyzeBody walks body once to find variables that must not be tracked
// precisely (poisonedVars), then interprets the body from an empty state.
func (c *checker) analyzeBody(body *ast.BlockStmt) {
	c.poisoned = poisonedVars(body, c.info)
	c.evalBlock(body.List, state{})
}

// evalBlock threads the state sequentially through the statements of a block.
func (c *checker) evalBlock(stmts []ast.Stmt, s state) state {
	for _, stmt := range stmts {
		s = c.evalStmt(stmt, s)
	}
	return s
}

// evalStmt applies one statement to the incoming state and returns the outgoing
// state. Statements the analysis does not model (go/defer bodies, returns,
// increments) pass the state through unchanged.
func (c *checker) evalStmt(stmt ast.Stmt, s state) state {
	switch n := stmt.(type) {
	case *ast.DeclStmt:
		c.evalDecl(n, s)
	case *ast.AssignStmt:
		return c.evalAssign(n, s)
	case *ast.SendStmt:
		return c.evalSend(n, s, false)
	case *ast.ExprStmt:
		return c.evalExprStmt(n, s)
	case *ast.BlockStmt:
		return c.evalBlock(n.List, s)
	case *ast.IfStmt:
		return c.evalIf(n, s)
	case *ast.ForStmt:
		return c.evalFor(n, s)
	case *ast.RangeStmt:
		return c.evalRange(n, s)
	case *ast.SwitchStmt:
		return c.evalSwitch(n, s)
	case *ast.TypeSwitchStmt:
		if n.Init != nil {
			s = c.evalStmt(n.Init, s)
		}
		return c.evalCaseBodies(n.Body, s)
	case *ast.SelectStmt:
		return c.evalSelect(n, s)
	case *ast.LabeledStmt:
		return c.evalStmt(n.Stmt, s)
	}
	return s
}

// evalDecl handles `var ch chan T` declarations: a var with no initializer is a
// nil channel; one initialized from make is Open; anything else is Unknown.
func (c *checker) evalDecl(n *ast.DeclStmt, s state) {
	gd, ok := n.Decl.(*ast.GenDecl)
	if !ok || gd.Tok != token.VAR {
		return
	}
	for _, spec := range gd.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for i, nameIdent := range vs.Names {
			obj := c.info.Defs[nameIdent]
			if obj == nil || !common.IsChannel(obj.Type()) || c.poisoned[obj] {
				continue
			}
			switch {
			case len(vs.Values) == 0:
				s[obj] = Nil // zero value of a channel type is nil
			case len(vs.Values) == len(vs.Names):
				s[obj] = c.rhsState(vs.Values[i])
			default:
				s[obj] = Unknown // multi-value initializer we cannot split
			}
		}
	}
}

// evalAssign checks any receive on the right-hand side for a nil-channel
// operation and updates the state of channel-typed left-hand variables.
func (c *checker) evalAssign(n *ast.AssignStmt, s state) state {
	for _, rhs := range n.Rhs {
		c.checkReceiveExpr(rhs, s, false)
	}
	return c.applyAssignLHS(n, s)
}

// applyAssignLHS updates the state of channel-typed left-hand variables from
// the assignment's right-hand side: make(chan ...) is Open, nil is Nil, and
// anything else — including a value received from a channel — is Unknown. It is
// shared by ordinary assignments and select comm clauses so that a variable
// assigned inside `case v = <-ch:` is no longer treated as its old state.
func (c *checker) applyAssignLHS(n *ast.AssignStmt, s state) state {
	aligned := len(n.Lhs) == len(n.Rhs)
	for i, lhs := range n.Lhs {
		ident, ok := lhs.(*ast.Ident)
		if !ok || ident.Name == "_" {
			continue
		}
		obj := c.info.ObjectOf(ident)
		if obj == nil || !common.IsChannel(obj.Type()) || c.poisoned[obj] {
			continue
		}
		if aligned {
			s[obj] = c.rhsState(n.Rhs[i])
		} else {
			s[obj] = Unknown
		}
	}
	return s
}

// rhsState classifies the value assigned to a channel variable: make(chan ...)
// yields Open, an explicit nil yields Nil, and everything else is Unknown.
func (c *checker) rhsState(rhs ast.Expr) chanState {
	switch e := common.UnwrapParenExpr(rhs).(type) {
	case *ast.CallExpr:
		if ident, ok := e.Fun.(*ast.Ident); ok && ident.Name == "make" {
			if _, isBuiltin := c.info.ObjectOf(ident).(*types.Builtin); isBuiltin {
				return Open
			}
		}
	case *ast.Ident:
		if e.Name == "nil" {
			return Nil
		}
	}
	return Unknown
}

// evalExprStmt handles the two channel operations that appear as bare
// statements: close(ch) and a discarded receive <-ch.
func (c *checker) evalExprStmt(n *ast.ExprStmt, s state) state {
	switch e := common.UnwrapParenExpr(n.X).(type) {
	case *ast.CallExpr:
		if c.isCloseCall(e) {
			return c.evalClose(e, s)
		}
	case *ast.UnaryExpr:
		if e.Op == token.ARROW {
			c.checkReceive(e.X, s, false)
		}
	}
	return s
}

// isCloseCall reports whether call is the builtin close(x) with a single
// argument, guarding against a same-named user function.
func (c *checker) isCloseCall(call *ast.CallExpr) bool {
	ident, ok := call.Fun.(*ast.Ident)
	if !ok || ident.Name != "close" || len(call.Args) != 1 {
		return false
	}
	_, isBuiltin := c.info.ObjectOf(ident).(*types.Builtin)
	return isBuiltin
}

// evalClose reports close() on a nil (GCL6001) or already-closed (GCL6002)
// channel and transitions the variable to Closed.
func (c *checker) evalClose(call *ast.CallExpr, s state) state {
	obj, name, ok := c.chanObject(call.Args[0])
	if !ok {
		return s
	}
	switch s.get(obj) {
	case Nil:
		c.ec.AddError(call.Pos(), category.CloseOfNilChannel,
			"close of nil channel '"+name+"' panics at runtime")
	case Closed:
		c.ec.AddError(call.Pos(), category.CloseOfClosedChannel,
			"close of already-closed channel '"+name+"' panics at runtime")
	}
	if !c.poisoned[obj] {
		s[obj] = Closed
	}
	return s
}

// evalSend reports a send on a closed (GCL6003) or nil (GCL6004) channel. A nil
// send inside a select comm clause is a deliberate way to disable a case and is
// not reported, but a send on a closed channel panics even there.
func (c *checker) evalSend(n *ast.SendStmt, s state, inSelectComm bool) state {
	obj, name, ok := c.chanObject(n.Chan)
	if !ok {
		return s
	}
	switch s.get(obj) {
	case Closed:
		c.ec.AddError(n.Pos(), category.SendOnClosedChannel,
			"send on closed channel '"+name+"' panics at runtime")
	case Nil:
		if !inSelectComm {
			c.ec.AddError(n.Pos(), category.NilChannelOperation,
				"send on nil channel '"+name+"' blocks forever")
		}
	}
	return s
}

// checkReceiveExpr reports a nil-channel receive when expr is a top-level
// receive (v := <-ch). Receives nested deeper in an expression are not tracked.
func (c *checker) checkReceiveExpr(expr ast.Expr, s state, inSelectComm bool) {
	if u, ok := common.UnwrapParenExpr(expr).(*ast.UnaryExpr); ok && u.Op == token.ARROW {
		c.checkReceive(u.X, s, inSelectComm)
	}
}

// checkReceive reports a receive on a nil channel (GCL6004), unless it is a
// select comm clause, where a nil channel deliberately disables the case.
func (c *checker) checkReceive(chanExpr ast.Expr, s state, inSelectComm bool) {
	obj, name, ok := c.chanObject(chanExpr)
	if !ok || inSelectComm {
		return
	}
	if s.get(obj) == Nil {
		c.ec.AddError(chanExpr.Pos(), category.NilChannelOperation,
			"receive on nil channel '"+name+"' blocks forever")
	}
}

// evalIf evaluates both arms from a clone of the incoming state and merges the
// results, so a variable only stays concrete when both arms agree.
//
// It also applies nil-guard narrowing: `if ch != nil { ... }` proves ch is not
// nil inside the then-branch, and `if ch == nil { ... }` proves it inside the
// else-branch (and the fall-through when the then-branch exits). Narrowing only
// ever clears a Nil state to Unknown — it never introduces Nil — so it can only
// suppress reports, never create them. This models the ubiquitous
// lazily-initialised channel guarded by an explicit nil check.
func (c *checker) evalIf(n *ast.IfStmt, s state) state {
	if n.Init != nil {
		s = c.evalStmt(n.Init, s)
	}

	thenClear := map[types.Object]bool{}
	elseClear := map[types.Object]bool{}
	c.nonNilInThen(n.Cond, thenClear)
	c.nonNilInElse(n.Cond, elseClear)

	thenIn := s.clone()
	clearNil(thenIn, thenClear)
	thenS := c.evalBlock(n.Body.List, thenIn)

	elseIn := s.clone()
	clearNil(elseIn, elseClear)
	var elseS state
	if n.Else != nil {
		elseS = c.evalStmt(n.Else, elseIn)
	} else {
		elseS = elseIn
	}

	// When one arm cannot fall through (it returns/continues/breaks/panics),
	// the state after the if is only the other arm's — carrying that arm's
	// nil-guard narrowing forward. This is what makes `if ch == nil { return }`
	// followed by an operation on ch safe: the continuation runs only when ch
	// is non-nil.
	if blockTerminates(n.Body) {
		return elseS
	}
	if elseTerminates(n.Else) {
		return thenS
	}
	return merge(thenS, elseS)
}

// nonNilInThen collects channel objects guaranteed non-nil when cond is true
// (the then-branch): a bare `ch != nil`, and every such guard in an && chain.
func (c *checker) nonNilInThen(cond ast.Expr, out map[types.Object]bool) {
	be, ok := common.UnwrapParenExpr(cond).(*ast.BinaryExpr)
	if !ok {
		return
	}
	switch be.Op {
	case token.NEQ:
		if obj := c.nilCompObject(be.X, be.Y); obj != nil {
			out[obj] = true
		}
	case token.LAND:
		c.nonNilInThen(be.X, out)
		c.nonNilInThen(be.Y, out)
	}
}

// nonNilInElse collects channel objects guaranteed non-nil when cond is false
// (the else-branch): a bare `ch == nil`, and every such guard in an || chain.
func (c *checker) nonNilInElse(cond ast.Expr, out map[types.Object]bool) {
	be, ok := common.UnwrapParenExpr(cond).(*ast.BinaryExpr)
	if !ok {
		return
	}
	switch be.Op {
	case token.EQL:
		if obj := c.nilCompObject(be.X, be.Y); obj != nil {
			out[obj] = true
		}
	case token.LOR:
		c.nonNilInElse(be.X, out)
		c.nonNilInElse(be.Y, out)
	}
}

// nilCompObject returns the channel variable object compared against nil in
// `ch == nil` / `ch != nil` (either operand order), or nil when the expression
// is not a nil comparison of a tracked channel identifier.
func (c *checker) nilCompObject(x, y ast.Expr) types.Object {
	if isNilIdent(x) {
		if obj, _, ok := c.chanObject(y); ok {
			return obj
		}
	}
	if isNilIdent(y) {
		if obj, _, ok := c.chanObject(x); ok {
			return obj
		}
	}
	return nil
}

// isNilIdent reports whether expr is the predeclared nil identifier.
func isNilIdent(expr ast.Expr) bool {
	ident, ok := common.UnwrapParenExpr(expr).(*ast.Ident)
	return ok && ident.Name == "nil"
}

// clearNil lifts the listed objects from Nil to Unknown, used to apply
// nil-guard narrowing. Non-nil states are left untouched.
func clearNil(s state, objs map[types.Object]bool) {
	for obj := range objs {
		if s.get(obj) == Nil {
			s[obj] = Unknown
		}
	}
}

// blockTerminates reports whether b's straight-line flow cannot fall through to
// the following statement because its last statement transfers control away
// (return, continue, break, goto, or panic).
func blockTerminates(b *ast.BlockStmt) bool {
	if b == nil || len(b.List) == 0 {
		return false
	}
	return stmtTerminates(b.List[len(b.List)-1])
}

// elseTerminates reports whether an if statement's else arm cannot fall
// through. An `else if` terminates only when both of its own arms do.
func elseTerminates(els ast.Stmt) bool {
	switch e := els.(type) {
	case *ast.BlockStmt:
		return blockTerminates(e)
	case *ast.IfStmt:
		return blockTerminates(e.Body) && e.Else != nil && elseTerminates(e.Else)
	}
	return false
}

// stmtTerminates reports whether stmt transfers control away from the current
// straight-line path.
func stmtTerminates(stmt ast.Stmt) bool {
	switch s := stmt.(type) {
	case *ast.ReturnStmt:
		return true
	case *ast.BranchStmt:
		return s.Tok == token.CONTINUE || s.Tok == token.BREAK || s.Tok == token.GOTO
	case *ast.ExprStmt:
		if call, ok := s.X.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "panic" {
				return true
			}
		}
	}
	return false
}

// evalFor evaluates the loop body once from the entry state (reporting any
// operation that is already bad on entry) and merges with the entry state to
// model zero iterations. It does not carry body-end state into a next iteration,
// so a cross-iteration re-close is missed rather than risking a false positive.
func (c *checker) evalFor(n *ast.ForStmt, s state) state {
	if n.Init != nil {
		s = c.evalStmt(n.Init, s)
	}
	bodyS := c.evalBlock(n.Body.List, s.clone())
	return merge(s, bodyS)
}

// evalRange treats `range ch` as a receive (a range over a nil channel blocks
// forever) and evaluates the body like a for loop.
func (c *checker) evalRange(n *ast.RangeStmt, s state) state {
	if common.IsChannel(c.info.TypeOf(n.X)) {
		c.checkReceive(n.X, s, false)
	}
	// A channel-typed range variable takes a value of unknown state each
	// iteration, so it must not keep any prior (e.g. nil) state.
	c.markUnknown(n.Key, s)
	c.markUnknown(n.Value, s)
	bodyS := c.evalBlock(n.Body.List, s.clone())
	return merge(s, bodyS)
}

// markUnknown sets a channel-typed identifier's state to Unknown, used where a
// variable is bound to a value the analysis cannot follow (a range variable).
func (c *checker) markUnknown(expr ast.Expr, s state) {
	ident, ok := expr.(*ast.Ident)
	if !ok || ident.Name == "_" {
		return
	}
	obj := c.info.ObjectOf(ident)
	if obj == nil || !common.IsChannel(obj.Type()) || c.poisoned[obj] {
		return
	}
	s[obj] = Unknown
}

// evalSwitch evaluates the init and merges every case body.
func (c *checker) evalSwitch(n *ast.SwitchStmt, s state) state {
	if n.Init != nil {
		s = c.evalStmt(n.Init, s)
	}
	return c.evalCaseBodies(n.Body, s)
}

// evalCaseBodies merges the states of all case bodies. When there is no default
// clause the switch may match nothing, so the entry state is merged in too.
func (c *checker) evalCaseBodies(body *ast.BlockStmt, s state) state {
	var acc state
	hasDefault := false
	for _, clause := range body.List {
		cc, ok := clause.(*ast.CaseClause)
		if !ok {
			continue
		}
		if cc.List == nil {
			hasDefault = true
		}
		branch := c.evalBlock(cc.Body, s.clone())
		if acc == nil {
			acc = branch
		} else {
			acc = merge(acc, branch)
		}
	}
	if acc == nil {
		return s
	}
	if !hasDefault {
		acc = merge(acc, s)
	}
	return acc
}

// evalSelect evaluates each comm clause (suppressing nil-channel reports on the
// communication itself) and its body, then merges all branches.
func (c *checker) evalSelect(n *ast.SelectStmt, s state) state {
	var acc state
	for _, clause := range n.Body.List {
		cc, ok := clause.(*ast.CommClause)
		if !ok {
			continue
		}
		branch := s.clone()
		if cc.Comm != nil {
			branch = c.evalComm(cc.Comm, branch)
		}
		branch = c.evalBlock(cc.Body, branch)
		if acc == nil {
			acc = branch
		} else {
			acc = merge(acc, branch)
		}
	}
	if acc == nil {
		return s
	}
	return acc
}

// evalComm evaluates the communication operation of a select case. A nil send
// or receive here is legitimate (it disables the case), so nil reports are
// suppressed; a send on a closed channel still panics and is reported.
func (c *checker) evalComm(comm ast.Stmt, s state) state {
	switch cm := comm.(type) {
	case *ast.SendStmt:
		return c.evalSend(cm, s, true)
	case *ast.ExprStmt:
		if u, ok := common.UnwrapParenExpr(cm.X).(*ast.UnaryExpr); ok && u.Op == token.ARROW {
			c.checkReceive(u.X, s, true)
		}
	case *ast.AssignStmt:
		for _, rhs := range cm.Rhs {
			c.checkReceiveExpr(rhs, s, true)
		}
		return c.applyAssignLHS(cm, s)
	}
	return s
}

// chanObject resolves a channel expression to its variable object and name when
// it is a plain identifier. Selectors, index expressions and other shapes are
// not tracked, so they resolve to Unknown and are never reported.
func (c *checker) chanObject(expr ast.Expr) (types.Object, string, bool) {
	ident, ok := common.UnwrapParenExpr(expr).(*ast.Ident)
	if !ok {
		return nil, "", false
	}
	obj := c.info.ObjectOf(ident)
	if obj == nil {
		return nil, "", false
	}
	return obj, ident.Name, true
}

// poisonedVars returns the channel variables in body that the analysis must not
// track precisely: those whose address is taken (a callee may reassign or make
// them through the pointer) and those assigned inside a nested function literal
// (a closure or goroutine may reassign them, racing with the outer flow).
func poisonedVars(body *ast.BlockStmt, info *types.Info) map[types.Object]bool {
	poisoned := map[types.Object]bool{}

	var walk func(n ast.Node, inFuncLit bool)
	walk = func(n ast.Node, inFuncLit bool) {
		ast.Inspect(n, func(node ast.Node) bool {
			switch e := node.(type) {
			case *ast.FuncLit:
				// Descend into the literal with the flag set; the manual
				// recursion handles the subtree, so stop this Inspect branch.
				walk(e.Body, true)
				return false
			case *ast.UnaryExpr:
				if e.Op == token.AND {
					markChannelObject(poisoned, info, e.X)
				}
			case *ast.AssignStmt:
				if inFuncLit && e.Tok == token.ASSIGN {
					for _, lhs := range e.Lhs {
						markChannelObject(poisoned, info, lhs)
					}
				}
			}
			return true
		})
	}
	walk(body, false)
	return poisoned
}

// markChannelObject adds expr's object to set when expr is a plain identifier
// bound to a channel variable.
func markChannelObject(set map[types.Object]bool, info *types.Info, expr ast.Expr) {
	ident, ok := common.UnwrapParenExpr(expr).(*ast.Ident)
	if !ok {
		return
	}
	if obj := info.ObjectOf(ident); obj != nil && common.IsChannel(obj.Type()) {
		set[obj] = true
	}
}
