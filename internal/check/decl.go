// Package check is Origin's type checker (Phase 2).
//
// It implements spec/03-types.md's inference strategy: mandatory signatures, so every
// body is checked in isolation against a known type; inference inside a body; the value
// restriction on `let` generalization; and defaulting once, at the end. It also owns
// exhaustiveness (spec/05), trait bounds and coherence (spec/06).
//
// It never resolves a name. Everything an identifier refers to comes from the resolver's
// side tables (spec/07-modules.md), which is the layering that lets both passes be
// tested independently.
package check

import (
	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/resolve"
	"github.com/scarypheonix/meta/internal/types"
)

// Bound is one trait obligation: `Type: Trait`.
type Bound struct {
	Type  types.Type
	Trait *TraitInfo
	Args  []types.Type
	Span  diag.Span
}

// FnSig is a function's declared type. Signatures are mandatory (ADR-0009), so this is
// always known before any body is checked.
type FnSig struct {
	Decl *ast.FnDecl
	// Params are the function's own generic parameters.
	Params []*types.Param
	// Self is the receiver's type for a method, nil otherwise.
	Self       types.Type
	ParamTypes []types.Type
	Ret        types.Type
	Bounds     []Bound
}

// TraitInfo is a declared trait.
type TraitInfo struct {
	Decl        *ast.TraitDecl
	Params      []*types.Param
	Supertraits []Bound
	// AssocTypes are the associated type names, in declaration order.
	AssocTypes []string
	// Methods maps a method name to its declaration.
	Methods map[string]*ast.FnDecl
	// Sigs maps a method name to its signature, with Self left as the trait's own self
	// parameter so it can be substituted per impl.
	Sigs map[string]*FnSig
	// SelfParam is the rigid parameter standing for `Self` inside the trait.
	SelfParam *types.Param
}

// ImplInfo is one impl block.
type ImplInfo struct {
	Decl   *ast.ImplDecl
	Params []*types.Param
	Self   types.Type
	// Trait is nil for an inherent impl.
	Trait     *TraitInfo
	TraitArgs []types.Type
	Bounds    []Bound
	Methods   map[string]*ast.FnDecl
	Assoc     map[string]types.Type
	// Builtin marks a compiler-provided impl for a primitive type. Such impls have no
	// AST and are listed in builtinImpls; Phase 7 replaces them with Origin source.
	Builtin bool
}

// Result is the checker's output: the side tables later passes read.
type Result struct {
	// ExprTypes maps an expression node to its type.
	ExprTypes map[ast.NodeID]types.Type
	// PatTypes maps a pattern node to the type it matches against.
	PatTypes map[ast.NodeID]types.Type
	// LocalTypes maps a binding to its type.
	LocalTypes map[*resolve.Local]types.Type
	// Methods maps a method-call node to the declaration it resolves to, so the
	// interpreter and later the backend do not repeat the search.
	Methods map[ast.NodeID]*ast.FnDecl
	// Generics lists the type parameters a function's body may mention: the enclosing
	// impl's or trait's, then `Self` for a trait method, then the function's own. A
	// function with none is compiled once; a function with some is compiled once per
	// instantiation (ADR-0010).
	Generics map[*ast.FnDecl][]*types.Param
	// Insts maps a call site to the instantiation of the callee: the generic
	// parameters its body may mention and the type each was given here. It is what
	// monomorphization (ADR-0010) reads to decide which specialized copy to emit.
	//
	// A recorded argument may still be symbolic. Inside a generic body the arguments
	// are the *enclosing* function's parameters, so the monomorphizer substitutes its
	// own instantiation into them before it has a concrete tuple.
	Insts map[ast.NodeID]*Inst
	// Defs maps a struct or enum declaration to its type definition.
	Defs map[ast.Item]*types.Def
	// SelfTypes maps a method's declaration to its receiver's type, mirroring
	// FnSig.Self (a checker-internal table this is the one field of it that
	// internal/compile needs, to know a method's `self` parameter's kind for a stack
	// map, ADR-0021). Absent for a plain function.
	SelfTypes map[*ast.FnDecl]types.Type
	// Lookup resolves a method on a type after checking is done. Monomorphization
	// needs it: a trait method called on a type parameter cannot be resolved while the
	// parameter is symbolic, and can be once the parameter has a type.
	Lookup *Resolver
}

// Inst is one instantiation of a callee at a call site.
type Inst struct {
	// Decl is the function or method called. For a trait method called on a type
	// parameter it is the trait's own declaration, which may have no body: the
	// concrete impl is only known once the parameter is substituted.
	Decl *ast.FnDecl
	// Params and Args are parallel: the generic parameters the callee's body may
	// mention, and the type each was instantiated at.
	Params []*types.Param
	Args   []types.Type
	// Recv is the receiver's type for a method call, nil otherwise.
	Recv types.Type
	// Method is the method's name, empty for a plain function call.
	Method string
}

// Resolver answers method-resolution questions after checking, over the impl table the
// checker built. It exists for monomorphization, which resolves a trait method against
// the concrete type a parameter turned out to be.
type Resolver struct {
	c *Checker
}

// Method resolves a method call on a concrete receiver type, reporting the declaration
// to call and the generic parameters of the impl that provides it.
//
// It returns false when no method is found, which for a well-typed program means the
// receiver is a primitive whose impl is compiler-provided and has no declaration to
// call. The caller falls back to the builtin in that case.
func (r *Resolver) Method(recv types.Type, name string) (*Inst, bool) {
	cand, _ := r.c.lookupMethod(recv, name)
	if cand == nil {
		return nil, false
	}
	subst := map[*types.Param]types.Type{}
	for k, v := range cand.Subst {
		subst[k] = v
	}
	if cand.Trait != nil {
		subst[cand.Trait.SelfParam] = recv
	}
	inst := &Inst{Decl: cand.Decl, Recv: recv, Method: name}
	for _, p := range r.c.out.Generics[cand.Decl] {
		arg, known := subst[p]
		if !known {
			// A method with generic parameters of its own, called on a type parameter:
			// nothing at this point says what they were instantiated at.
			return nil, false
		}
		inst.Params = append(inst.Params, p)
		inst.Args = append(inst.Args, types.Prune(arg))
	}
	return inst, true
}

// Checker holds the state of one program's check.
type Checker struct {
	bag *diag.Bag
	res *resolve.Result
	ctx *types.Ctx
	out *Result

	defs    map[ast.Item]*types.Def
	fnSigs  map[*ast.FnDecl]*FnSig
	traits  map[*ast.TraitDecl]*TraitInfo
	aliases map[*ast.TypeAliasDecl]types.Type
	impls   []*ImplInfo
	// implsBySelf indexes impls by the name of the type they are for, which is how
	// method resolution finds candidates without scanning every impl.
	implsBySelf map[string][]*ImplInfo

	// env is the type environment of the declaration being checked.
	env typeEnv
	// ret is the return type of the function body being checked.
	ret types.Type
	// obligations are the trait bounds collected while checking the current body.
	obligations []Bound
	// bodyVars are the inference variables created for the current body, checked for
	// an unsolved leftover at the end.
	bodyVars []pendingVar
	// intLits are the integer literals in the current body, range-checked once their
	// types are settled.
	intLits []literalUse
	// opChecks are operator applications whose operand type is not settled yet. They
	// are verified after defaulting, because `1.0 & 2.0` and `let x: u8 = 1 + 2;` both
	// depend on what the literals turn out to be.
	opChecks []operandCheck
	// schemes holds the generalized type of each `let` binding that generalized.
	schemes map[*resolve.Local]*types.Scheme
	// generalized records the variables a `let` quantified over. They are deliberately
	// unsolved -- that is what polymorphism is -- so the end-of-body check skips them.
	generalized map[*types.Var]bool
	// loopValues stacks the type each enclosing `loop` breaks with.
	loopValues []types.Type
}

// typeEnv maps the type names in scope for one declaration.
type typeEnv struct {
	params map[string]*types.Param
	self   types.Type
	// selfTrait is set inside a trait declaration, so `Self::Item` knows its trait.
	selfTrait *TraitInfo
	// bounds are the declared obligations in scope, which is how `T::Item` finds the
	// trait that supplies `Item`.
	bounds []Bound
}

// Program runs the checker over the resolved files. It always returns a Result; check
// bag.HasErrors before trusting it.
func Program(bag *diag.Bag, res *resolve.Result, files ...*ast.File) *Result {
	c := &Checker{
		bag: bag, res: res, ctx: types.NewCtx(),
		out: &Result{
			ExprTypes:  map[ast.NodeID]types.Type{},
			PatTypes:   map[ast.NodeID]types.Type{},
			LocalTypes: map[*resolve.Local]types.Type{},
			Methods:    map[ast.NodeID]*ast.FnDecl{},
			Insts:      map[ast.NodeID]*Inst{},
			Generics:   map[*ast.FnDecl][]*types.Param{},
			Defs:       map[ast.Item]*types.Def{},
			SelfTypes:  map[*ast.FnDecl]types.Type{},
		},
		defs:        map[ast.Item]*types.Def{},
		fnSigs:      map[*ast.FnDecl]*FnSig{},
		traits:      map[*ast.TraitDecl]*TraitInfo{},
		aliases:     map[*ast.TypeAliasDecl]types.Type{},
		implsBySelf: map[string][]*ImplInfo{},
		schemes:     map[*resolve.Local]*types.Scheme{},
		generalized: map[*types.Var]bool{},
	}

	// Declarations are collected in dependency order: nominal shells first (so a type
	// can refer to any other), then traits, then signatures and impls, then bodies.
	for _, f := range files {
		c.collectShells(f)
	}
	for _, f := range files {
		c.collectTraits(f)
	}
	for _, f := range files {
		c.collectFields(f)
	}
	for _, f := range files {
		c.collectSignatures(f)
	}
	c.registerBuiltinImpls()
	for _, f := range files {
		c.collectImpls(f)
	}
	c.checkCoherence()
	for _, f := range files {
		c.checkBodies(f)
	}
	c.out.Defs = c.defs
	c.out.Lookup = &Resolver{c: c}
	return c.out
}

// ---------------------------------------------------------------------------
// Pass 1: nominal shells
// ---------------------------------------------------------------------------

func (c *Checker) collectShells(f *ast.File) {
	for _, it := range f.Items {
		switch v := it.(type) {
		case *ast.StructDecl:
			c.defs[it] = &types.Def{
				Kind: types.StructDef, Name: v.Name.Name,
				Params: c.genericParams(v.Generics), Struct: v,
			}
		case *ast.EnumDecl:
			c.defs[it] = &types.Def{
				Kind: types.EnumDef, Name: v.Name.Name,
				Params: c.genericParams(v.Generics), Enum: v,
			}
		}
	}
}

func (c *Checker) genericParams(gs []*ast.GenericParam) []*types.Param {
	out := make([]*types.Param, 0, len(gs))
	for _, g := range gs {
		out = append(out, c.ctx.NewParam(g.Name.Name))
	}
	return out
}

// envFor builds the type environment for a declaration's generic parameters.
func envFor(params []*types.Param, self types.Type, trait *TraitInfo) typeEnv {
	m := make(map[string]*types.Param, len(params))
	for _, p := range params {
		m[p.Name] = p
	}
	return typeEnv{params: m, self: self, selfTrait: trait}
}

// ---------------------------------------------------------------------------
// Pass 2: traits
// ---------------------------------------------------------------------------

func (c *Checker) collectTraits(f *ast.File) {
	for _, it := range f.Items {
		v, ok := it.(*ast.TraitDecl)
		if !ok {
			continue
		}
		ti := &TraitInfo{
			Decl:      v,
			Params:    c.genericParams(v.Generics),
			Methods:   map[string]*ast.FnDecl{},
			Sigs:      map[string]*FnSig{},
			SelfParam: c.ctx.NewParam("Self"),
		}
		for _, at := range v.AssocTypes {
			ti.AssocTypes = append(ti.AssocTypes, at.Name.Name)
		}
		for _, m := range v.Methods {
			if prev, dup := ti.Methods[m.Name.Name]; dup {
				c.bag.Errorf("E0034", m.Name.Loc, "trait `%s` declares `%s` twice", v.Name.Name, m.Name.Name).
					Label("duplicate method").
					Secondary(prev.Name.Loc, "first declared here")
				continue
			}
			ti.Methods[m.Name.Name] = m
		}
		c.traits[v] = ti
	}
}

// ---------------------------------------------------------------------------
// Pass 3: struct fields and enum payloads
// ---------------------------------------------------------------------------

func (c *Checker) collectFields(f *ast.File) {
	for _, it := range f.Items {
		switch v := it.(type) {
		case *ast.StructDecl:
			def := c.defs[it]
			c.env = envFor(def.Params, nil, nil)
			for _, fld := range v.Fields {
				def.FieldTypes = append(def.FieldTypes, c.toType(fld.Type))
			}
		case *ast.EnumDecl:
			def := c.defs[it]
			c.env = envFor(def.Params, nil, nil)
			for _, va := range v.Variants {
				var payload []types.Type
				switch va.Kind {
				case ast.TupleVariant:
					for _, t := range va.Types {
						payload = append(payload, c.toType(t))
					}
				case ast.StructVariant:
					for _, fld := range va.Fields {
						payload = append(payload, c.toType(fld.Type))
					}
				}
				def.VariantTypes = append(def.VariantTypes, payload)
			}
		case *ast.TypeAliasDecl:
			c.env = envFor(c.genericParams(v.Generics), nil, nil)
			c.aliases[v] = c.toType(v.Type)
		}
	}
	c.env = typeEnv{}
}

// ---------------------------------------------------------------------------
// Pass 4: function and trait-method signatures
// ---------------------------------------------------------------------------

func (c *Checker) collectSignatures(f *ast.File) {
	for _, it := range f.Items {
		switch v := it.(type) {
		case *ast.FnDecl:
			c.fnSigs[v] = c.signature(v, nil, nil, nil)
		case *ast.TraitDecl:
			ti := c.traits[v]
			selfTy := ti.SelfParam
			for name, m := range ti.Methods {
				ti.Sigs[name] = c.signature(m, ti.Params, selfTy, ti)
			}
			for _, st := range v.Supertraits {
				c.env = envFor(ti.Params, selfTy, ti)
				if b, ok := c.toBound(selfTy, st); ok {
					ti.Supertraits = append(ti.Supertraits, b)
				}
			}
		}
	}
	c.env = typeEnv{}
}

// signature builds a function's declared type. outerParams are the enclosing
// declaration's generic parameters (a trait's or an impl's).
func (c *Checker) signature(fn *ast.FnDecl, outerParams []*types.Param, self types.Type, trait *TraitInfo) *FnSig {
	own := c.genericParams(fn.Generics)
	all := append(append([]*types.Param{}, outerParams...), own...)
	c.env = envFor(all, self, trait)

	// The generic parameters the body may mention, in one fixed order: the enclosing
	// declaration's, then `Self` when the enclosing declaration is a trait, then the
	// function's own. Monomorphization keys an instantiation on exactly this list, and
	// method resolution builds its argument list from it, so the order lives here and
	// nowhere else.
	generics := append([]*types.Param{}, outerParams...)
	if p, isParam := self.(*types.Param); isParam {
		generics = append(generics, p)
	}
	c.out.Generics[fn] = append(generics, own...)

	sig := &FnSig{Decl: fn, Params: own, Self: self}
	if fn.Self != nil {
		sig.Self = self
	}
	// Bounds are collected before the parameter and return types are converted: an
	// associated-type projection such as `T::Item` finds the trait that declares
	// `Item` by looking at the bounds in scope.
	sig.Bounds = c.collectBounds(fn.Generics, fn.Where, own)
	c.env.bounds = sig.Bounds

	for _, p := range fn.Params {
		sig.ParamTypes = append(sig.ParamTypes, c.toType(p.Type))
	}
	sig.Ret = types.Unit()
	if fn.Ret != nil {
		sig.Ret = c.toType(fn.Ret)
	}
	return sig
}

// collectBounds turns inline bounds and where predicates into obligations.
func (c *Checker) collectBounds(gs []*ast.GenericParam, where []*ast.WherePred, own []*types.Param) []Bound {
	var out []Bound
	byName := map[string]*types.Param{}
	for _, p := range own {
		byName[p.Name] = p
	}
	for _, g := range gs {
		p, ok := byName[g.Name.Name]
		if !ok {
			continue
		}
		for _, b := range g.Bounds {
			if bound, ok := c.toBound(p, b); ok {
				out = append(out, bound)
			}
		}
	}
	for _, w := range where {
		subject := c.toType(w.Type)
		for _, b := range w.Bounds {
			if bound, ok := c.toBound(subject, b); ok {
				out = append(out, bound)
			}
		}
	}
	return out
}

// toBound resolves a syntactic trait reference against a subject type.
func (c *Checker) toBound(subject types.Type, tr *ast.TraitRef) (Bound, bool) {
	ref, ok := c.res.Ref(tr.NodeID())
	if !ok || ref.Kind == resolve.Unresolved {
		return Bound{}, false // already reported by the resolver
	}
	if ref.Kind != resolve.Trait {
		c.bag.Errorf("E0404", tr.Span(), "`%s` is not a trait", tr.Path).
			Label("expected a trait name").
			Note("only a trait can appear in a bound")
		return Bound{}, false
	}
	ti := c.traits[ref.Trait]
	if ti == nil {
		return Bound{}, false
	}
	var args []types.Type
	for _, a := range tr.Args {
		args = append(args, c.toType(a))
	}
	return Bound{Type: subject, Trait: ti, Args: args, Span: tr.Span()}, true
}

// ---------------------------------------------------------------------------
// Pass 5: impls
// ---------------------------------------------------------------------------

func (c *Checker) collectImpls(f *ast.File) {
	for _, it := range f.Items {
		v, ok := it.(*ast.ImplDecl)
		if !ok {
			continue
		}
		params := c.genericParams(v.Generics)
		c.env = envFor(params, nil, nil)
		self := c.toType(v.Type)

		info := &ImplInfo{
			Decl: v, Params: params, Self: self,
			Methods: map[string]*ast.FnDecl{},
			Assoc:   map[string]types.Type{},
		}
		c.env = envFor(params, self, nil)
		info.Bounds = c.collectBounds(nil, v.Where, params)

		if v.Trait != nil {
			if b, ok := c.toBound(self, v.Trait); ok {
				info.Trait, info.TraitArgs = b.Trait, b.Args
			}
		}
		for _, at := range v.AssocTypes {
			info.Assoc[at.Name.Name] = c.toType(at.Type)
		}
		for _, m := range v.Methods {
			if prev, dup := info.Methods[m.Name.Name]; dup {
				c.bag.Errorf("E0034", m.Name.Loc, "this impl defines `%s` twice", m.Name.Name).
					Label("duplicate method").
					Secondary(prev.Name.Loc, "first defined here")
				continue
			}
			info.Methods[m.Name.Name] = m
			c.fnSigs[m] = c.signature(m, params, self, nil)
		}
		c.impls = append(c.impls, info)
		c.indexImpl(info)
		c.checkImplCompleteness(info)
	}
	c.env = typeEnv{}
}

// indexImpl files an impl under the name of the type it is for.
func (c *Checker) indexImpl(info *ImplInfo) {
	key := selfKey(info.Self)
	c.implsBySelf[key] = append(c.implsBySelf[key], info)
}

// selfKey is the index key for a self type: a nominal type's name, or a primitive's.
func selfKey(t types.Type) string {
	switch v := types.Prune(t).(type) {
	case *types.Named:
		return v.Def.Name
	case *types.Prim:
		return v.Kind.String()
	case *types.TupleT:
		return "(tuple)"
	case *types.FnT:
		return "(fn)"
	}
	return "(other)"
}

// checkImplCompleteness verifies a trait impl defines everything the trait requires and
// nothing it does not (spec/06-traits-generics.md).
func (c *Checker) checkImplCompleteness(info *ImplInfo) {
	if info.Trait == nil {
		return
	}
	ti := info.Trait
	for _, name := range ti.AssocTypes {
		if _, ok := info.Assoc[name]; !ok {
			c.bag.Errorf("E0046", info.Decl.Span(), "missing associated type `%s` in this impl", name).
				Label("`type %s = ...;` is required", name).
				Secondary(ti.Decl.Name.Loc, "declared by trait `%s`", ti.Decl.Name.Name)
		}
	}
	for name := range info.Assoc {
		if !containsString(ti.AssocTypes, name) {
			c.bag.Errorf("E0046", info.Decl.Span(), "trait `%s` has no associated type `%s`", ti.Decl.Name.Name, name).
				Label("not declared by the trait").
				Secondary(ti.Decl.Name.Loc, "`%s` is declared here", ti.Decl.Name.Name).
				Note("an impl may only define the associated types its trait declares")
		}
	}
	for name, decl := range ti.Methods {
		if _, ok := info.Methods[name]; ok {
			continue
		}
		if decl.Body != nil {
			continue // the trait supplies a default
		}
		c.bag.Errorf("E0046", info.Decl.Span(), "missing method `%s` in this impl", name).
			Label("`%s` is required by trait `%s`", name, ti.Decl.Name.Name).
			Secondary(decl.Name.Loc, "declared here")
	}
	for name, m := range info.Methods {
		if _, ok := ti.Methods[name]; !ok {
			c.bag.Errorf("E0407", m.Name.Loc, "trait `%s` has no method `%s`", ti.Decl.Name.Name, name).
				Label("not a member of the trait").
				Note("an impl may only define methods the trait declares")
		}
	}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
