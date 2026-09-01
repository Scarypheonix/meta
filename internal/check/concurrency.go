package check

import (
	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/prelude"
	"github.com/scarypheonix/meta/internal/resolve"
	"github.com/scarypheonix/meta/internal/types"
)

// The signatures of the compiler-provided concurrency operations (spec/12-concurrency.md).
//
// ADR-0025 put the surface in the prelude rather than in the grammar, so `spawn` is an
// ordinary generic function and `JoinHandle[T]` an ordinary generic struct. What cannot
// be written in Origin is the *body*: starting a thread, parking on a queue, taking a
// lock. Those are the functions here, and the prelude's methods are written in terms of
// them — the same arrangement `Show` already uses, where the trait is Origin and the impl
// is the compiler's.
//
// Every one takes the handle struct itself rather than the raw handle inside it, so a
// runtime that receives a value it does not recognize can trap rather than dereference
// whatever integer it was handed (the prelude records the same, for the same reason).

// preludeDef finds a prelude type by name. The prelude declares one type per name, so
// there is nothing to disambiguate; the result is cached because this runs per call site.
func (c *Checker) preludeDef(name string) *types.Def {
	if c.preludeDefs == nil {
		c.preludeDefs = map[string]*types.Def{}
		for _, def := range c.defs {
			if def != nil && DeclaredInPrelude(def) {
				c.preludeDefs[def.Name] = def
			}
		}
	}
	return c.preludeDefs[name]
}

// DeclaredInPrelude reports whether a definition is the prelude's own.
//
// A program may declare a type with a name the prelude also uses -- `enum List` is an
// ordinary thing to write, and spec/07-modules.md makes the prelude the last resolution
// step, so the program's own name wins in the program's own code. What must not happen is
// the reverse: a builtin's signature naming `List` and getting the *program's*, which is
// how `map::new` briefly came to return a list of somebody else's enum.
func DeclaredInPrelude(def *types.Def) bool {
	var span diag.Span
	switch {
	case def.Struct != nil:
		span = def.Struct.Span()
	case def.Enum != nil:
		span = def.Enum.Span()
	default:
		return false
	}
	return span.Valid() && span.File != nil && span.File.Name == prelude.Name
}

// named builds `Name[args...]` for a prelude type, or the error type when the prelude
// does not declare it — which is a broken prelude, reported elsewhere as a parse failure.
func (c *Checker) named(name string, args ...types.Type) types.Type {
	def := c.preludeDef(name)
	if def == nil {
		return types.Error
	}
	return &types.Named{Def: def, Args: args}
}

// concurrencyBuiltinType gives the operations in std::thread, std::chan and std::sync
// their signatures. It returns nil for a name it does not own, so builtinType can carry
// on with the rest.
func (c *Checker) concurrencyBuiltinType(name string, targs []ast.Type, span diag.Span) types.Type {
	unit := types.Unit()
	i64 := types.P(types.I64)

	// Every one of these is generic in a single element type, so an explicit type
	// argument -- `channel[i64](0)`, as spec/12-concurrency.md writes it -- names that
	// type directly. Without this the type would have to be inferred from a later use,
	// and a channel created and never used would have no type at all.
	elem := func() types.Type {
		if len(targs) == 1 {
			return c.toType(targs[0])
		}
		if len(targs) > 1 {
			c.bag.Errorf("E0107", span,
				"`%s` takes 1 type argument but %d were supplied", name, len(targs)).
				Label("wrong number of type arguments")
		}
		return c.freshFor(span)
	}

	switch name {
	case "thread::spawn":
		// spawn[T: Send](body: fn() -> T) -> JoinHandle[T]
		//
		// The `Send` bound is imposed at the call site rather than carried here, because
		// a builtin's type is a plain FnT with nowhere to put a bound. checkConcurrencyCall
		// does it, and also checks the closure's captures, which no type can express.
		//
		// The operation itself yields a bare handle; internal/compile wraps it in the
		// `JoinHandle[T]` this signature promises, using the instantiation the checker
		// recorded here. The runtime never constructs a prelude type.
		t := elem()
		return &types.FnT{
			Params: []types.Type{&types.FnT{Params: nil, Ret: t}},
			Ret:    c.named("JoinHandle", t),
		}
	case "thread::join_thread":
		t := elem()
		return &types.FnT{Params: []types.Type{c.named("JoinHandle", t)}, Ret: t}

	case "chan::channel":
		// channel[T: Send](capacity: i64) -> (Sender[T], Receiver[T])
		t := elem()
		return &types.FnT{
			Params: []types.Type{i64},
			Ret:    &types.TupleT{Elems: []types.Type{c.named("Sender", t), c.named("Receiver", t)}},
		}
	case "chan::send_value":
		t := elem()
		return &types.FnT{Params: []types.Type{c.named("Sender", t), t}, Ret: unit}
	case "chan::await_value":
		// Dequeues under the runtime's lock and holds the value for this thread, so that
		// the prelude's `recv` can build `Option` in Origin without racing another
		// receiver between the test and the take.
		t := elem()
		return &types.FnT{Params: []types.Type{c.named("Receiver", t)}, Ret: types.P(types.Bool)}
	case "chan::taken_value":
		t := elem()
		return &types.FnT{Params: []types.Type{c.named("Receiver", t)}, Ret: t}
	case "chan::close_sender":
		t := elem()
		return &types.FnT{Params: []types.Type{c.named("Sender", t)}, Ret: unit}

	case "sync::mutex":
		t := elem()
		return &types.FnT{Params: []types.Type{t}, Ret: c.named("Mutex", t)}
	case "sync::with_lock":
		t := elem()
		r := c.freshFor(span)
		return &types.FnT{
			Params: []types.Type{c.named("Mutex", t), &types.FnT{Params: []types.Type{t}, Ret: r}},
			Ret:    r,
		}
	}
	return nil
}

// checkConcurrencyCall imposes what the signatures above cannot: the `Send` obligations
// that make ADR-0014's no-data-races claim true, and the capture check that a function
// type cannot carry.
//
// It runs after the call's arguments have been inferred, so the types it reports are the
// ones the programmer will recognize.
func (c *Checker) checkConcurrencyCall(v *ast.Call, fn *types.FnT, result types.Type) {
	ref, ok := c.res.Ref(v.Fn.NodeID())
	if !ok || ref.Kind != resolve.Builtin {
		return
	}
	send := c.traitByName("Send")
	if send == nil {
		return // a prelude without `Send` is broken; reported elsewhere
	}

	switch ref.Builtin {
	case "thread::spawn":
		// The thread's result crosses back to whoever joins it, so it must be `Send`.
		// Reported against the closure, which is where a programmer can change it.
		if len(v.Args) == 1 {
			if h, ok := types.Prune(result).(*types.Named); ok && len(h.Args) == 1 {
				c.requireSendAs(h.Args[0], send, v.Args[0].Span(), "E0702",
					"cannot be returned from a thread")
			}
			c.checkSpawnCaptures(v.Args[0])
		}

	case "chan::channel":
		// Nothing crosses yet, but the element type is fixed here, and reporting it at
		// the `channel` call names the line that chose the type rather than a distant
		// `send`.
		if t, ok := types.Prune(result).(*types.TupleT); ok && len(t.Elems) == 2 {
			if s, ok := types.Prune(t.Elems[0]).(*types.Named); ok && len(s.Args) == 1 {
				c.requireSendAs(s.Args[0], send, v.Span(), "E0700",
					"cannot be a channel's element type")
			}
		}

	case "sync::mutex":
		// `Mutex[T]` is `Send` for `Send` `T` -- the lock makes mutation safe, not the
		// contents sendable, so a `T` that is unsendable for a reason other than `mut`
		// is still unsendable inside one.
		_ = fn
	}
}

// checkSpawnCaptures reports a closure passed to `spawn` that captures something not
// `Send` (E0701).
//
// This is the check a type cannot do. `fn() -> i64` is one type whether the closure
// behind it captured an immutable counter or a mutable one, so the capture list recorded
// by resolution is the only place the answer exists. It is also the check that closes the
// obvious hole in ADR-0014: without it, a mutable aggregate that cannot cross a channel
// crosses by being captured instead.
func (c *Checker) checkSpawnCaptures(arg ast.Expr) {
	lam, ok := arg.(*ast.Lambda)
	if !ok {
		return // a named function has no captures; anything else is checked by its type
	}
	name, failure := c.closureCaptureNotSend(lam)
	if failure == nil {
		return
	}
	d := c.bag.Errorf("E0701", arg.Span(),
		"this closure cannot be spawned: it captures `%s`, which is not `Send`", name).
		Label("captured here")
	for _, line := range failure.chain() {
		d.Note("%s", line)
	}
	d.Note("a spawned closure carries its captures to another thread, so each one must " +
		"be `Send` for the same reason a channel's values must be")
	d.Help("share the value as a `Mutex[%s]`, whose accessor holds the lock, or capture "+
		"something immutable instead", failure.Type)
}
