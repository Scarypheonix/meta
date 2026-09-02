package check

import (
	"sort"
	"strings"

	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/types"
)

// builtinImpls lists the compiler-provided impls for primitive types.
//
// These cannot be written in Origin until the standard library exists (Phase 7), because
// their bodies need operations the language does not expose. Declaring them here rather
// than special-casing bound checking keeps one code path: `i64: Show` is satisfied by
// finding an impl, exactly like `MyType: Show`.
var builtinImpls = map[string][]types.PrimKind{
	// The floats are absent on purpose: `Show for f64` and `Show for f32` are Origin
	// source in the prelude, because the shortest decimal that reads back as the same
	// float needs exact arithmetic wider than sixty-four bits and is not something to
	// write three times (spec/16-floats.md, ADR-0031).
	"Show": {
		types.I8, types.I16, types.I32, types.I64,
		types.U8, types.U16, types.U32, types.U64,
		types.Bool, types.Char, types.String,
	},
	"Ord": {
		types.I8, types.I16, types.I32, types.I64,
		types.U8, types.U16, types.U32, types.U64,
		types.F32, types.F64, types.Char, types.String,
	},
	"Int": {
		types.I8, types.I16, types.I32, types.I64,
		types.U8, types.U16, types.U32, types.U64,
	},
}

// traitByName finds a declared trait by name. The prelude declares the ones the
// compiler itself needs.
func (c *Checker) traitByName(name string) *TraitInfo {
	for _, ti := range c.traits {
		if ti.Decl.Name.Name == name {
			return ti
		}
	}
	return nil
}

func (c *Checker) registerBuiltinImpls() {
	names := make([]string, 0, len(builtinImpls))
	for name := range builtinImpls {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic impl order, so diagnostics are deterministic

	for _, name := range names {
		ti := c.traitByName(name)
		if ti == nil {
			continue // the prelude does not declare it; nothing to register
		}
		for _, k := range builtinImpls[name] {
			info := &ImplInfo{
				Self: types.P(k), Trait: ti, Builtin: true,
				Methods: map[string]*ast.FnDecl{},
				Assoc:   map[string]types.Type{},
			}
			c.impls = append(c.impls, info)
			c.indexImpl(info)
		}
	}
}

// ---------------------------------------------------------------------------
// Coherence
// ---------------------------------------------------------------------------

// checkCoherence enforces the overlap rule from spec/06-traits-generics.md: no two impls
// of the same trait may apply to the same type.
//
// The orphan rule is the other half. Origin 0.1 compiles one package, so every trait and
// every type is local and the orphan rule cannot be violated yet; it becomes checkable
// when the package manager lands (Phase 8) and is recorded in docs/deferred.md.
func (c *Checker) checkCoherence() {
	for i := 0; i < len(c.impls); i++ {
		a := c.impls[i]
		if a.Trait == nil {
			continue
		}
		for j := i + 1; j < len(c.impls); j++ {
			b := c.impls[j]
			if b.Trait != a.Trait {
				continue
			}
			if a.Builtin && b.Builtin {
				continue // the compiler's own impls are disjoint by construction
			}
			if !c.implsOverlap(a, b) {
				continue
			}
			// Report against the impl the programmer wrote, whichever side it is on.
			user, other := a, b
			if a.Builtin {
				user, other = b, a
			}
			if other.Builtin {
				c.bag.Errorf("E0119", user.Decl.Span(),
					"conflicting implementations of trait `%s` for `%s`",
					user.Trait.Decl.Name.Name, user.Self).
					Label("this impl conflicts with a built-in one").
					Note("`%s` is implemented for `%s` by the compiler", user.Trait.Decl.Name.Name, user.Self).
					Help("the primitive impls arrive as Origin source in Phase 7; until then they cannot be replaced")
				continue
			}
			c.bag.Errorf("E0119", user.Decl.Span(),
				"conflicting implementations of trait `%s`", user.Trait.Decl.Name.Name).
				Label("first implementation").
				Secondary(other.Decl.Span(), "conflicting implementation here").
				Note("both apply to `%s`", user.Self)
		}
	}
	c.checkInherentMethodClashes()
}

// implsOverlap reports whether two impls of the same trait can apply to one type. Each
// impl's generic parameters are instantiated with fresh variables first, so
// `impl[T] Trait for Vec[T]` and `impl Trait for Vec[i64]` are correctly found to
// overlap.
func (c *Checker) implsOverlap(a, b *ImplInfo) bool {
	selfA, _ := c.instantiateImplSelf(a)
	selfB, _ := c.instantiateImplSelf(b)
	return types.Unify(selfA, selfB) == nil
}

// instantiateImplSelf returns the impl's self type with fresh inference variables in
// place of its generic parameters, plus the substitution used.
func (c *Checker) instantiateImplSelf(info *ImplInfo) (types.Type, map[*types.Param]types.Type) {
	if len(info.Params) == 0 {
		return info.Self, nil
	}
	subst := make(map[*types.Param]types.Type, len(info.Params))
	for _, p := range info.Params {
		subst[p] = c.ctx.Fresh()
	}
	return types.Substitute(info.Self, subst), subst
}

// checkInherentMethodClashes reports a method defined twice for the same type across
// separate inherent impls, which method resolution would find ambiguous.
func (c *Checker) checkInherentMethodClashes() {
	seen := map[string]map[string]*ast.FnDecl{}
	for _, info := range c.impls {
		if info.Trait != nil {
			continue
		}
		key := selfKey(info.Self)
		if seen[key] == nil {
			seen[key] = map[string]*ast.FnDecl{}
		}
		for name, m := range info.Methods {
			if prev, dup := seen[key][name]; dup {
				c.bag.Errorf("E0034", m.Name.Loc,
					"`%s` has two inherent methods named `%s`", key, name).
					Label("duplicate method").
					Secondary(prev.Name.Loc, "first defined here")
				continue
			}
			seen[key][name] = m
		}
	}
}

// ---------------------------------------------------------------------------
// Bound solving
// ---------------------------------------------------------------------------

// satisfies reports whether subject implements the trait, and if not, why.
func (c *Checker) satisfies(subject types.Type, ti *TraitInfo) bool {
	subject = types.Prune(subject)
	if types.IsError(subject) || types.IsNever(subject) {
		return true // already reported, or diverging
	}
	// An unsolved variable cannot be judged yet; treat it as satisfied and let the
	// unsolved-type check report it, so one mistake produces one diagnostic.
	if _, unsolved := subject.(*types.Var); unsolved {
		return true
	}

	// `Send` is derived from a type's structure rather than found among impls (ADR-0014,
	// send.go). A concrete type is judged directly; a rigid parameter still needs a
	// declared bound, since nothing is known about what it will be instantiated with, so
	// that case falls through to the ordinary bound lookup below.
	if ti.Decl.Name.Name == "Send" {
		if _, rigid := subject.(*types.Param); !rigid {
			return c.isSend(subject)
		}
	}

	// A rigid parameter is satisfied only by a declared bound, directly or through a
	// supertrait.
	if p, ok := subject.(*types.Param); ok {
		for _, b := range c.env.bounds {
			if bp, ok := types.Prune(b.Type).(*types.Param); ok && bp == p {
				if b.Trait == ti || c.supertraitReaches(b.Trait, ti) {
					return true
				}
			}
		}
		return false
	}

	for _, info := range c.candidateImpls(subject) {
		if info.Trait != ti {
			continue
		}
		self, subst := c.instantiateImplSelf(info)
		if types.Unify(self, subject) != nil {
			continue
		}
		// The impl's own bounds must hold at this instantiation.
		ok := true
		for _, b := range info.Bounds {
			if !c.satisfies(types.Substitute(b.Type, subst), b.Trait) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// supertraitReaches reports whether have implies want through supertraits.
func (c *Checker) supertraitReaches(have, want *TraitInfo) bool {
	if have == want {
		return true
	}
	for _, st := range have.Supertraits {
		if c.supertraitReaches(st.Trait, want) {
			return true
		}
	}
	return false
}

// candidateImpls returns the impls that could apply to a type.
func (c *Checker) candidateImpls(t types.Type) []*ImplInfo {
	return c.implsBySelf[selfKey(t)]
}

// requireBound records an obligation to be verified once the body's inference is done.
func (c *Checker) requireBound(subject types.Type, ti *TraitInfo, span diag.Span) {
	c.obligations = append(c.obligations, Bound{Type: subject, Trait: ti, Span: span})
}

// requireSendAs is requireBound for a `Send` obligation that should report its own
// diagnostic rather than the generic one: what the value was being used for is the whole
// content of the message (spec/12-concurrency.md).
func (c *Checker) requireSendAs(subject types.Type, ti *TraitInfo, span diag.Span, code, what string) {
	c.obligations = append(c.obligations,
		Bound{Type: subject, Trait: ti, Span: span, Code: code, What: what})
}

// solveObligations checks every bound collected while checking a body.
func (c *Checker) solveObligations() {
	for _, b := range c.obligations {
		if c.satisfies(b.Type, b.Trait) {
			continue
		}
		// `Send` is derived, so the generic advice below -- "nothing implements it yet;
		// write an impl" -- is not merely unhelpful but wrong: writing `impl Send` is not
		// how a type becomes sendable, and following that help would be a mistake. Say
		// which field makes it unsendable instead (spec/12-concurrency.md).
		if b.Trait.Decl.Name.Name == "Send" {
			c.reportNotSend(b)
			continue
		}
		d := c.bag.Errorf("E0277", b.Span, "`%s` does not implement `%s`",
			b.Type, b.Trait.Decl.Name.Name).
			Label("the trait bound is not satisfied")
		if _, isParam := types.Prune(b.Type).(*types.Param); isParam {
			d.Help("add a bound: `%s: %s`", b.Type, b.Trait.Decl.Name.Name)
		} else if impls := c.implementorsOf(b.Trait); impls != "" {
			d.Note("`%s` is implemented for %s", b.Trait.Decl.Name.Name, impls)
			d.Help("write `impl %s for %s { .. }`", b.Trait.Decl.Name.Name, b.Type)
		} else {
			d.Note("nothing implements `%s` yet", b.Trait.Decl.Name.Name)
			d.Help("write `impl %s for %s { .. }`", b.Trait.Decl.Name.Name, b.Type)
		}
		d.Secondary(b.Trait.Decl.Name.Loc, "trait declared here")
	}
	c.obligations = nil
}

// implementorsOf lists a few types that do implement the trait, to make the diagnostic
// actionable. It is deliberately truncated: a wall of type names helps nobody.
func (c *Checker) implementorsOf(ti *TraitInfo) string {
	var names []string
	for _, info := range c.impls {
		if info.Trait == ti {
			names = append(names, "`"+info.Self.String()+"`")
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return ""
	}
	if len(names) > 4 {
		return strings.Join(names[:4], ", ") + " and others"
	}
	return strings.Join(names, ", ")
}

// ---------------------------------------------------------------------------
// Method resolution
// ---------------------------------------------------------------------------

// methodCandidate is a resolved method call target.
type methodCandidate struct {
	Decl *ast.FnDecl
	Sig  *FnSig
	// Subst maps the impl's and the method's generic parameters to the types this call
	// instantiates them at.
	Subst map[*types.Param]types.Type
	// Trait names the trait the method came from, or nil for an inherent method.
	Trait *TraitInfo
	// Impl is the impl block the method was found in, nil when the receiver is a type
	// parameter and the method came from one of its bounds.
	Impl *ImplInfo
}

// lookupMethod implements the resolution order of spec/06-traits-generics.md: inherent
// methods first, then trait methods. Origin has no autoref and no autoderef, so the
// receiver type must match exactly.
func (c *Checker) lookupMethod(recv types.Type, name string) (*methodCandidate, []string) {
	recv = types.Prune(recv)

	// A rigid parameter's methods come from its declared bounds.
	if p, ok := recv.(*types.Param); ok {
		for _, b := range c.env.bounds {
			bp, ok := types.Prune(b.Type).(*types.Param)
			if !ok || bp != p {
				continue
			}
			if cand := c.traitMethod(b.Trait, p, name); cand != nil {
				return cand, nil
			}
			for _, st := range b.Trait.Supertraits {
				if cand := c.traitMethod(st.Trait, p, name); cand != nil {
					return cand, nil
				}
			}
		}
		return nil, nil
	}

	var inherent, viaTrait []*methodCandidate
	for _, info := range c.candidateImpls(recv) {
		self, subst := c.instantiateImplSelf(info)
		if types.Unify(self, recv) != nil {
			continue
		}
		decl, ok := info.Methods[name]
		if !ok && info.Trait != nil {
			// A trait impl may inherit a default method body from the trait, and a
			// compiler-provided impl has no bodies at all: its methods are the trait's
			// declarations, implemented by the interpreter and later the backend.
			if d, has := info.Trait.Methods[name]; has && (d.Body != nil || info.Builtin) {
				decl = d
				ok = true
			}
		}
		if !ok {
			continue
		}
		sig := c.fnSigs[decl]
		if sig == nil && info.Trait != nil {
			sig = info.Trait.Sigs[name]
		}
		if sig == nil {
			continue
		}
		cand := &methodCandidate{Decl: decl, Sig: sig, Subst: subst, Trait: info.Trait, Impl: info}
		if info.Trait == nil {
			inherent = append(inherent, cand)
		} else {
			viaTrait = append(viaTrait, cand)
		}
	}

	if len(inherent) == 1 {
		return inherent[0], nil
	}
	if len(inherent) > 1 {
		return nil, []string{"more than one inherent method"}
	}
	if len(viaTrait) == 1 {
		return viaTrait[0], nil
	}
	if len(viaTrait) > 1 {
		var names []string
		for _, cand := range viaTrait {
			names = append(names, cand.Trait.Decl.Name.Name)
		}
		sort.Strings(names)
		return nil, names
	}
	return nil, nil
}

// traitMethod builds a candidate for calling a trait method on a rigid parameter.
func (c *Checker) traitMethod(ti *TraitInfo, self types.Type, name string) *methodCandidate {
	decl, ok := ti.Methods[name]
	if !ok {
		return nil
	}
	sig := ti.Sigs[name]
	if sig == nil {
		return nil
	}
	return &methodCandidate{
		Decl: decl, Sig: sig, Trait: ti,
		Subst: map[*types.Param]types.Type{ti.SelfParam: self},
	}
}

// normalize reduces an associated-type projection when the impl that defines it is
// known. An unreduced projection is not an error: it stays symbolic while the self type
// is still a rigid parameter.
func (c *Checker) normalize(t types.Type) types.Type {
	proj, ok := types.Prune(t).(*types.AssocT)
	if !ok {
		return t
	}
	self := c.normalize(proj.Self)
	if _, stillParam := types.Prune(self).(*types.Param); stillParam {
		return &types.AssocT{Trait: proj.Trait, Member: proj.Member, Self: self}
	}
	for _, info := range c.candidateImpls(self) {
		if info.Trait == nil || info.Trait.Decl != proj.Trait {
			continue
		}
		implSelf, subst := c.instantiateImplSelf(info)
		if types.Unify(implSelf, self) != nil {
			continue
		}
		if def, ok := info.Assoc[proj.Member]; ok {
			return c.normalize(types.Substitute(def, subst))
		}
	}
	return &types.AssocT{Trait: proj.Trait, Member: proj.Member, Self: self}
}
