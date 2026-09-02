package interp

import (
	"fmt"
	"strings"

	"github.com/scarypheonix/meta/internal/arith"
	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/bytecode"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/mono"
	"github.com/scarypheonix/meta/internal/resolve"
)

func (in *Interp) evalCall(c *ast.Call) (Value, ctrl) {
	// A call to a tuple variant constructs it; anything else evaluates the callee first,
	// then the arguments left to right (spec/04-expressions.md).
	if pe, ok := c.Fn.(*ast.PathExpr); ok {
		if ref, found := in.res.Ref(pe.NodeID()); found && ref.Kind == resolve.Variant {
			return in.constructVariant(c, ref)
		}
	}

	callee, ctl := in.evalExpr(c.Fn)
	if ctl.stops() {
		return Unit{}, ctl
	}
	args := make([]Value, 0, len(c.Args))
	for _, a := range c.Args {
		v, ac := in.evalExpr(a)
		if ac.stops() {
			return Unit{}, ac
		}
		args = append(args, v)
	}

	switch f := callee.(type) {
	case *Closure:
		return in.callClosure(f, args, c.Span()), normal
	case *Builtin:
		return in.callBuiltin(f.Name, args, c), normal
	}
	in.trap(c.Fn.Span(), "%s is not callable", TypeName(callee))
	return Unit{}, normal
}

func (in *Interp) constructVariant(c *ast.Call, ref resolve.Ref) (Value, ctrl) {
	if ref.Variant.Kind != ast.TupleVariant {
		in.trap(c.Span(), "`%s` takes no arguments", ref.Variant.Name.Name)
	}
	if len(c.Args) != len(ref.Variant.Types) {
		in.trap(c.Span(), "`%s` takes %d value(s) but %d were supplied",
			ref.Variant.Name.Name, len(ref.Variant.Types), len(c.Args))
	}
	vals := make([]Value, 0, len(c.Args))
	for _, a := range c.Args {
		v, ac := in.evalExpr(a)
		if ac.stops() {
			return Unit{}, ac
		}
		vals = append(vals, v)
	}
	return &Enum{Def: ref.Enum, Variant: ref.Variant, Vals: vals}, normal
}

func (in *Interp) evalMethodCall(m *ast.MethodCall) (Value, ctrl) {
	recv, c := in.evalExpr(m.Recv)
	if c.stops() {
		return Unit{}, c
	}
	args := make([]Value, 0, len(m.Args))
	for _, a := range m.Args {
		v, ac := in.evalExpr(a)
		if ac.stops() {
			return Unit{}, ac
		}
		args = append(args, v)
	}
	target, _ := in.mo.Lookup(in.frame().inst, m.NodeID())
	return in.callResolved(target, recv, m.Name.Name, in.kindOfExpr(m.Recv), args, m.Span()), normal
}

// callResolved runs the method a call site reaches.
//
// `target` is monomorphization's answer for this site inside the instance that is running
// -- the same answer the bytecode compiler and the backend get, from the same table
// (ADR-0010). It is what makes `x.shout()` inside `fn go[T: Loud](x: T)` reach a different
// impl per instantiation, which nothing about the runtime value could say: `1i64` and
// `2u8` are one Go int64 here.
//
// A site with no instance reaches a compiler-provided impl -- `42.to_str()`,
// `"a".cmp("b")` -- which is not Origin source to run, and falls through to the builtin
// methods.
func (in *Interp) callResolved(target *mono.Instance, recv Value, name string, kind bytecode.Kind, args []Value, span diag.Span) Value {
	if target != nil && target.Decl.Body != nil {
		return in.callFunction(target.Decl, target, args, recv, true, span)
	}
	if v, ok := in.builtinMethod(recv, name, kind, args, span); ok {
		return v
	}
	in.trap(span, "no method `%s` on %s", name, TypeName(recv))
	return Unit{}
}

// ---------------------------------------------------------------------------
// Builtins
// ---------------------------------------------------------------------------

func (in *Interp) callBuiltin(name string, args []Value, c *ast.Call) Value {
	span := c.Span()
	// The concurrency operations the prelude's own methods call (spec/12-concurrency.md).
	if v, ok := in.concurrencyBuiltin(name, args, span); ok {
		return v
	}
	// The array operations (spec/13-collections.md).
	if v, ok := in.arrayBuiltin(name, args, span); ok {
		return v
	}
	// The string operations the prelude's `Str` trait is written over (spec/14-strings.md).
	if v, ok := in.strBuiltin(name, args, span); ok {
		return v
	}
	// The file operations (spec/15-files.md).
	if v, ok := in.fsBuiltin(name, args, span); ok {
		return v
	}
	// A float's bits, which is what the prelude renders a float over (spec/16-floats.md).
	if v, ok := in.floatBuiltin(name, args, span); ok {
		return v
	}
	switch name {
	case "io::print", "io::println":
		if len(args) != 1 {
			in.trap(span, "`%s` takes exactly one argument", name)
		}
		s, ok := args[0].(*Str)
		if !ok {
			in.trap(span, "`%s` takes a `String`, found %s; call `.to_str()` first", name, TypeName(args[0]))
		}
		if name == "io::println" {
			fmt.Fprintln(in.stdout, s.S)
		} else {
			fmt.Fprint(in.stdout, s.S)
		}
		return Unit{}

	case "panic":
		if len(args) != 1 {
			in.trap(span, "`panic` takes exactly one argument")
		}
		s, ok := args[0].(*Str)
		if !ok {
			in.trap(span, "`panic` takes a `String`, found %s", TypeName(args[0]))
		}
		in.trap(span, "%s", s.S)

	case "ref_eq":
		if len(args) != 2 {
			in.trap(span, "`ref_eq` takes exactly two arguments")
		}
		eq, err := refEq(args[0], args[1])
		if err != nil {
			in.trap(span, "%s", err.Error())
		}
		return Bool(eq)
	}
	in.trap(span, "unknown builtin `%s`", name)
	return Unit{}
}

// builtinMethod implements the methods the prelude will eventually declare in Origin.
// Phase 7 replaces these with a real standard library; until then they live here so the
// end-to-end suite can exercise the language.
func (in *Interp) builtinMethod(recv Value, name string, kind bytecode.Kind, args []Value, span diag.Span) (Value, bool) {
	switch name {
	case "to_str":
		if len(args) != 0 {
			in.trap(span, "`to_str` takes no arguments")
		}
		return &Str{S: DisplayAt(kind, recv)}, true

	case "wrapping_add", "wrapping_sub", "wrapping_mul":
		a, b := in.twoInts(recv, args, name, span)
		k := intKindOr(kind)
		switch name {
		case "wrapping_add":
			return Int(arith.Wrap(k, a+b)), true
		case "wrapping_sub":
			return Int(arith.Wrap(k, a-b)), true
		default:
			return Int(arith.Wrap(k, a*b)), true
		}

	case "saturating_add", "saturating_sub", "saturating_mul":
		a, b := in.twoInts(recv, args, name, span)
		return Int(arith.Saturating(intKindOr(kind), arithOpOf(name), a, b)), true

	case "checked_add", "checked_sub", "checked_mul":
		a, b := in.twoInts(recv, args, name, span)
		v, ok := arith.Checked(intKindOr(kind), arithOpOf(name), a, b)
		if !ok {
			return in.optionNone(span), true
		}
		return in.optionSome(Int(v), span), true

	case "cmp":
		if len(args) != 1 {
			in.trap(span, "`cmp` takes exactly one argument")
		}
		return in.ordering(recv, args[0], intKindOr(kind), span), true
	}
	return nil, false
}

// intKindOr falls back to `i64` when the caller could not name a width, which happens only
// for a receiver the checker recorded no type for -- a shape no well-typed program has.
func intKindOr(k bytecode.Kind) bytecode.Kind {
	if k.IsInteger() {
		return k
	}
	return bytecode.KindI64
}

// arithOpOf maps a `checked_*` or `saturating_*` method name to the operation it performs.
func arithOpOf(name string) arith.Op {
	switch {
	case strings.HasSuffix(name, "_add"):
		return arith.OpAdd
	case strings.HasSuffix(name, "_sub"):
		return arith.OpSub
	}
	return arith.OpMul
}

func (in *Interp) twoInts(recv Value, args []Value, name string, span diag.Span) (uint64, uint64) {
	if len(args) != 1 {
		in.trap(span, "`%s` takes exactly one argument", name)
	}
	a, ok := recv.(Int)
	if !ok {
		in.trap(span, "`%s` applies to integers, found %s", name, TypeName(recv))
	}
	b, ok := args[0].(Int)
	if !ok {
		in.trap(span, "`%s` takes an integer, found %s", name, TypeName(args[0]))
	}
	return uint64(a), uint64(b)
}

// optionSome and optionNone build prelude `Option` values. They trap if the prelude is
// missing, which is a compiler bug rather than a program's.
func (in *Interp) optionSome(v Value, span diag.Span) Value {
	def, va := in.preludeVariant("Option", "Some", span)
	return &Enum{Def: def, Variant: va, Vals: []Value{v}}
}

func (in *Interp) optionNone(span diag.Span) Value {
	def, va := in.preludeVariant("Option", "None", span)
	return &Enum{Def: def, Variant: va}
}

func (in *Interp) ordering(a, b Value, k bytecode.Kind, span diag.Span) Value {
	cmp, err := compareValues(a, b, k)
	if err != nil {
		in.trap(span, "%s", err.Error())
	}
	name := "Equal"
	switch {
	case cmp < 0:
		name = "Less"
	case cmp > 0:
		name = "Greater"
	}
	def, va := in.preludeVariant("Ordering", name, span)
	return &Enum{Def: def, Variant: va}
}

func compareValues(a, b Value, k bytecode.Kind) (int, error) {
	switch x := a.(type) {
	case Int:
		y, ok := b.(Int)
		if !ok {
			return 0, fmt.Errorf("cannot compare an integer with %s", TypeName(b))
		}
		// Signed or unsigned as the static type says: `u64::MAX.cmp(1)` is Greater.
		return compareAt(intKindOr(k), uint64(x), uint64(y)), nil
	case Float:
		y, ok := b.(Float)
		if !ok {
			return 0, fmt.Errorf("cannot compare a float with %s", TypeName(b))
		}
		switch {
		case float64(x) < float64(y):
			return -1, nil
		case float64(x) > float64(y):
			return 1, nil
		}
		return 0, nil
	case Char:
		y, ok := b.(Char)
		if !ok {
			return 0, fmt.Errorf("cannot compare a char with %s", TypeName(b))
		}
		return compareInt(int64(x), int64(y)), nil
	case *Str:
		y, ok := b.(*Str)
		if !ok {
			return 0, fmt.Errorf("cannot compare a String with %s", TypeName(b))
		}
		return compareString(x.S, y.S), nil
	}
	return 0, fmt.Errorf("`cmp` is not defined on %s", TypeName(a))
}

// preludeVariant looks up an enum variant the interpreter needs to build.
func (in *Interp) preludeVariant(enum, variant string, span diag.Span) (*ast.EnumDecl, *ast.Variant) {
	def := in.res.Enums[enum]
	if def == nil {
		in.trap(span, "the prelude does not define `%s`", enum)
	}
	for _, v := range def.Variants {
		if v.Name.Name == variant {
			return def, v
		}
	}
	in.trap(span, "the prelude's `%s` has no variant `%s`", enum, variant)
	return nil, nil
}
