// Package types is the shared representation of Origin's semantic types.
//
// It is the single module both the checker and, later, the backend agree on
// (process rule 5). It owns type terms, substitution, unification and printing; it does
// not own declarations, inference strategy, or diagnostics — those belong to the
// checker, which is the only package allowed to decide what a type error means.
//
// Printing is part of the contract: spec/09-errors.md rule 3 requires types in messages
// to be written in source syntax, so String is a specified behaviour and is tested.
package types

import (
	"fmt"
	"strings"

	"github.com/scarypheonix/meta/internal/ast"
)

// Type is a semantic type.
type Type interface {
	isType()
	// String renders the type in Origin source syntax.
	String() string
}

// PrimKind identifies a primitive type.
type PrimKind int

// The primitive types of spec/03-types.md.
const (
	I8 PrimKind = iota
	I16
	I32
	I64
	U8
	U16
	U32
	U64
	F32
	F64
	Bool
	Char
	String
	UnitKind
	// Never is the type of a diverging expression. It unifies with anything and is not
	// writable in source in 0.1.
	Never
)

var primNames = [...]string{
	I8: "i8", I16: "i16", I32: "i32", I64: "i64",
	U8: "u8", U16: "u16", U32: "u32", U64: "u64",
	F32: "f32", F64: "f64",
	Bool: "bool", Char: "char", String: "String", UnitKind: "()", Never: "!",
}

func (k PrimKind) String() string {
	if int(k) < len(primNames) && primNames[k] != "" {
		return primNames[k]
	}
	return fmt.Sprintf("prim(%d)", int(k))
}

// IsInteger reports whether k is one of the integer types.
func (k PrimKind) IsInteger() bool { return k >= I8 && k <= U64 }

// IsSigned reports whether k is a signed integer type.
func (k PrimKind) IsSigned() bool { return k >= I8 && k <= I64 }

// IsFloat reports whether k is a float type.
func (k PrimKind) IsFloat() bool { return k == F32 || k == F64 }

// IsNumeric reports whether arithmetic operators apply to k.
func (k PrimKind) IsNumeric() bool { return k.IsInteger() || k.IsFloat() }

// IsOrdered reports whether `<` and friends apply to k directly (spec/04-expressions.md:
// integers, floats, char and String; everything else goes through `Ord`).
func (k PrimKind) IsOrdered() bool { return k.IsNumeric() || k == Char || k == String }

// Bits returns the width of an integer type.
func (k PrimKind) Bits() uint {
	switch k {
	case I8, U8:
		return 8
	case I16, U16:
		return 16
	case I32, U32:
		return 32
	case I64, U64:
		return 64
	}
	return 0
}

// Prim is a primitive type.
type Prim struct{ Kind PrimKind }

func (*Prim) isType()          {}
func (p *Prim) String() string { return p.Kind.String() }

// Shared instances for the primitives, so equality checks can be by pointer where that
// is convenient and allocation is avoided in the hot path.
var prims = func() [Never + 1]*Prim {
	var a [Never + 1]*Prim
	for k := I8; k <= Never; k++ {
		a[k] = &Prim{Kind: k}
	}
	return a
}()

// P returns the shared instance of a primitive type.
func P(k PrimKind) *Prim { return prims[k] }

// Unit is the type `()`.
func Unit() *Prim { return P(UnitKind) }

// DefKind distinguishes the two kinds of nominal declaration.
type DefKind int

const (
	// StructDef is a struct declaration.
	StructDef DefKind = iota
	// EnumDef is an enum declaration.
	EnumDef
	// BuiltinDef is a nominal type the compiler provides rather than any source
	// declares: `Array[T]` (ADR-0028). It has parameters and a name like any other
	// nominal type, and no declaration behind it -- which is what makes
	// `Array[i64] { }` the same error `String { }` already is, with nothing new to
	// write for it.
	BuiltinDef
)

// ArrayDef is the declaration `Array[T]` does not have.
//
// One value for the whole compiler, compared by pointer, so that "is this an array?" is a
// question with one answer rather than a name match. spec/13-collections.md specifies what
// it is; internal/compile gives each instantiation its layout.
var ArrayDef = &Def{
	Kind:   BuiltinDef,
	Name:   "Array",
	Params: []*Param{{Name: "T", ID: -1}},
}

// Def is a nominal type declaration: a struct or an enum, with its generic parameters.
type Def struct {
	Kind   DefKind
	Name   string
	Params []*Param
	// Struct and Enum: exactly one is set, according to Kind.
	Struct *ast.StructDecl
	Enum   *ast.EnumDecl
	// FieldTypes holds a struct's field types in declaration order, in terms of Params.
	FieldTypes []Type
	// VariantTypes holds an enum's payload types per variant, in declaration order.
	VariantTypes [][]Type
}

// Named is a nominal type applied to arguments: `Vec[i64]`, `Point`.
type Named struct {
	Def  *Def
	Args []Type
}

func (*Named) isType() {}

func (n *Named) String() string {
	if len(n.Args) == 0 {
		return n.Def.Name
	}
	var parts []string
	for _, a := range n.Args {
		parts = append(parts, a.String())
	}
	return n.Def.Name + "[" + strings.Join(parts, ", ") + "]"
}

// TupleT is `(A, B)`.
type TupleT struct{ Elems []Type }

func (*TupleT) isType() {}

func (t *TupleT) String() string {
	var parts []string
	for _, e := range t.Elems {
		parts = append(parts, e.String())
	}
	if len(parts) == 1 {
		return "(" + parts[0] + ",)"
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// FnT is `fn(A, B) -> C`.
type FnT struct {
	Params []Type
	Ret    Type
}

func (*FnT) isType() {}

func (f *FnT) String() string {
	var parts []string
	for _, p := range f.Params {
		parts = append(parts, p.String())
	}
	return "fn(" + strings.Join(parts, ", ") + ") -> " + f.Ret.String()
}

// Param is a generic parameter: a rigid type variable that unifies only with itself.
type Param struct {
	Name string
	// ID makes two same-named parameters from different declarations distinct.
	ID int
}

func (*Param) isType()          {}
func (p *Param) String() string { return p.Name }

// Defaulting says what an unsolved inference variable becomes at the end of a body
// (spec/03-types.md).
type Defaulting int

const (
	// NoDefault means an unsolved variable is an error.
	NoDefault Defaulting = iota
	// IntDefault means an unsolved variable becomes `i64`.
	IntDefault
	// FloatDefault means an unsolved variable becomes `f64`.
	FloatDefault
)

// Var is an inference variable. Binding is by mutation -- unify.go's bind and the two
// defaulting cases write Ref, and nothing else does -- which is why every read goes through
// Prune. Prune itself does not write; see the note there for why that matters once a solved
// graph is read from several threads.
//
// Level implements Rémy's ranked generalization: a variable created inside a `let`
// right-hand side has a higher level than the binding around it, and only variables
// whose level exceeds the current one may be generalized. Without it, `let` would
// generalize variables that the enclosing scope still constrains.
type Var struct {
	ID      int
	Ref     Type
	Default Defaulting
	Level   int
}

func (*Var) isType() {}

func (v *Var) String() string {
	if r := Prune(v); r != v {
		return r.String()
	}
	// spec/09-errors.md rule 2: a diagnostic must never show an internal identifier, so
	// an unsolved variable prints as an underscore, never as its number.
	return "_"
}

// ErrorT stands in for a type that could not be determined. A diagnostic was already
// reported, so unifying with it always succeeds and never reports again
// (spec/09-errors.md rule 4).
type ErrorT struct{}

func (*ErrorT) isType()        {}
func (*ErrorT) String() string { return "{unknown}" }

// Error is the shared error type instance.
var Error = &ErrorT{}

// Prune follows bound inference variables to the type they stand for. Every function that
// inspects a type must call it first.
//
// It does not compress the path, and that is deliberate. Compression made this a *write* to
// shared state on what every caller treats as a read, which is fine while only the checker
// runs and it is single-threaded -- and is a data race the moment a solved type graph is
// read concurrently. The interpreter does exactly that: `kindOfType` prunes on the
// arithmetic path, and green threads are goroutines, so several of them inspect the same
// types at once. `go test -race ./tests/e2e` reported it on every threaded program.
//
// Losing compression costs nothing that was being paid for. The three sites that bind a
// variable (unify.go's bind and the two defaulting cases) all run during checking, so by the
// time the graph is read concurrently it is solved and immutable, and the chains that
// compression would have shortened are already as short as inference left them. Measured on
// the heaviest checking workload the project has -- `originc check stage1/src`, 34,000 lines
// of Origin -- the two versions are 360/408/375 ms against 346/397/378 ms: the difference is
// smaller than the variance between runs of either.
//
// The loop replaces the recursion for the same reason it is cheap to do so: the chain is
// walked either way, and an iterative walk cannot deepen the Go stack on a pathological one.
func Prune(t Type) Type {
	for {
		v, ok := t.(*Var)
		if !ok || v.Ref == nil {
			return t
		}
		t = v.Ref
	}
}

// IsError reports whether t is the error type, after pruning.
func IsError(t Type) bool {
	_, ok := Prune(t).(*ErrorT)
	return ok
}

// IsNever reports whether t is the never type, after pruning.
func IsNever(t Type) bool {
	p, ok := Prune(t).(*Prim)
	return ok && p.Kind == Never
}

// AsPrim returns t as a primitive, after pruning.
func AsPrim(t Type) (*Prim, bool) {
	p, ok := Prune(t).(*Prim)
	return p, ok
}

// AsNamed returns t as a nominal type, after pruning.
func AsNamed(t Type) (*Named, bool) {
	n, ok := Prune(t).(*Named)
	return n, ok
}

// AssocT is an associated-type projection, `T::Item`, that has not been reduced to a
// concrete type yet. The checker normalizes it by finding the impl that defines the
// member; it survives as a type only while the self type is still a rigid parameter.
type AssocT struct {
	Trait  *ast.TraitDecl
	Member string
	Self   Type
}

func (*AssocT) isType() {}

func (a *AssocT) String() string { return a.Self.String() + "::" + a.Member }
