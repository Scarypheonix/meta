package types

import (
	"strings"
	"testing"
)

func TestPrimClassification(t *testing.T) {
	for _, tt := range []struct {
		k                               PrimKind
		integer, signed, float, ordered bool
	}{
		{I8, true, true, false, true},
		{U64, true, false, false, true},
		{F64, false, false, true, true},
		{Bool, false, false, false, false},
		{Char, false, false, false, true},
		{String, false, false, false, true},
		{UnitKind, false, false, false, false},
	} {
		if got := tt.k.IsInteger(); got != tt.integer {
			t.Errorf("%s.IsInteger() = %v, want %v", tt.k, got, tt.integer)
		}
		if got := tt.k.IsSigned(); got != tt.signed {
			t.Errorf("%s.IsSigned() = %v, want %v", tt.k, got, tt.signed)
		}
		if got := tt.k.IsFloat(); got != tt.float {
			t.Errorf("%s.IsFloat() = %v, want %v", tt.k, got, tt.float)
		}
		if got := tt.k.IsOrdered(); got != tt.ordered {
			t.Errorf("%s.IsOrdered() = %v, want %v", tt.k, got, tt.ordered)
		}
	}
	for _, tt := range []struct {
		k    PrimKind
		bits uint
	}{{I8, 8}, {U16, 16}, {I32, 32}, {U64, 64}, {Bool, 0}} {
		if got := tt.k.Bits(); got != tt.bits {
			t.Errorf("%s.Bits() = %d, want %d", tt.k, got, tt.bits)
		}
	}
}

func TestUnifyPrimitives(t *testing.T) {
	if err := Unify(P(I64), P(I64)); err != nil {
		t.Errorf("i64 should unify with itself: %v", err)
	}
	err := Unify(P(I32), P(I64))
	if err == nil {
		t.Fatal("i32 must not unify with i64: Origin has no implicit widening")
	}
	if !strings.Contains(err.Error(), "`i32`") || !strings.Contains(err.Error(), "`i64`") {
		t.Errorf("the error should name both types in source syntax, got %q", err.Error())
	}
}

func TestUnifyBindsVariables(t *testing.T) {
	c := NewCtx()
	v := c.Fresh()
	if err := Unify(v, P(Bool)); err != nil {
		t.Fatalf("binding a fresh variable should succeed: %v", err)
	}
	if p, ok := AsPrim(v); !ok || p.Kind != Bool {
		t.Errorf("the variable did not bind to bool")
	}
	if err := Unify(v, P(I64)); err == nil {
		t.Error("a bound variable must not rebind to a different type")
	}
}

func TestErrorAndNeverUnifyWithAnything(t *testing.T) {
	// spec/09-errors.md rule 4: the error type must not produce a second diagnostic.
	if err := Unify(Error, P(I64)); err != nil {
		t.Errorf("the error type must unify with anything: %v", err)
	}
	if err := Unify(P(String), Error); err != nil {
		t.Errorf("the error type must unify with anything: %v", err)
	}
	// spec/03-types.md: a diverging expression has type `!` and unifies with any type.
	if err := Unify(P(Never), P(I64)); err != nil {
		t.Errorf("`!` must unify with anything: %v", err)
	}
}

func TestOccursCheck(t *testing.T) {
	c := NewCtx()
	v := c.Fresh()
	err := Unify(v, &TupleT{Elems: []Type{v, P(I64)}})
	if err == nil {
		t.Fatal("binding a variable inside itself must fail the occurs check")
	}
	if !err.Infinite {
		t.Errorf("the error should be flagged as an infinite type, got %q", err.Error())
	}
}

func TestUnifyStructural(t *testing.T) {
	c := NewCtx()
	def := &Def{Kind: StructDef, Name: "Pair", Params: []*Param{c.NewParam("A"), c.NewParam("B")}}

	a, b := c.Fresh(), c.Fresh()
	want := &Named{Def: def, Args: []Type{P(I64), P(Bool)}}
	got := &Named{Def: def, Args: []Type{a, b}}
	if err := Unify(want, got); err != nil {
		t.Fatalf("structural unification failed: %v", err)
	}
	if p, ok := AsPrim(a); !ok || p.Kind != I64 {
		t.Error("the first argument did not bind to i64")
	}
	if p, ok := AsPrim(b); !ok || p.Kind != Bool {
		t.Error("the second argument did not bind to bool")
	}

	other := &Def{Kind: StructDef, Name: "Other"}
	if err := Unify(want, &Named{Def: other}); err == nil {
		t.Error("two different declarations must not unify: nominal types are nominal")
	}
}

func TestUnifyArityMismatchExplainsItself(t *testing.T) {
	err := Unify(&TupleT{Elems: []Type{P(I64)}}, &TupleT{Elems: []Type{P(I64), P(I64)}})
	if err == nil {
		t.Fatal("tuples of different arity must not unify")
	}
	if !strings.Contains(err.Error(), "1-element tuple") {
		t.Errorf("the error should explain the arity mismatch, got %q", err.Error())
	}

	err = Unify(&FnT{Params: []Type{P(I64)}, Ret: P(I64)}, &FnT{Ret: P(I64)})
	if err == nil {
		t.Fatal("functions of different arity must not unify")
	}
	if !strings.Contains(err.Error(), "argument") {
		t.Errorf("the error should mention argument count, got %q", err.Error())
	}
}

func TestRigidParametersOnlyUnifyWithThemselves(t *testing.T) {
	c := NewCtx()
	p1, p2 := c.NewParam("T"), c.NewParam("T")
	if err := Unify(p1, p1); err != nil {
		t.Errorf("a parameter should unify with itself: %v", err)
	}
	if err := Unify(p1, p2); err == nil {
		t.Error("two parameters that merely share a name must not unify")
	}
	if err := Unify(p1, P(I64)); err == nil {
		t.Error("a rigid parameter must not unify with a concrete type")
	}
}

func TestDefaulting(t *testing.T) {
	c := NewCtx()
	v := c.FreshDefaulting(IntDefault)
	if !ApplyDefaults(v) {
		t.Fatal("an integer-defaulting variable should resolve")
	}
	if p, ok := AsPrim(v); !ok || p.Kind != I64 {
		t.Error("an unsolved integer literal must default to i64")
	}

	f := c.FreshDefaulting(FloatDefault)
	ApplyDefaults(f)
	if p, ok := AsPrim(f); !ok || p.Kind != F64 {
		t.Error("an unsolved float literal must default to f64")
	}

	plain := c.Fresh()
	if ApplyDefaults(plain) {
		t.Error("a variable with no default must not silently resolve")
	}
}

func TestDefaultCarriesAcrossVariables(t *testing.T) {
	c := NewCtx()
	lit := c.FreshDefaulting(IntDefault)
	binding := c.Fresh()
	if err := Unify(lit, binding); err != nil {
		t.Fatalf("unifying two variables should succeed: %v", err)
	}
	if !ApplyDefaults(binding) {
		t.Fatal("the default should have carried across, so `let x = 1; let y = x;` still defaults")
	}
	if p, ok := AsPrim(binding); !ok || p.Kind != I64 {
		t.Error("the carried default should be i64")
	}
}

func TestSubstitute(t *testing.T) {
	c := NewCtx()
	tp := c.NewParam("T")
	def := &Def{Kind: EnumDef, Name: "Option", Params: []*Param{tp}}
	generic := &Named{Def: def, Args: []Type{tp}}

	got := Substitute(generic, map[*Param]Type{tp: P(String)})
	if got.String() != "Option[String]" {
		t.Errorf("Substitute gave %q, want %q", got.String(), "Option[String]")
	}
	// Substitution must not mutate the original.
	if generic.String() != "Option[T]" {
		t.Errorf("Substitute mutated its input: %q", generic.String())
	}
}

func TestStringUsesSourceSyntax(t *testing.T) {
	c := NewCtx()
	def := &Def{Kind: StructDef, Name: "Map", Params: []*Param{c.NewParam("K"), c.NewParam("V")}}
	for _, tt := range []struct {
		t    Type
		want string
	}{
		{P(I64), "i64"},
		{P(UnitKind), "()"},
		{&TupleT{Elems: []Type{P(I64), P(Bool)}}, "(i64, bool)"},
		{&TupleT{Elems: []Type{P(I64)}}, "(i64,)"},
		{&FnT{Params: []Type{P(I64)}, Ret: P(Bool)}, "fn(i64) -> bool"},
		{&Named{Def: def, Args: []Type{P(String), P(I64)}}, "Map[String, i64]"},
		{Error, "{unknown}"},
	} {
		if got := tt.t.String(); got != tt.want {
			t.Errorf("String() = %q, want %q", got, tt.want)
		}
	}
}

// spec/09-errors.md rule 2: a diagnostic must never contain an internal identifier, so
// an unsolved inference variable must not print its number.
func TestUnsolvedVariablePrintsAsUnderscore(t *testing.T) {
	c := NewCtx()
	for i := 0; i < 20; i++ {
		c.Fresh()
	}
	v := c.Fresh()
	if got := v.String(); got != "_" {
		t.Errorf("an unsolved variable printed as %q; it must never leak its number", got)
	}
	Unify(v, P(I64))
	if got := v.String(); got != "i64" {
		t.Errorf("a bound variable should print as what it is, got %q", got)
	}
}

func TestFreeVars(t *testing.T) {
	c := NewCtx()
	a, b := c.Fresh(), c.Fresh()
	ty := &FnT{Params: []Type{a, b, a}, Ret: &TupleT{Elems: []Type{b}}}
	vars := FreeVars(ty, nil)
	if len(vars) != 2 {
		t.Errorf("FreeVars found %d variables, want 2 (deduplicated)", len(vars))
	}
	Unify(a, P(I64))
	if n := len(FreeVars(ty, nil)); n != 1 {
		t.Errorf("after binding one variable FreeVars found %d, want 1", n)
	}
}
