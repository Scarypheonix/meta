// Package interp is Origin's tree-walking interpreter (Phase 1).
//
// It evaluates the syntax tree directly, and its values are Go values: an Origin integer
// is a Go int64 with no width, an aggregate is a Go pointer. Where the specification's
// behaviour depends on a static type, that costs it something and it says so:
//
//   - Integer arithmetic uses i64 semantics for every integer. Per-width overflow
//     (`255u8 + 1`) needs the type checker and lands in Phase 2.
//   - Field mutability is checked when a field is assigned rather than at compile time,
//     because the receiver's type is not known until then. Phase 2 moves it earlier.
//
// Both are recorded in docs/phases/1-complete.md. Neither is a stub that lies: the rule
// is enforced, just later than the specification's phase ordering will eventually put it.
//
// Method dispatch used to be a third such place and is not any more. Which impl a call
// reaches is a fact about the receiver's static type -- `impl Loud for i64` and
// `impl Loud for u8` are one Go int64 here -- so the interpreter reads the answer from the
// instantiation set instead of guessing from the value, exactly as the bytecode compiler
// and the backend do (ADR-0029). It keeps no types of its own; it reads the ones the
// checker already computed.
package interp

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/mono"
	"github.com/scarypheonix/meta/internal/resolve"
)

// Value is a runtime value. Primitives are Go values; aggregates are pointers, which is
// what gives them the reference semantics of ADR-0008 for free.
type Value interface{ isValue() }

// Int is any integer. Phase 1 has one integer width.
type Int int64

// Float is `f32` or `f64`; Phase 1 computes in binary64.
type Float float64

// Bool is `true` or `false`.
type Bool bool

// Char is a Unicode scalar value.
type Char rune

// Unit is `()`.
type Unit struct{}

func (Int) isValue()   {}
func (Float) isValue() {}
func (Bool) isValue()  {}
func (Char) isValue()  {}
func (Unit) isValue()  {}

// Str is an immutable UTF-8 string. It is a pointer so that `ref_eq` has something to
// compare, matching spec/08-memory-model.md's classification of `String` as an
// aggregate.
type Str struct{ S string }

func (*Str) isValue() {}

// Tuple is a fixed-length heterogeneous sequence.
type Tuple struct{ Elems []Value }

func (*Tuple) isValue() {}

// Struct is an instance of a struct declaration. Fields are stored positionally, in
// declaration order, so that structural equality is deterministic.
type Struct struct {
	Def  *ast.StructDecl
	Vals []Value
}

func (*Struct) isValue() {}

// FieldIndex returns the position of a named field, or -1.
func (s *Struct) FieldIndex(name string) int {
	for i, f := range s.Def.Fields {
		if f.Name.Name == name {
			return i
		}
	}
	return -1
}

// Enum is an instance of one enum variant.
type Enum struct {
	Def     *ast.EnumDecl
	Variant *ast.Variant
	// Vals holds a tuple variant's positional payload or a struct variant's fields in
	// declaration order. A unit variant has none.
	Vals []Value
}

func (*Enum) isValue() {}

// FieldIndex returns the position of a named field in a struct variant, or -1.
func (e *Enum) FieldIndex(name string) int {
	for i, f := range e.Variant.Fields {
		if f.Name.Name == name {
			return i
		}
	}
	return -1
}

// Closure is a function value: either a named function or a lambda, together with the
// bindings it captured by value at creation (spec/04-expressions.md).
type Closure struct {
	// Fn is set for a named function or method.
	Fn *ast.FnDecl
	// Lambda is set for a lambda.
	Lambda *ast.Lambda
	// Env holds the captured bindings, by value.
	Env map[*resolve.Local]Value
	// Recv is the bound receiver for a method value.
	Recv    Value
	HasRecv bool
	// Inst is the monomorphized instance the function value stands for: for a named
	// function, the one its use site reaches; for a lambda, the one that created it. A
	// function value carries it because a call through the value happens somewhere with
	// no name to look up (ADR-0010).
	Inst *mono.Instance
}

func (*Closure) isValue() {}

// Builtin is a compiler-provided function such as `io::println`.
type Builtin struct{ Name string }

func (*Builtin) isValue() {}

// TypeName returns the name used in diagnostics for a value's type. Phase 1 has no type
// checker, so this is descriptive rather than authoritative.
func TypeName(v Value) string {
	switch t := v.(type) {
	case Int:
		return "integer"
	case Float:
		return "float"
	case Bool:
		return "bool"
	case Char:
		return "char"
	case Unit:
		return "()"
	case *Str:
		return "String"
	case *Tuple:
		return "tuple"
	case *Struct:
		return t.Def.Name.Name
	case *Enum:
		return t.Def.Name.Name
	case *Array:
		return "Array"
	case *Closure, *Builtin:
		return "function"
	}
	return "value"
}

// Display renders a value the way `to_str` does. It is total: every value has a
// rendering, so a diagnostic can always show one.
func Display(v Value) string {
	switch t := v.(type) {
	case Int:
		return strconv.FormatInt(int64(t), 10)
	case Float:
		return formatFloat(float64(t))
	case Bool:
		if t {
			return "true"
		}
		return "false"
	case Char:
		return string(rune(t))
	case Unit:
		return "()"
	case *Str:
		return t.S
	case *Tuple:
		var parts []string
		for _, e := range t.Elems {
			parts = append(parts, Display(e))
		}
		return "(" + strings.Join(parts, ", ") + ")"
	case *Struct:
		var parts []string
		for i, f := range t.Def.Fields {
			parts = append(parts, f.Name.Name+": "+Display(t.Vals[i]))
		}
		return t.Def.Name.Name + " { " + strings.Join(parts, ", ") + " }"
	case *Enum:
		return displayEnum(t)
	case *Array:
		var parts []string
		for _, e := range t.Elems {
			parts = append(parts, Display(e))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *Closure, *Builtin:
		return "<function>"
	}
	return "<value>"
}

func displayEnum(e *Enum) string {
	switch e.Variant.Kind {
	case ast.UnitVariant:
		return e.Variant.Name.Name
	case ast.TupleVariant:
		var parts []string
		for _, v := range e.Vals {
			parts = append(parts, Display(v))
		}
		return e.Variant.Name.Name + "(" + strings.Join(parts, ", ") + ")"
	default:
		var parts []string
		for i, f := range e.Variant.Fields {
			parts = append(parts, f.Name.Name+": "+Display(e.Vals[i]))
		}
		return e.Variant.Name.Name + " { " + strings.Join(parts, ", ") + " }"
	}
}

// formatFloat renders a float with the shortest representation that round-trips.
//
// The specification does not yet fix float formatting; docs/deferred.md records it
// against Phase 7. Until it does, this is the implementation's choice and the
// end-to-end suite avoids depending on it.
func formatFloat(f float64) string {
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eE") && !strings.Contains(s, "Inf") && !strings.Contains(s, "NaN") {
		s += ".0"
	}
	return s
}

// equal implements `==`: structural, total, and recursive (spec/04-expressions.md).
// It reports false for a type mismatch rather than trapping, because Phase 1 has no
// type checker to have rejected the comparison earlier.
func equal(a, b Value) (bool, error) {
	switch x := a.(type) {
	case Int:
		y, ok := b.(Int)
		return ok && x == y, nil
	case Float:
		y, ok := b.(Float)
		return ok && x == y, nil // IEEE: NaN != NaN, 0.0 == -0.0
	case Bool:
		y, ok := b.(Bool)
		return ok && x == y, nil
	case Char:
		y, ok := b.(Char)
		return ok && x == y, nil
	case Unit:
		_, ok := b.(Unit)
		return ok, nil
	case *Str:
		y, ok := b.(*Str)
		return ok && x.S == y.S, nil
	case *Tuple:
		y, ok := b.(*Tuple)
		if !ok || len(x.Elems) != len(y.Elems) {
			return false, nil
		}
		for i := range x.Elems {
			eq, err := equal(x.Elems[i], y.Elems[i])
			if err != nil || !eq {
				return false, err
			}
		}
		return true, nil
	case *Struct:
		y, ok := b.(*Struct)
		if !ok || x.Def != y.Def {
			return false, nil
		}
		for i := range x.Vals {
			eq, err := equal(x.Vals[i], y.Vals[i])
			if err != nil || !eq {
				return false, err
			}
		}
		return true, nil
	case *Enum:
		y, ok := b.(*Enum)
		if !ok || x.Def != y.Def || x.Variant != y.Variant {
			return false, nil
		}
		for i := range x.Vals {
			eq, err := equal(x.Vals[i], y.Vals[i])
			if err != nil || !eq {
				return false, err
			}
		}
		return true, nil
	case *Array:
		// Element-wise, and capacity-blind: two arrays holding the same elements are
		// equal whatever room they have left (spec/13-collections.md).
		y, ok := b.(*Array)
		if !ok || len(x.Elems) != len(y.Elems) {
			return false, nil
		}
		for i := range x.Elems {
			eq, err := equal(x.Elems[i], y.Elems[i])
			if err != nil || !eq {
				return false, err
			}
		}
		return true, nil
	case *Closure, *Builtin:
		return false, fmt.Errorf("function values cannot be compared with `==`")
	}
	return false, fmt.Errorf("cannot compare %s", TypeName(a))
}

// refEq implements `ref_eq`: object identity, defined only for aggregates.
func refEq(a, b Value) (bool, error) {
	switch x := a.(type) {
	case *Str:
		y, ok := b.(*Str)
		return ok && x == y, nil
	case *Tuple:
		y, ok := b.(*Tuple)
		return ok && x == y, nil
	case *Struct:
		y, ok := b.(*Struct)
		return ok && x == y, nil
	case *Enum:
		y, ok := b.(*Enum)
		return ok && x == y, nil
	}
	return false, fmt.Errorf("`ref_eq` is defined only for aggregates, not %s", TypeName(a))
}
