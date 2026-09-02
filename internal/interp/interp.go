package interp

import (
	"fmt"
	"io"
	"math"

	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/bytecode"
	"github.com/scarypheonix/meta/internal/check"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/mono"
	"github.com/scarypheonix/meta/internal/prelude"
	"github.com/scarypheonix/meta/internal/resolve"
)

// TrapExitCode is the process exit status after a trap (spec/04-expressions.md).
const TrapExitCode = 101

// Trap is a runtime trap: an integer overflow, a bad index, an explicit panic. Traps are
// not catchable in Origin 0.1, so they propagate as a Go panic and are recovered only at
// the interpreter's boundary.
type Trap struct {
	Msg  string
	Span diag.Span
}

func (t *Trap) Error() string {
	return fmt.Sprintf("origin: %s at %s", t.Msg, t.Span)
}

// ctrlKind distinguishes normal completion from the three non-local exits.
type ctrlKind int

const (
	ctrlNone ctrlKind = iota
	ctrlBreak
	ctrlContinue
	ctrlReturn
)

// ctrl carries a non-local exit up the evaluation. Its zero value is normal completion,
// which keeps the common path allocation-free.
type ctrl struct {
	kind ctrlKind
	val  Value
}

var normal = ctrl{}

func (c ctrl) stops() bool { return c.kind != ctrlNone }

// frame is one function activation.
type frame struct {
	vars     map[*resolve.Local]Value
	captured map[*resolve.Local]Value
	self     Value
	hasSelf  bool
	// inst is the monomorphized instance whose body this frame is running, and is what
	// every call made from it is resolved against (ADR-0010). Without it the interpreter
	// would have to guess which impl a call on a type parameter reaches, and for a
	// numeric one -- `impl Loud for i64` beside `impl Loud for u8` -- there is nothing in
	// a Go int64 to guess from.
	inst *mono.Instance
	// callSpan is where this frame was called from, which is what lets a trap raised
	// inside a prelude method name the user's own line instead of the prelude's.
	callSpan diag.Span
}

func newFrame() *frame { return &frame{vars: map[*resolve.Local]Value{}} }

func (f *frame) lookup(l *resolve.Local) (Value, bool) {
	if v, ok := f.vars[l]; ok {
		return v, true
	}
	if f.captured != nil {
		v, ok := f.captured[l]
		return v, ok
	}
	return nil, false
}

// Interp evaluates a resolved program.
type Interp struct {
	res *resolve.Result
	// mo is the program's instantiation set, and rootInst finds the single instance of a
	// function that has no generic parameters of its own -- the entry point for a call
	// whose callee monomorphization did not have to choose between.
	mo       *mono.Result
	rootInst map[*ast.FnDecl]*mono.Instance
	// tys is the checker's own types, read for the one thing a runtime value cannot say:
	// which of the eight integer types an arithmetic operand is, and therefore at what
	// width the operation traps (spec/04-expressions.md). An Origin integer is a Go int64
	// here, so `255u8` and `255u32` are the same value and different programs.
	tys    *check.Result
	stdout io.Writer
	stderr io.Writer
	frames []*frame
	// depth guards against unbounded recursion, which the specification says traps as a
	// stack overflow rather than corrupting memory.
	depth    int
	maxDepth int

	// rt is the concurrency runtime, shared by every thread; tid identifies this thread,
	// main being 1 (spec/12-concurrency.md).
	rt  *runtime
	tid int64
	// taken holds the value `chan::await_value` dequeued, until the prelude's `recv`
	// reads it with `chan::taken_value`. It is per-thread, which is what makes the pair
	// atomic with respect to another receiver.
	taken Value
	// takenText is the same arrangement for `fs::read_file` (spec/15-files.md): the bytes
	// it read, held on this thread until the prelude's `read_to_string` takes them with
	// `fs::taken_text`.
	takenText string
}

// New returns an interpreter writing program output to stdout and trap messages to
// stderr. tys and mo are the check result and the instantiation set for the same program:
// the interpreter runs only programs that have been checked and monomorphized, and reads
// both for the questions a runtime value cannot answer -- which impl a call reaches, and at
// what width an arithmetic operation traps.
func New(res *resolve.Result, tys *check.Result, mo *mono.Result, stdout, stderr io.Writer) *Interp {
	in := &Interp{res: res, tys: tys, mo: mo, stdout: stdout, stderr: stderr, maxDepth: 8192, rt: newRuntime(), tid: 1}
	in.rootInst = map[*ast.FnDecl]*mono.Instance{}
	if mo != nil {
		for _, inst := range mo.Instances {
			if len(inst.Args) == 0 {
				in.rootInst[inst.Decl] = inst
			}
		}
	}
	return in
}

// Run calls `main` and returns the process exit status: 0 on success, 101 on a trap.
func (in *Interp) Run() (exitCode int) {
	main, ok := in.res.Fns["main"]
	if !ok {
		fmt.Fprintln(in.stderr, "origin: no `main` function")
		return 1
	}
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		// `dying` means another thread trapped first and this one was woken to unwind;
		// the trap to report is that one, not a second invented here (ADR-0026).
		if _, unwinding := r.(dying); unwinding {
			in.rt.mu.Lock()
			t := in.rt.trap
			in.rt.mu.Unlock()
			if t != nil {
				fmt.Fprintln(in.stderr, t.Error())
			}
			exitCode = TrapExitCode
			return
		}
		t, isTrap := r.(*Trap)
		if !isTrap {
			panic(r)
		}
		// Record it before reporting, so any thread parked on a channel or a lock wakes
		// and unwinds instead of running on past the end of the program.
		in.rt.mu.Lock()
		in.rt.fail(t)
		in.rt.live--
		in.rt.mu.Unlock()

		fmt.Fprintln(in.stderr, t.Error())
		exitCode = TrapExitCode
	}()
	in.callFunction(main, in.rootInst[main], nil, nil, false, main.Span())

	// `main` returning does not end the program while a spawned thread is still running
	// (spec/12-concurrency.md): a Go program's exit racing its goroutines is a common
	// bug, and "whichever happened first" is not an answer §04 permits.
	in.rt.mu.Lock()
	in.rt.live-- // main is no longer a thread that can make progress
	in.rt.cond.Broadcast()
	in.rt.mu.Unlock()
	in.rt.wg.Wait()

	// A thread that trapped while main was finishing still ends the process (ADR-0026).
	in.rt.mu.Lock()
	t := in.rt.trap
	in.rt.mu.Unlock()
	if t != nil {
		fmt.Fprintln(in.stderr, t.Error())
		return TrapExitCode
	}
	return 0
}

func (in *Interp) trap(span diag.Span, format string, args ...any) {
	panic(&Trap{Msg: fmt.Sprintf(format, args...), Span: in.userSpan(span)})
}

// userSpan resolves a span to the innermost location in the program's own source.
//
// The prelude's methods are written in terms of compiler-provided operations, so a trap
// raised by one of them -- "send on a closed channel" -- naturally carries a span inside
// the prelude, which tells a programmer nothing they can act on. Walking out to the
// nearest frame called from real source names the line they wrote instead. Traps raised
// directly by user code are unaffected, since their span is already outside the prelude.
func (in *Interp) userSpan(span diag.Span) diag.Span {
	if !isPreludeSpan(span) {
		return span
	}
	for i := len(in.frames) - 1; i >= 0; i-- {
		if s := in.frames[i].callSpan; s.Valid() && !isPreludeSpan(s) {
			return s
		}
	}
	return span
}

func isPreludeSpan(s diag.Span) bool {
	return s.Valid() && s.File != nil && s.File.Name == prelude.Name
}

func (in *Interp) frame() *frame { return in.frames[len(in.frames)-1] }

// callFunction evaluates a function or method body in a fresh frame, running as the
// given instance: `inst` is what the calls inside that body are resolved against.
func (in *Interp) callFunction(fn *ast.FnDecl, inst *mono.Instance, args []Value, recv Value, hasRecv bool, span diag.Span) Value {
	if fn.Body == nil {
		in.trap(span, "call to `%s`, which has no body", fn.Name.Name)
	}
	if in.depth >= in.maxDepth {
		in.trap(span, "stack overflow")
	}
	if len(args) != len(fn.Params) {
		in.trap(span, "`%s` takes %d argument(s) but %d were supplied", fn.Name.Name, len(fn.Params), len(args))
	}

	f := newFrame()
	f.callSpan = span
	f.inst = inst
	f.self, f.hasSelf = recv, hasRecv
	for i, p := range fn.Params {
		in.bindPattern(f, p.Pat, args[i], span)
	}

	in.frames = append(in.frames, f)
	in.depth++
	v, c := in.evalBlock(fn.Body)
	in.depth--
	in.frames = in.frames[:len(in.frames)-1]

	if c.kind == ctrlReturn {
		return c.val
	}
	return v
}

// callClosure applies a function value.
func (in *Interp) callClosure(cl *Closure, args []Value, span diag.Span) Value {
	if cl.Fn != nil {
		return in.callFunction(cl.Fn, cl.Inst, args, cl.Recv, cl.HasRecv, span)
	}
	lam := cl.Lambda
	if len(args) != len(lam.Params) {
		in.trap(span, "this lambda takes %d argument(s) but %d were supplied", len(lam.Params), len(args))
	}
	if in.depth >= in.maxDepth {
		in.trap(span, "stack overflow")
	}
	f := newFrame()
	f.callSpan = span
	f.captured = cl.Env
	// A lambda's body is compiled inside the instance that created it and has no
	// instance of its own, so calls it makes resolve against that one (ADR-0010).
	f.inst = cl.Inst
	for i, p := range lam.Params {
		in.bindPattern(f, p.Pat, args[i], span)
	}
	in.frames = append(in.frames, f)
	in.depth++
	v, c := in.evalExpr(lam.Body)
	in.depth--
	in.frames = in.frames[:len(in.frames)-1]
	if c.kind == ctrlReturn {
		return c.val
	}
	return v
}

// ---------------------------------------------------------------------------
// Blocks and statements
// ---------------------------------------------------------------------------

func (in *Interp) evalBlock(b *ast.Block) (Value, ctrl) {
	if b == nil {
		return Unit{}, normal
	}
	for _, s := range b.Stmts {
		if c := in.evalStmt(s); c.stops() {
			return Unit{}, c
		}
	}
	if b.Tail != nil {
		return in.evalExpr(b.Tail)
	}
	return Unit{}, normal
}

func (in *Interp) evalStmt(s ast.Stmt) ctrl {
	switch v := s.(type) {
	case *ast.LetStmt:
		val, c := in.evalExpr(v.Value)
		if c.stops() {
			return c
		}
		if !in.matchPattern(in.frame(), v.Pat, val) {
			in.trap(v.Span(), "refutable pattern in `let` did not match")
		}
		return normal
	case *ast.ExprStmt:
		_, c := in.evalExpr(v.X)
		return c
	case *ast.ItemStmt:
		return normal // items are declarations, evaluated by the resolver's collection
	}
	return normal
}

// ---------------------------------------------------------------------------
// Expressions
// ---------------------------------------------------------------------------

func (in *Interp) evalExpr(e ast.Expr) (Value, ctrl) {
	switch v := e.(type) {
	case nil:
		return Unit{}, normal

	case *ast.IntLit:
		if v.Overflow || v.Value > math.MaxInt64 {
			in.trap(v.Span(), "integer literal is out of range for `i64`")
		}
		return Int(int64(v.Value)), normal
	case *ast.FloatLit:
		return Float(v.Value), normal
	case *ast.StrLit:
		return &Str{S: v.Value}, normal
	case *ast.CharLit:
		return Char(v.Value), normal
	case *ast.BoolLit:
		return Bool(v.Value), normal

	case *ast.SelfExpr:
		f := in.frame()
		if !f.hasSelf {
			in.trap(v.Span(), "`self` used outside a method")
		}
		return f.self, normal

	case *ast.PathExpr:
		return in.evalPath(v)

	case *ast.StructLit:
		return in.evalStructLit(v)

	case *ast.TupleExpr:
		if len(v.Elems) == 0 {
			return Unit{}, normal
		}
		elems := make([]Value, 0, len(v.Elems))
		for _, el := range v.Elems {
			val, c := in.evalExpr(el)
			if c.stops() {
				return Unit{}, c
			}
			elems = append(elems, val)
		}
		return &Tuple{Elems: elems}, normal

	case *ast.Lambda:
		return in.makeClosure(v), normal

	case *ast.Block:
		return in.evalBlock(v)

	case *ast.If:
		cond, c := in.evalExpr(v.Cond)
		if c.stops() {
			return Unit{}, c
		}
		b, ok := cond.(Bool)
		if !ok {
			in.trap(v.Cond.Span(), "`if` condition must be a `bool`, found %s", TypeName(cond))
		}
		if bool(b) {
			return in.evalBlock(v.Then)
		}
		if v.Else != nil {
			return in.evalExpr(v.Else)
		}
		return Unit{}, normal

	case *ast.Match:
		return in.evalMatch(v)

	case *ast.While:
		for {
			cond, c := in.evalExpr(v.Cond)
			if c.stops() {
				return Unit{}, c
			}
			b, ok := cond.(Bool)
			if !ok {
				in.trap(v.Cond.Span(), "`while` condition must be a `bool`, found %s", TypeName(cond))
			}
			if !bool(b) {
				return Unit{}, normal
			}
			_, c = in.evalBlock(v.Body)
			switch c.kind {
			case ctrlBreak:
				return Unit{}, normal
			case ctrlReturn:
				return Unit{}, c
			}
		}

	case *ast.Loop:
		for {
			_, c := in.evalBlock(v.Body)
			switch c.kind {
			case ctrlBreak:
				if c.val == nil {
					return Unit{}, normal
				}
				return c.val, normal
			case ctrlReturn:
				return Unit{}, c
			}
		}

	case *ast.For:
		return in.evalFor(v)

	case *ast.Break:
		if v.Value == nil {
			return Unit{}, ctrl{kind: ctrlBreak}
		}
		val, c := in.evalExpr(v.Value)
		if c.stops() {
			return Unit{}, c
		}
		return Unit{}, ctrl{kind: ctrlBreak, val: val}

	case *ast.Continue:
		return Unit{}, ctrl{kind: ctrlContinue}

	case *ast.Return:
		if v.Value == nil {
			return Unit{}, ctrl{kind: ctrlReturn, val: Unit{}}
		}
		val, c := in.evalExpr(v.Value)
		if c.stops() {
			return Unit{}, c
		}
		return Unit{}, ctrl{kind: ctrlReturn, val: val}

	case *ast.Unary:
		return in.evalUnary(v)

	case *ast.Binary:
		return in.evalBinary(v)

	case *ast.Assign:
		return in.evalAssign(v)

	case *ast.Cast:
		return in.evalCast(v)

	case *ast.Call:
		return in.evalCall(v)

	case *ast.MethodCall:
		return in.evalMethodCall(v)

	case *ast.FieldAccess:
		recv, c := in.evalExpr(v.Recv)
		if c.stops() {
			return Unit{}, c
		}
		val, ok := fieldValue(recv, v.Name.Name)
		if !ok {
			in.trap(v.Span(), "no field `%s` on %s", v.Name.Name, TypeName(recv))
		}
		return val, normal

	case *ast.Try:
		return in.evalTry(v)

	case *ast.ErrorExpr:
		in.trap(v.Span(), "evaluating an expression the parser could not read")
	}
	panic(fmt.Sprintf("unimplemented: evaluating %T", e))
}

func fieldValue(recv Value, name string) (Value, bool) {
	switch r := recv.(type) {
	case *Struct:
		if i := r.FieldIndex(name); i >= 0 {
			return r.Vals[i], true
		}
	case *Enum:
		if i := r.FieldIndex(name); i >= 0 {
			return r.Vals[i], true
		}
	}
	return nil, false
}

func (in *Interp) evalPath(p *ast.PathExpr) (Value, ctrl) {
	ref, ok := in.res.Ref(p.NodeID())
	if !ok || ref.Kind == resolve.Unresolved {
		in.trap(p.Span(), "evaluating the unresolved name `%s`", p.Path)
	}
	switch ref.Kind {
	case resolve.LocalVar:
		v, found := in.frame().lookup(ref.Local)
		if !found {
			in.trap(p.Span(), "`%s` is not initialized here", ref.Local.Name)
		}
		return v, normal
	case resolve.Fn:
		// The instance is the one this *use site* reaches: `identity` mentioned where an
		// `i64` is wanted is `identity[i64]`, and monomorphization recorded which. A
		// function with no generic parameters has exactly one instance and no entry.
		inst, ok := in.mo.Lookup(in.frame().inst, p.NodeID())
		if !ok {
			inst = in.rootInst[ref.Fn]
		}
		return &Closure{Fn: ref.Fn, Inst: inst}, normal
	case resolve.Builtin:
		return &Builtin{Name: ref.Builtin}, normal
	case resolve.Variant:
		if ref.Variant.Kind != ast.UnitVariant {
			// A tuple variant used as a value is its constructor function.
			return &Enum{Def: ref.Enum, Variant: ref.Variant}, normal
		}
		return &Enum{Def: ref.Enum, Variant: ref.Variant}, normal
	case resolve.Const:
		val, c := in.evalExpr(ref.Const.Value)
		return val, c

	case resolve.PrimConst:
		return in.primConst(ref, p.Span()), normal
	}
	in.trap(p.Span(), "`%s` is not a value", p.Path)
	return Unit{}, normal
}

// primConstBits maps an integer type name to its width and signedness.
var primConstBits = map[string]struct {
	bits   uint
	signed bool
}{
	"i8": {8, true}, "i16": {16, true}, "i32": {32, true}, "i64": {64, true},
	"u8": {8, false}, "u16": {16, false}, "u32": {32, false}, "u64": {64, false},
}

// primConst evaluates `i64::MAX` and friends.
//
// Phase 1's value model carries every integer as an i64, so `u64::MAX` has no
// representation yet; it traps rather than returning a plausible wrong number. Real
// integer widths arrive with the bytecode VM (docs/deferred.md, Phase 3).
func (in *Interp) primConst(ref resolve.Ref, span diag.Span) Value {
	info, ok := primConstBits[ref.Name]
	if !ok {
		in.trap(span, "`%s` has no associated constant `%s`", ref.Name, ref.Member)
	}
	switch ref.Member {
	case "BITS":
		return Int(int64(info.bits))
	case "MIN":
		if !info.signed {
			return Int(0)
		}
		return Int(-(int64(1) << (info.bits - 1)))
	case "MAX":
		if info.signed {
			return Int(int64(1)<<(info.bits-1) - 1)
		}
		if info.bits >= 64 {
			in.trap(span, "unimplemented: `u64::MAX` needs real integer widths (Phase 3)")
		}
		return Int(int64(1)<<info.bits - 1)
	}
	in.trap(span, "`%s` has no associated constant `%s`", ref.Name, ref.Member)
	return Unit{}
}

func (in *Interp) evalStructLit(lit *ast.StructLit) (Value, ctrl) {
	ref, ok := in.res.Ref(lit.NodeID())
	if !ok || ref.Kind == resolve.Unresolved {
		in.trap(lit.Span(), "constructing the unresolved type `%s`", lit.Path)
	}

	// Field initializers are evaluated in source order, not declaration order
	// (spec/04-expressions.md).
	given := map[string]Value{}
	for _, f := range lit.Fields {
		val, c := in.evalExpr(f.Value)
		if c.stops() {
			return Unit{}, c
		}
		if _, dup := given[f.Name.Name]; dup {
			in.trap(f.Span(), "field `%s` is initialized twice", f.Name.Name)
		}
		given[f.Name.Name] = val
	}

	switch ref.Kind {
	case resolve.Struct:
		def := ref.Struct
		vals := make([]Value, len(def.Fields))
		for i, f := range def.Fields {
			v, ok := given[f.Name.Name]
			if !ok {
				in.trap(lit.Span(), "missing field `%s` in `%s`", f.Name.Name, def.Name.Name)
			}
			vals[i] = v
			delete(given, f.Name.Name)
		}
		for name := range given {
			in.trap(lit.Span(), "`%s` has no field `%s`", def.Name.Name, name)
		}
		return &Struct{Def: def, Vals: vals}, normal

	case resolve.Variant:
		va := ref.Variant
		if va.Kind != ast.StructVariant {
			in.trap(lit.Span(), "`%s` is not a struct variant", va.Name.Name)
		}
		vals := make([]Value, len(va.Fields))
		for i, f := range va.Fields {
			v, ok := given[f.Name.Name]
			if !ok {
				in.trap(lit.Span(), "missing field `%s` in `%s`", f.Name.Name, va.Name.Name)
			}
			vals[i] = v
			delete(given, f.Name.Name)
		}
		for name := range given {
			in.trap(lit.Span(), "`%s` has no field `%s`", va.Name.Name, name)
		}
		return &Enum{Def: ref.Enum, Variant: va, Vals: vals}, normal
	}
	in.trap(lit.Span(), "`%s` cannot be constructed with a struct literal", lit.Path)
	return Unit{}, normal
}

func (in *Interp) makeClosure(l *ast.Lambda) Value {
	env := map[*resolve.Local]Value{}
	f := in.frame()
	for _, local := range in.res.Captures[l.NodeID()] {
		if v, ok := f.lookup(local); ok {
			// Captured by value: a primitive is copied, an aggregate's reference is
			// copied so the object stays shared (spec/04-expressions.md).
			env[local] = v
		}
	}
	return &Closure{Lambda: l, Env: env, Inst: in.frame().inst}
}

func (in *Interp) evalMatch(m *ast.Match) (Value, ctrl) {
	scrut, c := in.evalExpr(m.Scrutinee)
	if c.stops() {
		return Unit{}, c
	}
	f := in.frame()
	for _, arm := range m.Arms {
		saved := saveVars(f)
		if !in.matchPattern(f, arm.Pat, scrut) {
			restoreVars(f, saved)
			continue
		}
		if arm.Guard != nil {
			g, gc := in.evalExpr(arm.Guard)
			if gc.stops() {
				return Unit{}, gc
			}
			b, ok := g.(Bool)
			if !ok {
				in.trap(arm.Guard.Span(), "a match guard must be a `bool`, found %s", TypeName(g))
			}
			if !bool(b) {
				restoreVars(f, saved)
				continue
			}
		}
		return in.evalExpr(arm.Body)
	}
	// Phase 2's exhaustiveness checker makes this unreachable at compile time; until it
	// exists, reaching it is a trap rather than a silent wrong answer.
	in.trap(m.Span(), "no match arm matched %s", Display(scrut))
	return Unit{}, normal
}

// saveVars and restoreVars undo the bindings a failed arm introduced. Patterns bind as
// they match, so a partial match must not leak its bindings into the next arm.
func saveVars(f *frame) map[*resolve.Local]Value {
	saved := make(map[*resolve.Local]Value, len(f.vars))
	for k, v := range f.vars {
		saved[k] = v
	}
	return saved
}

func restoreVars(f *frame, saved map[*resolve.Local]Value) {
	for k := range f.vars {
		if _, ok := saved[k]; !ok {
			delete(f.vars, k)
		}
	}
	for k, v := range saved {
		f.vars[k] = v
	}
}

// evalFor implements the desugaring in spec/04-expressions.md exactly.
func (in *Interp) evalFor(fo *ast.For) (Value, ctrl) {
	iterable, c := in.evalExpr(fo.Iter)
	if c.stops() {
		return Unit{}, c
	}
	// The desugaring's two calls have no nodes of their own, so both are keyed to the
	// `for` node in two tables (internal/check's forElementType explains why).
	cur := in.frame().inst
	intoIter, _ := in.mo.LookupIter(cur, fo.NodeID())
	nextFn, _ := in.mo.Lookup(cur, fo.NodeID())
	it := in.callResolved(intoIter, iterable, "into_iter", bytecode.KindUnknown, nil, fo.Iter.Span())
	f := in.frame()
	for {
		next := in.callResolved(nextFn, it, "next", bytecode.KindUnknown, nil, fo.Span())
		e, ok := next.(*Enum)
		if !ok || e.Def.Name.Name != "Option" {
			in.trap(fo.Span(), "`next` must return `Option`, found %s", TypeName(next))
		}
		if e.Variant.Name.Name == "None" {
			return Unit{}, normal
		}
		if len(e.Vals) != 1 {
			in.trap(fo.Span(), "`Some` must carry exactly one value")
		}
		if !in.matchPattern(f, fo.Pat, e.Vals[0]) {
			in.trap(fo.Span(), "refutable pattern in `for` did not match")
		}
		_, bc := in.evalBlock(fo.Body)
		switch bc.kind {
		case ctrlBreak:
			return Unit{}, normal
		case ctrlReturn:
			return Unit{}, bc
		}
	}
}

// evalTry implements `?` (spec/09-errors.md), on a `Result` or an `Option`.
//
// The early return hands back the value it was given rather than rebuilding it, which the
// other two engines cannot do: `Result[i64, E]::Err` and `Result[String, E]::Err` are two
// layouts there, and the enclosing function's own is the one that has to come back. Here
// an enum value is a pointer to the declaration plus a payload, and the declaration is the
// same one for every instantiation, so the distinction does not exist.
func (in *Interp) evalTry(t *ast.Try) (Value, ctrl) {
	val, c := in.evalExpr(t.X)
	if c.stops() {
		return Unit{}, c
	}
	e, ok := val.(*Enum)
	if !ok || (e.Def.Name.Name != "Result" && e.Def.Name.Name != "Option") {
		in.trap(t.Span(), "`?` applies to a `Result` or an `Option`, found %s", TypeName(val))
	}
	switch e.Variant.Name.Name {
	case "Ok", "Some":
		if len(e.Vals) != 1 {
			in.trap(t.Span(), "`%s` must carry exactly one value", e.Variant.Name.Name)
		}
		return e.Vals[0], normal
	case "Err", "None":
		return Unit{}, ctrl{kind: ctrlReturn, val: e}
	}
	in.trap(t.Span(), "`?` applies to `Ok`, `Err`, `Some` or `None`, found `%s`", e.Variant.Name.Name)
	return Unit{}, normal
}
