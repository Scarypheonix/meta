package types

import "fmt"

// Ctx allocates inference variables and performs unification. One Ctx serves one
// function body, matching ADR-0009: inference never crosses a function boundary.
type Ctx struct {
	nextVar   int
	nextParam int
	level     int
}

// NewCtx returns a fresh inference context.
func NewCtx() *Ctx { return &Ctx{} }

// Fresh returns a new unbound inference variable at the current level.
func (c *Ctx) Fresh() *Var {
	c.nextVar++
	return &Var{ID: c.nextVar, Level: c.level}
}

// EnterLevel and ExitLevel bracket a `let` right-hand side. Variables created inside
// are candidates for generalization when the level is left.
func (c *Ctx) EnterLevel() { c.level++ }

// ExitLevel leaves a level entered by EnterLevel.
func (c *Ctx) ExitLevel() { c.level-- }

// Level reports the current generalization level.
func (c *Ctx) Level() int { return c.level }

// FreshDefaulting returns a new inference variable that falls back to a concrete type if
// nothing else determines it (spec/03-types.md: integer literals default to `i64`,
// float literals to `f64`).
func (c *Ctx) FreshDefaulting(d Defaulting) *Var {
	v := c.Fresh()
	v.Default = d
	return v
}

// NewParam returns a fresh rigid type parameter.
func (c *Ctx) NewParam(name string) *Param {
	c.nextParam++
	return &Param{Name: name, ID: c.nextParam}
}

// UnifyError describes why two types could not be unified. The checker turns it into a
// diagnostic; it never renders one itself, so that every message about types is
// phrased in one place.
type UnifyError struct {
	// Want and Got are the two types at the outermost point of disagreement.
	Want, Got Type
	// Detail names a more specific cause when there is one, such as an arity mismatch.
	Detail string
	// Infinite reports that the failure was the occurs check.
	Infinite bool
}

func (e *UnifyError) Error() string {
	if e.Infinite {
		return fmt.Sprintf("infinite type: `%s` would contain itself", e.Got)
	}
	if e.Detail != "" {
		return e.Detail
	}
	return fmt.Sprintf("expected `%s`, found `%s`", e.Want, e.Got)
}

// Unify makes want and got equal, binding inference variables as needed.
//
// The error type unifies with everything, so one reported mistake never produces a
// second diagnostic. The never type unifies with everything for the same structural
// reason: a diverging expression has no value to disagree about.
func Unify(want, got Type) *UnifyError {
	want, got = Prune(want), Prune(got)

	if want == got {
		return nil
	}
	if IsError(want) || IsError(got) {
		return nil
	}
	if IsNever(want) || IsNever(got) {
		return nil
	}

	if v, ok := want.(*Var); ok {
		return bind(v, got)
	}
	if v, ok := got.(*Var); ok {
		return bind(v, want)
	}

	switch w := want.(type) {
	case *Prim:
		g, ok := got.(*Prim)
		if !ok || g.Kind != w.Kind {
			return &UnifyError{Want: want, Got: got}
		}
		return nil

	case *Param:
		g, ok := got.(*Param)
		if !ok || g.ID != w.ID {
			return &UnifyError{Want: want, Got: got}
		}
		return nil

	case *Named:
		g, ok := got.(*Named)
		if !ok || g.Def != w.Def {
			return &UnifyError{Want: want, Got: got}
		}
		if len(w.Args) != len(g.Args) {
			return &UnifyError{Want: want, Got: got,
				Detail: fmt.Sprintf("`%s` takes %d type argument(s) but %d were supplied",
					w.Def.Name, len(w.Args), len(g.Args))}
		}
		for i := range w.Args {
			if err := Unify(w.Args[i], g.Args[i]); err != nil {
				return err
			}
		}
		return nil

	case *TupleT:
		g, ok := got.(*TupleT)
		if !ok {
			return &UnifyError{Want: want, Got: got}
		}
		if len(w.Elems) != len(g.Elems) {
			return &UnifyError{Want: want, Got: got,
				Detail: fmt.Sprintf("expected a %d-element tuple, found %d elements",
					len(w.Elems), len(g.Elems))}
		}
		for i := range w.Elems {
			if err := Unify(w.Elems[i], g.Elems[i]); err != nil {
				return err
			}
		}
		return nil

	case *AssocT:
		g, ok := got.(*AssocT)
		if !ok || g.Trait != w.Trait || g.Member != w.Member {
			return &UnifyError{Want: want, Got: got}
		}
		return Unify(w.Self, g.Self)

	case *FnT:
		g, ok := got.(*FnT)
		if !ok {
			return &UnifyError{Want: want, Got: got}
		}
		if len(w.Params) != len(g.Params) {
			return &UnifyError{Want: want, Got: got,
				Detail: fmt.Sprintf("expected a function taking %d argument(s), found one taking %d",
					len(w.Params), len(g.Params))}
		}
		for i := range w.Params {
			if err := Unify(w.Params[i], g.Params[i]); err != nil {
				return err
			}
		}
		return Unify(w.Ret, g.Ret)
	}
	return &UnifyError{Want: want, Got: got}
}

// bind points an unbound variable at a type, after the occurs check.
func bind(v *Var, t Type) *UnifyError {
	if v == t {
		return nil
	}
	if occurs(v, t) {
		return &UnifyError{Want: v, Got: t, Infinite: true}
	}
	// A variable escaping into an outer scope must not stay generalizable there, so
	// lower every level inside t to v's.
	lowerLevels(t, v.Level)
	// Binding a defaulting variable to another variable carries the default across, so
	// `let x = 1; let y = x;` still defaults once, at the end.
	if other, ok := Prune(t).(*Var); ok && other.Default == NoDefault {
		other.Default = v.Default
	}
	v.Ref = t
	return nil
}

// lowerLevels caps the level of every free variable in t at limit.
func lowerLevels(t Type, limit int) {
	switch p := Prune(t).(type) {
	case *Var:
		if p.Level > limit {
			p.Level = limit
		}
	case *Named:
		for _, a := range p.Args {
			lowerLevels(a, limit)
		}
	case *TupleT:
		for _, e := range p.Elems {
			lowerLevels(e, limit)
		}
	case *FnT:
		for _, a := range p.Params {
			lowerLevels(a, limit)
		}
		lowerLevels(p.Ret, limit)
	case *AssocT:
		lowerLevels(p.Self, limit)
	}
}

// Scheme is a possibly-polymorphic type: the variables it quantifies over, and its body.
// A monomorphic type is a Scheme with no variables.
type Scheme struct {
	Vars []*Var
	Type Type
}

// Mono wraps a type as a monomorphic scheme.
func Mono(t Type) *Scheme { return &Scheme{Type: t} }

// Generalize quantifies over the variables in t whose level exceeds the context's,
// which are exactly the ones no enclosing binding constrains.
func (c *Ctx) Generalize(t Type) *Scheme {
	var vars []*Var
	for _, v := range FreeVars(t, nil) {
		if v.Level > c.level {
			vars = append(vars, v)
		}
	}
	return &Scheme{Vars: vars, Type: t}
}

// Instantiate returns a copy of the scheme's type with fresh variables in place of the
// quantified ones.
func (c *Ctx) Instantiate(s *Scheme) Type {
	if len(s.Vars) == 0 {
		return s.Type
	}
	m := make(map[*Var]Type, len(s.Vars))
	for _, v := range s.Vars {
		fresh := c.Fresh()
		fresh.Default = v.Default
		m[v] = fresh
	}
	return replaceVars(s.Type, m)
}

func replaceVars(t Type, m map[*Var]Type) Type {
	switch p := Prune(t).(type) {
	case *Var:
		if r, ok := m[p]; ok {
			return r
		}
		return p
	case *Named:
		if len(p.Args) == 0 {
			return p
		}
		args := make([]Type, len(p.Args))
		for i, a := range p.Args {
			args[i] = replaceVars(a, m)
		}
		return &Named{Def: p.Def, Args: args}
	case *TupleT:
		elems := make([]Type, len(p.Elems))
		for i, e := range p.Elems {
			elems[i] = replaceVars(e, m)
		}
		return &TupleT{Elems: elems}
	case *FnT:
		params := make([]Type, len(p.Params))
		for i, a := range p.Params {
			params[i] = replaceVars(a, m)
		}
		return &FnT{Params: params, Ret: replaceVars(p.Ret, m)}
	case *AssocT:
		return &AssocT{Trait: p.Trait, Member: p.Member, Self: replaceVars(p.Self, m)}
	default:
		return p
	}
}

// occurs reports whether v appears inside t, which would make the type infinite.
func occurs(v *Var, t Type) bool {
	switch p := Prune(t).(type) {
	case *Var:
		return p == v
	case *Named:
		for _, a := range p.Args {
			if occurs(v, a) {
				return true
			}
		}
	case *TupleT:
		for _, e := range p.Elems {
			if occurs(v, e) {
				return true
			}
		}
	case *FnT:
		for _, a := range p.Params {
			if occurs(v, a) {
				return true
			}
		}
		return occurs(v, p.Ret)
	}
	return false
}

// Substitute replaces generic parameters with the types they are instantiated at.
func Substitute(t Type, subst map[*Param]Type) Type {
	if len(subst) == 0 {
		return t
	}
	switch p := Prune(t).(type) {
	case *Param:
		if r, ok := subst[p]; ok {
			return r
		}
		return p
	case *Named:
		if len(p.Args) == 0 {
			return p
		}
		args := make([]Type, len(p.Args))
		for i, a := range p.Args {
			args[i] = Substitute(a, subst)
		}
		return &Named{Def: p.Def, Args: args}
	case *TupleT:
		elems := make([]Type, len(p.Elems))
		for i, e := range p.Elems {
			elems[i] = Substitute(e, subst)
		}
		return &TupleT{Elems: elems}
	case *FnT:
		params := make([]Type, len(p.Params))
		for i, a := range p.Params {
			params[i] = Substitute(a, subst)
		}
		return &FnT{Params: params, Ret: Substitute(p.Ret, subst)}
	case *AssocT:
		return &AssocT{Trait: p.Trait, Member: p.Member, Self: Substitute(p.Self, subst)}
	default:
		return p
	}
}

// FreeVars appends the unbound inference variables in t to out.
func FreeVars(t Type, out []*Var) []*Var {
	switch p := Prune(t).(type) {
	case *Var:
		for _, seen := range out {
			if seen == p {
				return out
			}
		}
		return append(out, p)
	case *Named:
		for _, a := range p.Args {
			out = FreeVars(a, out)
		}
	case *TupleT:
		for _, e := range p.Elems {
			out = FreeVars(e, out)
		}
	case *FnT:
		for _, a := range p.Params {
			out = FreeVars(a, out)
		}
		out = FreeVars(p.Ret, out)
	case *AssocT:
		out = FreeVars(p.Self, out)
	}
	return out
}

// ApplyDefaults resolves an unsolved variable to its fallback, and reports whether it
// now has one. Defaulting happens once, after all constraints in a body are solved
// (spec/03-types.md).
func ApplyDefaults(t Type) bool {
	v, ok := Prune(t).(*Var)
	if !ok {
		return true
	}
	switch v.Default {
	case IntDefault:
		v.Ref = P(I64)
		return true
	case FloatDefault:
		v.Ref = P(F64)
		return true
	}
	return false
}
