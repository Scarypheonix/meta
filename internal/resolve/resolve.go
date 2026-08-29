// Package resolve binds every identifier occurrence to what it names.
//
// Its output is the single source of truth for names (spec/07-modules.md): later passes
// look up a NodeID in the Result's side tables and never re-consult a scope. Nothing
// here writes into the AST.
//
// Phase 1 resolves one file plus the prelude. The filesystem module tree and cross-file
// visibility arrive in Phase 2; until then a `use` of a name the prelude does not
// provide resolves to a builtin or is reported.
package resolve

import (
	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/diag"
)

// Kind says what an identifier occurrence names.
type Kind int

const (
	// Unresolved means the name was not found; a diagnostic was already reported.
	Unresolved Kind = iota
	// LocalVar is a binding introduced by a pattern.
	LocalVar
	// Fn is a function item.
	Fn
	// Struct is a struct declaration, used as a constructor or a type.
	Struct
	// Enum is an enum declaration, used as a type.
	Enum
	// Variant is one variant of an enum.
	Variant
	// Const is a constant item.
	Const
	// Builtin is a compiler-provided function such as `io::println`.
	Builtin
	// TypeParam is a generic parameter in scope.
	TypeParam
	// Trait is a trait declaration.
	Trait
	// TypeAlias is a `type` alias.
	TypeAlias
	// Prim is a primitive type name such as `i64` or `bool`.
	Prim
	// SelfTy is `Self` inside a trait or impl.
	SelfTy
	// Assoc is an associated-type projection such as `Self::Item` or `T::Item`. The
	// resolver records the base and the member; deciding which trait provides the
	// member needs types, so it belongs to the checker.
	Assoc
)

// Local is one binding. Identity is the pointer: two Locals with the same name in
// different scopes are different bindings, which is what makes shadowing work.
type Local struct {
	Name string
	Mut  bool
	Decl diag.Span
	// fnDepth is the function nesting level the binding belongs to. A reference from a
	// deeper level is a capture.
	fnDepth int
}

// Ref is what one identifier occurrence resolves to. Exactly one of the pointer fields
// is set, according to Kind.
type Ref struct {
	Kind    Kind
	Local   *Local
	Fn      *ast.FnDecl
	Struct  *ast.StructDecl
	Enum    *ast.EnumDecl
	Variant *ast.Variant
	Const   *ast.ConstDecl
	Trait   *ast.TraitDecl
	Alias   *ast.TypeAliasDecl
	Builtin string
	Name    string
	// BaseKind and Member describe an associated-type projection (Kind == Assoc):
	// BaseKind is SelfTy or TypeParam, Name is the base's name, Member is the
	// projected associated type.
	BaseKind Kind
	Member   string
}

// Result is the resolver's output, keyed by AST node id.
type Result struct {
	// Refs maps a PathExpr, PathPat, BindPat or StructLit node to what it names.
	Refs map[ast.NodeID]Ref
	// Bindings maps a BindPat node that introduces a binding to that binding.
	Bindings map[ast.NodeID]*Local
	// Captures maps a Lambda node to the enclosing-frame bindings it captures by value.
	Captures map[ast.NodeID][]*Local
	// Fns maps a function name to its declaration, for the interpreter's entry point.
	Fns map[string]*ast.FnDecl
	// Enums maps an enum name to its declaration, so the interpreter can build prelude
	// values such as `Option::None` without going through a scope.
	Enums map[string]*ast.EnumDecl
	// Methods maps a type name to its inherent and trait-impl methods. Phase 1 keys by
	// the impl's type name; Phase 2 replaces this with real trait resolution.
	Methods map[string]map[string]*ast.FnDecl
}

// Ref returns the resolution recorded for a node, and whether one exists.
func (r *Result) Ref(id ast.NodeID) (Ref, bool) {
	ref, ok := r.Refs[id]
	return ref, ok
}

// builtins are the compiler-provided functions available in Phase 1. Each is
// implemented by the interpreter; the standard library written in Origin arrives in
// Phase 7.
var builtins = map[string]bool{
	"io::print":   true,
	"io::println": true,
	"panic":       true,
	"ref_eq":      true,
}

// PrimitiveNames are the built-in type names, in scope everywhere. They are not
// declarations, so they cannot be shadowed by one: declaring `struct i64` is rejected
// as a duplicate.
var PrimitiveNames = []string{
	"i8", "i16", "i32", "i64",
	"u8", "u16", "u32", "u64",
	"f32", "f64",
	"bool", "char", "String",
}

type scope struct {
	names  map[string]Ref
	parent *scope
}

func newScope(parent *scope) *scope {
	return &scope{names: map[string]Ref{}, parent: parent}
}

func (s *scope) lookup(name string) (Ref, bool) {
	for cur := s; cur != nil; cur = cur.parent {
		if r, ok := cur.names[name]; ok {
			return r, true
		}
	}
	return Ref{}, false
}

// lambdaCtx accumulates the captures of one lambda being resolved.
type lambdaCtx struct {
	node     ast.NodeID
	captured map[*Local]bool
	order    []*Local
}

type resolver struct {
	bag     *diag.Bag
	out     *Result
	scope   *scope
	fnDepth int
	lambdas []*lambdaCtx
	// loopDepth guards `break` and `continue`.
	loopDepth int
}

// Files resolves the given files as one unit, with the prelude first. It always returns
// a Result; check bag.HasErrors before trusting it.
func Files(bag *diag.Bag, files ...*ast.File) *Result {
	r := &resolver{
		bag: bag,
		out: &Result{
			Refs:     map[ast.NodeID]Ref{},
			Bindings: map[ast.NodeID]*Local{},
			Captures: map[ast.NodeID][]*Local{},
			Fns:      map[string]*ast.FnDecl{},
			Enums:    map[string]*ast.EnumDecl{},
			Methods:  map[string]map[string]*ast.FnDecl{},
		},
	}
	r.scope = newScope(nil)
	for _, name := range PrimitiveNames {
		r.scope.names[name] = Ref{Kind: Prim, Name: name}
	}

	// Items are collected before any body is resolved, so that recursion and mutual
	// recursion work without forward declarations (spec/07-modules.md: module cycles
	// are permitted, and so is any ordering within a module).
	for _, f := range files {
		for _, it := range f.Items {
			r.declareItem(it)
		}
	}
	for _, f := range files {
		for _, it := range f.Items {
			r.resolveItem(it)
		}
	}
	return r.out
}

func (r *resolver) declareItem(it ast.Item) {
	switch v := it.(type) {
	case *ast.FnDecl:
		r.declare(v.Name, Ref{Kind: Fn, Fn: v, Name: v.Name.Name})
		r.out.Fns[v.Name.Name] = v
	case *ast.StructDecl:
		r.declare(v.Name, Ref{Kind: Struct, Struct: v, Name: v.Name.Name})
	case *ast.EnumDecl:
		r.declare(v.Name, Ref{Kind: Enum, Enum: v, Name: v.Name.Name})
		r.out.Enums[v.Name.Name] = v
	case *ast.ConstDecl:
		r.declare(v.Name, Ref{Kind: Const, Const: v, Name: v.Name.Name})
	case *ast.TraitDecl:
		r.declare(v.Name, Ref{Kind: Trait, Trait: v, Name: v.Name.Name})
	case *ast.TypeAliasDecl:
		r.declare(v.Name, Ref{Kind: TypeAlias, Alias: v, Name: v.Name.Name})
	case *ast.ImplDecl:
		// An impl declares no top-level name; its methods are found through the type.
		name := implTypeName(v.Type)
		if name == "" {
			return
		}
		if r.out.Methods[name] == nil {
			r.out.Methods[name] = map[string]*ast.FnDecl{}
		}
		for _, m := range v.Methods {
			if prev, dup := r.out.Methods[name][m.Name.Name]; dup {
				r.bag.Errorf("E0034", m.Name.Loc, "`%s` has two methods named `%s`", name, m.Name.Name).
					Label("duplicate method").
					Secondary(prev.Name.Loc, "first defined here")
				continue
			}
			r.out.Methods[name][m.Name.Name] = m
		}
	case *ast.ErrorItem:
	}
}

// implTypeName returns the name an impl attaches its methods to, or "" when the impl's
// type is not a simple path (which Phase 1 does not support).
func implTypeName(t ast.Type) string {
	pt, ok := t.(*ast.PathType)
	if !ok || pt.Path == nil || len(pt.Path.Segments) == 0 {
		return ""
	}
	return pt.Path.Last().Name
}

func (r *resolver) declare(name ast.Ident, ref Ref) {
	if name.Name == "" {
		return // the parser already reported a missing name
	}
	if prev, ok := r.scope.names[name.Name]; ok && prev.Kind != LocalVar {
		r.bag.Errorf("E0433", name.Loc, "`%s` is declared more than once in this module", name.Name).
			Label("duplicate declaration").
			Note("each name may be declared once per module")
		return
	}
	r.scope.names[name.Name] = ref
}

func (r *resolver) resolveItem(it ast.Item) {
	switch v := it.(type) {
	case *ast.FnDecl:
		r.resolveFn(v)

	case *ast.ConstDecl:
		r.resolveTypeExpr(v.Type)
		if v.Value != nil {
			r.resolveExpr(v.Value)
		}

	case *ast.StructDecl:
		r.withGenerics(v.Generics, false, func() {
			r.resolveWhere(v.Where)
			for _, f := range v.Fields {
				r.resolveTypeExpr(f.Type)
			}
		})

	case *ast.EnumDecl:
		r.withGenerics(v.Generics, false, func() {
			r.resolveWhere(v.Where)
			for _, va := range v.Variants {
				for _, t := range va.Types {
					r.resolveTypeExpr(t)
				}
				for _, f := range va.Fields {
					r.resolveTypeExpr(f.Type)
				}
			}
		})

	case *ast.TypeAliasDecl:
		r.withGenerics(v.Generics, false, func() { r.resolveTypeExpr(v.Type) })

	case *ast.TraitDecl:
		r.withGenerics(v.Generics, true, func() {
			for _, st := range v.Supertraits {
				r.resolveTraitRef(st)
			}
			r.resolveWhere(v.Where)
			for _, at := range v.AssocTypes {
				for _, b := range at.Bounds {
					r.resolveTraitRef(b)
				}
			}
			for _, m := range v.Methods {
				r.resolveFn(m)
			}
		})

	case *ast.ImplDecl:
		r.withGenerics(v.Generics, true, func() {
			if v.Trait != nil {
				r.resolveTraitRef(v.Trait)
			}
			r.resolveTypeExpr(v.Type)
			r.resolveWhere(v.Where)
			for _, at := range v.AssocTypes {
				r.resolveTypeExpr(at.Type)
			}
			for _, m := range v.Methods {
				r.resolveFn(m)
			}
		})
	}
}

// withGenerics runs f with the given type parameters, and optionally `Self`, in scope.
func (r *resolver) withGenerics(gs []*ast.GenericParam, withSelf bool, f func()) {
	saved := r.scope
	r.scope = newScope(r.scope)
	if withSelf {
		r.scope.names["Self"] = Ref{Kind: SelfTy, Name: "Self"}
	}
	for _, g := range gs {
		r.scope.names[g.Name.Name] = Ref{Kind: TypeParam, Name: g.Name.Name}
		for _, b := range g.Bounds {
			r.resolveTraitRef(b)
		}
	}
	f()
	r.scope = saved
}

func (r *resolver) resolveWhere(preds []*ast.WherePred) {
	for _, w := range preds {
		r.resolveTypeExpr(w.Type)
		for _, b := range w.Bounds {
			r.resolveTraitRef(b)
		}
	}
}

func (r *resolver) resolveTraitRef(tr *ast.TraitRef) {
	if tr == nil {
		return
	}
	r.resolvePathIn(tr.Path, tr.NodeID(), false)
	for _, a := range tr.Args {
		r.resolveTypeExpr(a)
	}
}

// resolveTypeExpr resolves the names inside a syntactic type. The checker reads the
// result from the side table and never consults a scope itself (spec/07-modules.md).
func (r *resolver) resolveTypeExpr(t ast.Type) {
	switch v := t.(type) {
	case nil, *ast.ErrorType, *ast.UnitType, *ast.SelfType:
		return
	case *ast.PathType:
		r.resolvePathIn(v.Path, v.NodeID(), false)
		for _, a := range v.Args {
			r.resolveTypeExpr(a)
		}
	case *ast.TupleType:
		for _, e := range v.Elems {
			r.resolveTypeExpr(e)
		}
	case *ast.FnType:
		for _, p := range v.Params {
			r.resolveTypeExpr(p)
		}
		r.resolveTypeExpr(v.Ret)
	}
}

func (r *resolver) resolveFn(fn *ast.FnDecl) {
	// A trait's required method has no body, but its signature still needs resolving.
	saved, savedDepth, savedLoop := r.scope, r.fnDepth, r.loopDepth
	r.scope = newScope(r.scope)
	r.fnDepth++
	r.loopDepth = 0

	for _, g := range fn.Generics {
		r.scope.names[g.Name.Name] = Ref{Kind: TypeParam, Name: g.Name.Name}
		for _, b := range g.Bounds {
			r.resolveTraitRef(b)
		}
	}
	r.resolveWhere(fn.Where)
	for _, p := range fn.Params {
		r.resolveTypeExpr(p.Type)
		r.bindPattern(p.Pat, p.Mut)
	}
	r.resolveTypeExpr(fn.Ret)
	if fn.Body != nil {
		r.resolveBlock(fn.Body)
	}

	r.scope, r.fnDepth, r.loopDepth = saved, savedDepth, savedLoop
}

// ---------------------------------------------------------------------------
// Patterns
// ---------------------------------------------------------------------------

// bindPattern introduces the bindings a pattern declares. A bare name that resolves to
// a unit variant or a constant matches that value instead of binding
// (spec/02-grammar.md); that decision is made here, not in the parser.
func (r *resolver) bindPattern(p ast.Pattern, mut bool) {
	switch v := p.(type) {
	case nil:
		return
	case *ast.WildcardPat, *ast.ErrorPat:
		return

	case *ast.LitPat:
		return

	case *ast.BindPat:
		if !v.Mut && !mut {
			if ref, ok := r.scope.lookup(v.Name.Name); ok && isConstructorLike(ref) {
				r.out.Refs[v.NodeID()] = ref
				if v.Sub != nil {
					r.bindPattern(v.Sub, false)
				}
				return
			}
		}
		local := &Local{Name: v.Name.Name, Mut: v.Mut || mut, Decl: v.Name.Loc, fnDepth: r.fnDepth}
		r.scope.names[v.Name.Name] = Ref{Kind: LocalVar, Local: local, Name: local.Name}
		r.out.Bindings[v.NodeID()] = local
		r.out.Refs[v.NodeID()] = Ref{Kind: LocalVar, Local: local, Name: local.Name}
		if v.Sub != nil {
			r.bindPattern(v.Sub, mut)
		}

	case *ast.PathPat:
		r.resolvePathIn(v.Path, v.NodeID(), true)
		for _, e := range v.Elems {
			r.bindPattern(e, mut)
		}
		for _, f := range v.Fields {
			r.bindPattern(f.Pat, mut)
		}

	case *ast.TuplePat:
		for _, e := range v.Elems {
			r.bindPattern(e, mut)
		}

	case *ast.OrPat:
		// Every alternative must bind the same names; that check is Phase 2's
		// (spec/05-patterns.md, E0007). Here each alternative is simply resolved.
		for _, a := range v.Alts {
			r.bindPattern(a, mut)
		}
	}
}

func isConstructorLike(ref Ref) bool {
	return ref.Kind == Variant || ref.Kind == Const
}

// ---------------------------------------------------------------------------
// Paths
// ---------------------------------------------------------------------------

// resolvePathIn resolves a path and records the result against nodeID. inPattern
// suppresses the "did you mean a binding" phrasing.
func (r *resolver) resolvePathIn(path *ast.Path, nodeID ast.NodeID, inPattern bool) {
	if path == nil || len(path.Segments) == 0 {
		r.out.Refs[nodeID] = Ref{Kind: Unresolved}
		return
	}
	full := path.String()
	if builtins[full] {
		r.out.Refs[nodeID] = Ref{Kind: Builtin, Builtin: full, Name: full}
		return
	}

	first := path.Segments[0]
	base, ok := r.scope.lookup(first.Name)
	if !ok {
		r.reportUnresolved(path, first, inPattern)
		r.out.Refs[nodeID] = Ref{Kind: Unresolved}
		return
	}
	if len(path.Segments) == 1 {
		if base.Kind == LocalVar {
			r.noteCapture(base.Local)
		}
		r.out.Refs[nodeID] = base
		return
	}

	// `Self::Item` and `T::Item` are associated-type projections. Which trait supplies
	// the member depends on bounds, so the checker finishes the job.
	if (base.Kind == SelfTy || base.Kind == TypeParam) && len(path.Segments) == 2 {
		r.out.Refs[nodeID] = Ref{
			Kind: Assoc, BaseKind: base.Kind,
			Name: first.Name, Member: path.Segments[1].Name,
		}
		return
	}

	// Two segments: `Enum::Variant` is the other qualified form.
	if base.Kind == Enum && len(path.Segments) == 2 {
		want := path.Segments[1]
		for _, va := range base.Enum.Variants {
			if va.Name.Name == want.Name {
				r.out.Refs[nodeID] = Ref{Kind: Variant, Enum: base.Enum, Variant: va, Name: full}
				return
			}
		}
		r.bag.Errorf("E0433", want.Loc, "enum `%s` has no variant `%s`", base.Enum.Name.Name, want.Name).
			Label("no such variant").
			Note("`%s` declares %s", base.Enum.Name.Name, variantList(base.Enum))
		r.out.Refs[nodeID] = Ref{Kind: Unresolved}
		return
	}

	r.bag.Errorf("E0433", path.Span(), "cannot resolve `%s`", full).
		Label("unresolved path").
		Note("a qualified path is `Enum::Variant`, `Self::AssocType` or `T::AssocType`")
	r.out.Refs[nodeID] = Ref{Kind: Unresolved}
}

func variantList(e *ast.EnumDecl) string {
	out := ""
	for i, v := range e.Variants {
		if i > 0 {
			out += ", "
		}
		out += "`" + v.Name.Name + "`"
	}
	if out == "" {
		return "no variants"
	}
	return out
}

func (r *resolver) reportUnresolved(path *ast.Path, first ast.Ident, inPattern bool) {
	b := r.bag.Errorf("E0433", first.Loc, "cannot find `%s` in this scope", first.Name).
		Label("not found")
	if inPattern {
		b.Note("a name in a pattern binds a new variable unless it names a unit variant or a constant")
	}
	_ = path
}

// noteCapture records that the lambdas between the binding's frame and the current one
// capture it by value (spec/04-expressions.md).
func (r *resolver) noteCapture(local *Local) {
	if local.fnDepth >= r.fnDepth {
		return // same frame: an ordinary local reference
	}
	for _, lc := range r.lambdas {
		if !lc.captured[local] {
			lc.captured[local] = true
			lc.order = append(lc.order, local)
		}
	}
}

// ---------------------------------------------------------------------------
// Expressions and statements
// ---------------------------------------------------------------------------

func (r *resolver) resolveBlock(b *ast.Block) {
	if b == nil {
		return
	}
	saved := r.scope
	r.scope = newScope(r.scope)

	// Items declared in a block are visible throughout it, like items in a module.
	for _, s := range b.Stmts {
		if is, ok := s.(*ast.ItemStmt); ok {
			r.declareItem(is.Item)
		}
	}
	for _, s := range b.Stmts {
		r.resolveStmt(s)
	}
	if b.Tail != nil {
		r.resolveExpr(b.Tail)
	}
	r.scope = saved
}

func (r *resolver) resolveStmt(s ast.Stmt) {
	switch v := s.(type) {
	case *ast.LetStmt:
		r.resolveTypeExpr(v.Type)
		// The initializer is resolved before the pattern binds, so `let x = x;` refers
		// to the outer `x`.
		if v.Value != nil {
			r.resolveExpr(v.Value)
		}
		r.bindPattern(v.Pat, v.Mut)
	case *ast.ExprStmt:
		r.resolveExpr(v.X)
	case *ast.ItemStmt:
		r.resolveItem(v.Item)
	}
}

func (r *resolver) resolveExpr(e ast.Expr) {
	switch v := e.(type) {
	case nil:
		return
	case *ast.IntLit, *ast.FloatLit, *ast.StrLit, *ast.CharLit, *ast.BoolLit,
		*ast.SelfExpr, *ast.ErrorExpr, *ast.Continue:
		return

	case *ast.PathExpr:
		r.resolvePathIn(v.Path, v.NodeID(), false)
		for _, a := range v.Args {
			r.resolveTypeExpr(a)
		}

	case *ast.StructLit:
		r.resolvePathIn(v.Path, v.NodeID(), false)
		for _, a := range v.Args {
			r.resolveTypeExpr(a)
		}
		for _, f := range v.Fields {
			r.resolveExpr(f.Value)
		}

	case *ast.TupleExpr:
		for _, el := range v.Elems {
			r.resolveExpr(el)
		}

	case *ast.Lambda:
		r.resolveLambda(v)

	case *ast.Block:
		r.resolveBlock(v)

	case *ast.If:
		r.resolveExpr(v.Cond)
		r.resolveBlock(v.Then)
		r.resolveExpr(v.Else)

	case *ast.Match:
		r.resolveExpr(v.Scrutinee)
		for _, arm := range v.Arms {
			saved := r.scope
			r.scope = newScope(r.scope)
			r.bindPattern(arm.Pat, false)
			if arm.Guard != nil {
				r.resolveExpr(arm.Guard)
			}
			r.resolveExpr(arm.Body)
			r.scope = saved
		}

	case *ast.While:
		r.resolveExpr(v.Cond)
		r.loopDepth++
		r.resolveBlock(v.Body)
		r.loopDepth--

	case *ast.For:
		r.resolveExpr(v.Iter)
		saved := r.scope
		r.scope = newScope(r.scope)
		r.bindPattern(v.Pat, false)
		r.loopDepth++
		r.resolveBlock(v.Body)
		r.loopDepth--
		r.scope = saved

	case *ast.Loop:
		r.loopDepth++
		r.resolveBlock(v.Body)
		r.loopDepth--

	case *ast.Break:
		if r.loopDepth == 0 {
			r.bag.Errorf("E0433", v.Span(), "`break` outside of a loop").
				Label("not inside a loop")
		}
		r.resolveExpr(v.Value)

	case *ast.Return:
		r.resolveExpr(v.Value)

	case *ast.Unary:
		r.resolveExpr(v.X)

	case *ast.Binary:
		r.resolveExpr(v.L)
		r.resolveExpr(v.R)

	case *ast.Assign:
		r.resolveExpr(v.Place)
		r.resolveExpr(v.Value)
		r.checkAssignable(v)

	case *ast.Cast:
		r.resolveExpr(v.X)
		r.resolveTypeExpr(v.Type)

	case *ast.Call:
		r.resolveExpr(v.Fn)
		for _, a := range v.Args {
			r.resolveExpr(a)
		}

	case *ast.MethodCall:
		r.resolveExpr(v.Recv)
		for _, a := range v.Args {
			r.resolveExpr(a)
		}

	case *ast.FieldAccess:
		r.resolveExpr(v.Recv)

	case *ast.Try:
		r.resolveExpr(v.X)
	}
}

// checkAssignable enforces the part of the place rule that is decidable without types:
// a local binding must be declared `mut`. Field mutability needs the receiver's type
// and is enforced by the interpreter in Phase 1, then statically in Phase 2.
func (r *resolver) checkAssignable(a *ast.Assign) {
	switch place := a.Place.(type) {
	case *ast.PathExpr:
		ref, ok := r.out.Refs[place.NodeID()]
		if !ok || ref.Kind == Unresolved {
			return // already reported
		}
		if ref.Kind != LocalVar {
			r.bag.Errorf("E0594", place.Span(), "cannot assign to `%s`", place.Path).
				Label("not a place").
				Note("only a `mut` binding or a `mut` field can be assigned")
			return
		}
		if !ref.Local.Mut {
			r.bag.Errorf("E0594", place.Span(), "cannot assign to `%s`, which is not declared `mut`", ref.Local.Name).
				Label("cannot assign twice to an immutable binding").
				Secondary(ref.Local.Decl, "`%s` is bound here", ref.Local.Name).
				Help("declare it as `let mut %s`", ref.Local.Name)
		}
	case *ast.FieldAccess:
		// Checked at run time in Phase 1; statically in Phase 2 once field types exist.
	default:
		r.bag.Errorf("E0594", a.Place.Span(), "cannot assign to this expression").
			Label("not a place").
			Note("a place is a `mut` binding or a `mut` field (spec/04-expressions.md)")
	}
}

func (r *resolver) resolveLambda(l *ast.Lambda) {
	lc := &lambdaCtx{node: l.NodeID(), captured: map[*Local]bool{}}
	r.lambdas = append(r.lambdas, lc)

	saved, savedDepth, savedLoop := r.scope, r.fnDepth, r.loopDepth
	r.scope = newScope(r.scope)
	r.fnDepth++
	r.loopDepth = 0

	for _, p := range l.Params {
		r.resolveTypeExpr(p.Type)
		r.bindPattern(p.Pat, false)
	}
	r.resolveTypeExpr(l.Ret)
	r.resolveExpr(l.Body)

	r.scope, r.fnDepth, r.loopDepth = saved, savedDepth, savedLoop
	r.lambdas = r.lambdas[:len(r.lambdas)-1]
	r.out.Captures[l.NodeID()] = lc.order
}
