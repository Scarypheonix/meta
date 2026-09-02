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
	// ModuleRef names a module in the package's module tree.
	ModuleRef
	// PrimConst is an associated constant on a primitive type, such as `i64::MAX`.
	PrimConst
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
	Mod     *Module
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
	// Root is the package's root module.
	Root *Module
	// Enums maps an enum name to its declaration, so the interpreter can build prelude
	// values such as `Option::None` without going through a scope.
	Enums map[string]*ast.EnumDecl
	// Structs is the same for structs, so the interpreter can build the prelude's
	// concurrency handles (`JoinHandle`, `Sender`, `Receiver`, `Mutex`) whose values no
	// Origin expression constructs (spec/12-concurrency.md).
	Structs map[string]*ast.StructDecl
}

// Ref returns the resolution recorded for a node, and whether one exists.
func (r *Result) Ref(id ast.NodeID) (Ref, bool) {
	ref, ok := r.Refs[id]
	return ref, ok
}

// PrimitiveNames are the built-in type names, in scope everywhere. They are not
// declarations, so they cannot be shadowed by one: declaring `struct i64` is rejected
// as a duplicate.
var PrimitiveNames = []string{
	"i8", "i16", "i32", "i64",
	"u8", "u16", "u32", "u64",
	"f32", "f64",
	"bool", "char", "String",
	// Array is generic, unlike every other name here, and is a primitive for the same
	// reason String is: the compiler provides it and no source declares it (ADR-0028).
	"Array",
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
	// depth is the frame this lambda's own body runs in. A lambda captures a binding
	// only from a frame *outside* itself; one it declares locally is not a capture, and
	// recording it as one gives the closure a capture slot filled from a frame where the
	// binding does not exist yet.
	depth int
}

type resolver struct {
	bag *diag.Bag
	out *Result
	// root is the package's module tree; globals holds primitives, builtins and the
	// prelude's items; current is the module whose file is being resolved.
	root    *Module
	std     *Module
	globals *scope
	// preludeScope holds the prelude's own imports, layered over globals.
	preludeScope *scope
	current      *Module
	scope        *scope
	fnDepth      int
	lambdas      []*lambdaCtx
	// loopDepth guards `break` and `continue`.
	loopDepth int
}

// Input is one file to resolve, with the module path it lives at. The root module has
// the empty path; `lex/token.origin` under the source root has the path "lex::token".
type Input struct {
	Module  string
	File    *ast.File
	Prelude bool
}

// Files resolves files that all belong to the root module. It is the single-file entry
// point used by tests and by `originc run <file>`.
func Files(bag *diag.Bag, files ...*ast.File) *Result {
	inputs := make([]Input, 0, len(files))
	for _, f := range files {
		inputs = append(inputs, Input{File: f})
	}
	return Program(bag, inputs...)
}

// Program resolves a whole package: the prelude, the root module, and every submodule.
//
// Resolution happens in four passes, and the order is what makes module cycles legal
// (spec/07-modules.md): the module tree is built, then every module's items are
// declared, then imports are processed, then bodies are resolved. Nothing looks at a
// body until every name in the package exists.
func Program(bag *diag.Bag, inputs ...Input) *Result {
	r := &resolver{
		bag: bag,
		out: &Result{
			Refs:     map[ast.NodeID]Ref{},
			Bindings: map[ast.NodeID]*Local{},
			Captures: map[ast.NodeID][]*Local{},
			Fns:      map[string]*ast.FnDecl{},
			Enums:    map[string]*ast.EnumDecl{},
			Structs:  map[string]*ast.StructDecl{},
		},
	}

	r.root = newModule("", nil)
	r.out.Root = r.root
	r.globals = newScope(nil)
	// The prelude's items go into the global scope, which is what makes them visible
	// everywhere without a `use`. Its *imports* must not: `use std::thread` inside the
	// prelude is how its own method bodies reach the runtime's operations, and leaking
	// `thread` into every user module would make `use std::thread;` a no-op there. So
	// prelude bodies resolve in a scope layered over the globals, holding only its
	// imports.
	r.preludeScope = newScope(r.globals)
	for _, name := range PrimitiveNames {
		r.globals.names[name] = Ref{Kind: Prim, Name: name}
	}
	for name := range globalBuiltins {
		r.globals.names[name] = Ref{Kind: Builtin, Builtin: name, Name: name}
	}
	r.registerStdModules()

	// Pass 1: place every file in the module tree.
	var mods []*Module
	for _, in := range inputs {
		m := r.moduleAt(in.Module)
		m.Files = append(m.Files, in.File)
		m.Prelude = m.Prelude || in.Prelude
		mods = append(mods, m)
	}

	// Pass 2: declare each module's items into its own namespace. Prelude items go into
	// the global scope instead, which is what makes them visible without a `use`.
	for i, in := range inputs {
		m := mods[i]
		r.current = m
		if in.Prelude {
			r.scope = r.globals
			for _, it := range in.File.Items {
				r.declareItem(it)
			}
			continue
		}
		r.scope = m.Scope
		for _, it := range in.File.Items {
			r.declareItem(it)
			r.declareItemVisibility(it)
		}
	}

	// Pass 3: process imports, now that every module's items exist.
	for i, in := range inputs {
		r.current = mods[i]
		if in.Prelude {
			r.scope = r.preludeScope
		} else {
			r.scope = mods[i].Scope
		}
		for _, u := range in.File.Uses {
			r.resolveUse(u)
		}
	}

	// Pass 4: resolve bodies.
	for i, in := range inputs {
		r.current = mods[i]
		if in.Prelude {
			r.scope = r.preludeScope
		} else {
			r.scope = mods[i].Scope
		}
		for _, it := range in.File.Items {
			r.resolveItem(it)
		}
	}
	return r.out
}

// globalBuiltins are compiler-provided functions in scope everywhere, with no `use`.
var globalBuiltins = map[string]bool{
	"panic":  true,
	"ref_eq": true,
}

// stdModules are the compiler-provided modules of the standard library. They exist so
// that `use std::io;` and `io::println(..)` go through ordinary module resolution
// instead of a special case. Phase 7 replaces them with Origin source.
var stdModules = map[string][]string{
	"std::io": {"print", "println"},
	// Phase 7 (spec/13-collections.md). The operations on the one built-in collection;
	// `List` and `Map` are Origin source in the prelude, written in terms of these.
	"std::array": {"new", "len", "cap", "at", "set", "push", "truncate"},
	// `list::new` is the constructor `List::new` would be if the language had
	// associated functions (docs/deferred.md). It builds the prelude's own struct: the
	// operation exists to have somewhere to write the name, not because a runtime does
	// anything special for it.
	"std::list": {"new"},
	"std::map":  {"new"},
	// `hash::of` is specified, not private: three engines have to agree on it, so
	// spec/13-collections.md fixes the algorithm and the encoding rather than leaving
	// them to whoever writes the runtime.
	"std::hash": {"of"},
	// Phase 7 (spec/14-strings.md). The six operations on `String` whose bodies read or
	// allocate raw bytes; everything else about a string is Origin source in the prelude,
	// as the `Str` trait's default method bodies.
	"std::str": {"len", "byte_at", "slice", "concat", "char_at", "char_width"},
	// Phase 8 (spec/16-floats.md). The two operations that read a float's bits and put
	// them back. Rendering a float in decimal is Origin source in the prelude, written
	// over exactly these; nothing else in the language can see a float's representation.
	"std::float": {"bits", "from_bits"},
	// Phase 7 (spec/15-files.md). Four operations whose bodies are system calls. The
	// prelude's `read_to_string`, `write_string` and `file_exists` are what a program
	// calls; these are what those are written in terms of.
	"std::fs": {"read_file", "taken_text", "write_file", "file_exists"},
	// Phase 6 (spec/12-concurrency.md). `spawn` and `channel` are what a program calls;
	// the rest are the operations the prelude's own methods are written in terms of,
	// since a method body cannot otherwise reach an operation the runtime provides.
	"std::thread": {"spawn", "join_thread"},
	"std::chan":   {"channel", "send_value", "await_value", "taken_value", "close_sender"},
	"std::sync":   {"mutex", "with_lock"},
}

func (r *resolver) registerStdModules() {
	std := newModule("std", nil)
	r.std = std
	for path, names := range stdModules {
		m := std
		segs := splitPath(path)
		for _, seg := range segs[1:] {
			child, ok := m.Children[seg]
			if !ok {
				child = newModule(seg, m)
				m.Children[seg] = child
			}
			m = child
		}
		for _, name := range names {
			full := path[len("std::"):] + "::" + name
			m.Items[name] = Ref{Kind: Builtin, Builtin: full, Name: name}
			m.Pub[name] = true
		}
	}
}

func splitPath(p string) []string {
	if p == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i+1 < len(p); i++ {
		if p[i] == ':' && p[i+1] == ':' {
			out = append(out, p[start:i])
			start = i + 2
			i++
		}
	}
	return append(out, p[start:])
}

// moduleAt returns the module at a `::`-separated path, creating it if needed.
func (r *resolver) moduleAt(path string) *Module {
	m := r.root
	for _, seg := range splitPath(path) {
		child, ok := m.Children[seg]
		if !ok {
			child = newModule(seg, m)
			child.Scope = newScope(r.globals)
			m.Children[seg] = child
		}
		m = child
	}
	if m.Scope == nil {
		m.Scope = newScope(r.globals)
	}
	return m
}

// itemVisibility reports an item's name and whether it is `pub`.
func itemVisibility(it ast.Item) (ast.Ident, bool, bool) {
	switch v := it.(type) {
	case *ast.FnDecl:
		return v.Name, v.Pub, true
	case *ast.StructDecl:
		return v.Name, v.Pub, true
	case *ast.EnumDecl:
		return v.Name, v.Pub, true
	case *ast.TraitDecl:
		return v.Name, v.Pub, true
	case *ast.ConstDecl:
		return v.Name, v.Pub, true
	case *ast.TypeAliasDecl:
		return v.Name, v.Pub, true
	}
	return ast.Ident{}, false, false
}

func (r *resolver) declareItem(it ast.Item) {
	switch v := it.(type) {
	case *ast.FnDecl:
		r.declare(v.Name, Ref{Kind: Fn, Fn: v, Name: v.Name.Name})
		r.out.Fns[v.Name.Name] = v
	case *ast.StructDecl:
		r.declare(v.Name, Ref{Kind: Struct, Struct: v, Name: v.Name.Name})
		r.out.Structs[v.Name.Name] = v
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
		// An impl declares no top-level name at all: its methods are reached through the
		// receiver's type, which is the checker's question rather than the resolver's,
		// and the instance every call site reaches is monomorphization's (ADR-0010).
	case *ast.ErrorItem:
	}
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
	if r.current != nil {
		r.current.Items[name.Name] = ref
	}
}

// declareItemVisibility files an item's `pub` flag with its module.
func (r *resolver) declareItemVisibility(it ast.Item) {
	name, pub, ok := itemVisibility(it)
	if !ok || name.Name == "" || r.current == nil {
		return
	}
	r.current.Pub[name.Name] = pub
}

// resolveUse imports names from another module (spec/07-modules.md). Origin 0.1 has no
// glob imports and no renaming, so a `use` names either one item or a braced list of
// items from one module.
func (r *resolver) resolveUse(u *ast.Use) {
	if u.Path == nil || len(u.Path.Segments) == 0 {
		return
	}
	segs := u.Path.Segments

	if len(u.Names) > 0 {
		m := r.moduleByPath(segs, u.Path.Span())
		if m == nil {
			return
		}
		for _, name := range u.Names {
			r.importName(m, name)
		}
		return
	}

	// A single path: the last segment is either an item in the module named by the
	// prefix, or a module itself.
	if len(segs) >= 2 {
		if m := r.lookupModule(segs[:len(segs)-1]); m != nil {
			last := segs[len(segs)-1]
			if child, ok := m.Children[last.Name]; ok {
				r.scope.names[last.Name] = Ref{Kind: ModuleRef, Mod: child, Name: last.Name}
				return
			}
			r.importName(m, last)
			return
		}
	}
	if m := r.lookupModule(segs); m != nil {
		last := segs[len(segs)-1]
		r.scope.names[last.Name] = Ref{Kind: ModuleRef, Mod: m, Name: last.Name}
		return
	}
	r.bag.Errorf("E0432", u.Path.Span(), "cannot resolve import `%s`", u.Path).
		Label("no such module or item").
		Note("a module path names a file under the source root, or a module of `std`")
}

// importName brings one name from a module into the current scope, checking visibility.
func (r *resolver) importName(m *Module, name ast.Ident) {
	ref, pub, ok := m.Lookup(name.Name)
	if !ok {
		if child, isMod := m.Children[name.Name]; isMod {
			r.scope.names[name.Name] = Ref{Kind: ModuleRef, Mod: child, Name: name.Name}
			return
		}
		r.bag.Errorf("E0432", name.Loc, "%s has no item `%s`", m.Describe(), name.Name).
			Label("not found in that module")
		r.poison(name.Name)
		return
	}
	if !pub {
		r.reportPrivate(name.Loc, m, name.Name, ref)
		r.poison(name.Name)
		return
	}
	if prev, dup := r.scope.names[name.Name]; dup && prev != ref && prev.Kind != LocalVar {
		r.bag.Errorf("E0432", name.Loc, "`%s` is imported more than once", name.Name).
			Label("ambiguous import").
			Note("two imports bring different items into scope under this name")
		return
	}
	r.scope.names[name.Name] = ref
}

// poison binds a name that failed to import, so every later use of it resolves to the
// already-reported marker instead of producing a second diagnostic.
func (r *resolver) poison(name string) {
	if name == "" {
		return
	}
	r.scope.names[name] = Ref{Kind: Unresolved, Name: name}
}

// reportPrivate explains that an item exists but is not visible.
func (r *resolver) reportPrivate(at diag.Span, m *Module, name string, ref Ref) {
	d := r.bag.Errorf("E0603", at, "`%s` is private", name).
		Label("not visible outside %s", m.Describe()).
		Note("an item is private to its own module unless it is declared `pub`")
	if loc, ok := declSpan(ref); ok {
		d.Secondary(loc, "declared here, without `pub`")
	}
}

func declSpan(ref Ref) (diag.Span, bool) {
	switch ref.Kind {
	case Fn:
		return ref.Fn.Name.Loc, true
	case Struct:
		return ref.Struct.Name.Loc, true
	case Enum:
		return ref.Enum.Name.Loc, true
	case Trait:
		return ref.Trait.Name.Loc, true
	case Const:
		return ref.Const.Name.Loc, true
	case TypeAlias:
		return ref.Alias.Name.Loc, true
	}
	return diag.Span{}, false
}

// lookupModule resolves a module path: a name already in scope, then a sibling of the
// current module, then a child of the root, then `std`.
func (r *resolver) lookupModule(segs []ast.Ident) *Module {
	if len(segs) == 0 {
		return nil
	}
	first := segs[0].Name

	var start *Module
	switch {
	case first == "std":
		start = r.std
	default:
		if ref, ok := r.scope.lookup(first); ok && ref.Kind == ModuleRef {
			start = ref.Mod
		} else if r.current != nil && r.current.Parent != nil {
			if sib, ok := r.current.Parent.Children[first]; ok {
				start = sib
			}
		}
		if start == nil {
			if child, ok := r.root.Children[first]; ok {
				start = child
			}
		}
	}
	if start == nil {
		return nil
	}
	m := start
	for _, seg := range segs[1:] {
		child, ok := m.Children[seg.Name]
		if !ok {
			return nil
		}
		m = child
	}
	return m
}

// moduleByPath is lookupModule with a diagnostic when it fails.
func (r *resolver) moduleByPath(segs []ast.Ident, at diag.Span) *Module {
	m := r.lookupModule(segs)
	if m == nil {
		r.bag.Errorf("E0432", at, "cannot resolve module `%s`", pathText(segs)).
			Label("no such module").
			Note("a module path names a file under the source root, or a module of `std`")
	}
	return m
}

func pathText(segs []ast.Ident) string {
	out := ""
	for i, s := range segs {
		if i > 0 {
			out += "::"
		}
		out += s.Name
	}
	return out
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
// changes the wording of the diagnostic, because a bare name in a pattern would
// otherwise have bound rather than failed.
func (r *resolver) resolvePathIn(path *ast.Path, nodeID ast.NodeID, inPattern bool) {
	if path == nil || len(path.Segments) == 0 {
		r.out.Refs[nodeID] = Ref{Kind: Unresolved}
		return
	}
	segs := path.Segments
	first := segs[0]

	base, inScope := r.scope.lookup(first.Name)
	if inScope && len(segs) == 1 {
		if base.Kind == LocalVar {
			r.noteCapture(base.Local)
		}
		r.out.Refs[nodeID] = base
		return
	}

	if inScope && len(segs) == 2 {
		// `Self::Item` and `T::Item` are associated-type projections; which trait
		// supplies the member depends on bounds, so the checker finishes the job.
		if base.Kind == SelfTy || base.Kind == TypeParam {
			r.out.Refs[nodeID] = Ref{
				Kind: Assoc, BaseKind: base.Kind,
				Name: first.Name, Member: segs[1].Name,
			}
			return
		}
		if base.Kind == Enum {
			r.resolveVariant(base, segs[1], nodeID)
			return
		}
		if base.Kind == Prim {
			r.resolvePrimConst(base.Name, segs[1], nodeID)
			return
		}
	}

	// Otherwise the prefix must name a module.
	if m := r.lookupModule(segs[:len(segs)-1]); m != nil && len(segs) >= 2 {
		last := segs[len(segs)-1]
		ref, pub, ok := m.Lookup(last.Name)
		if ok {
			// An item is visible inside its own module even without `pub`.
			if !pub && m != r.current {
				r.reportPrivate(last.Loc, m, last.Name, ref)
				r.out.Refs[nodeID] = Ref{Kind: Unresolved}
				return
			}
			r.out.Refs[nodeID] = ref
			return
		}
		// `mod::Enum::Variant`
		if len(segs) >= 3 {
			if outer := r.lookupModule(segs[:len(segs)-2]); outer != nil {
				enumRef, pub, ok := outer.Lookup(segs[len(segs)-2].Name)
				if ok && enumRef.Kind == Enum {
					if !pub && outer != r.current {
						r.reportPrivate(segs[len(segs)-2].Loc, outer, segs[len(segs)-2].Name, enumRef)
						r.out.Refs[nodeID] = Ref{Kind: Unresolved}
						return
					}
					r.resolveVariant(enumRef, last, nodeID)
					return
				}
			}
		}
		r.bag.Errorf("E0433", last.Loc, "%s has no item `%s`", m.Describe(), last.Name).
			Label("not found in that module")
		r.out.Refs[nodeID] = Ref{Kind: Unresolved}
		return
	}

	if !inScope {
		r.reportUnresolved(path, first, inPattern)
		r.out.Refs[nodeID] = Ref{Kind: Unresolved}
		return
	}

	r.bag.Errorf("E0433", path.Span(), "cannot resolve `%s`", path).
		Label("unresolved path").
		Note("a qualified path is `module::item`, `Enum::Variant`, `Self::AssocType` or `T::AssocType`")
	r.out.Refs[nodeID] = Ref{Kind: Unresolved}
}

// PrimConsts are the associated constants every integer type carries. They exist so
// that the most negative value of a signed type can be written at all: a literal must
// fit the positive range, so `-128i8` is rejected and `i8::MIN` is how you say it
// (spec/01-lexical.md).
var PrimConsts = map[string]bool{"MIN": true, "MAX": true, "BITS": true}

func (r *resolver) resolvePrimConst(prim string, member ast.Ident, nodeID ast.NodeID) {
	if !PrimConsts[member.Name] {
		r.bag.Errorf("E0433", member.Loc, "`%s` has no associated constant `%s`", prim, member.Name).
			Label("no such constant").
			Note("every integer type has `MIN`, `MAX` and `BITS`")
		r.out.Refs[nodeID] = Ref{Kind: Unresolved}
		return
	}
	r.out.Refs[nodeID] = Ref{Kind: PrimConst, Name: prim, Member: member.Name}
}

// resolveVariant records a reference to one variant of an enum.
func (r *resolver) resolveVariant(enumRef Ref, want ast.Ident, nodeID ast.NodeID) {
	for _, va := range enumRef.Enum.Variants {
		if va.Name.Name == want.Name {
			r.out.Refs[nodeID] = Ref{
				Kind: Variant, Enum: enumRef.Enum, Variant: va,
				Name: enumRef.Enum.Name.Name + "::" + va.Name.Name,
			}
			return
		}
	}
	r.bag.Errorf("E0433", want.Loc, "enum `%s` has no variant `%s`", enumRef.Enum.Name.Name, want.Name).
		Label("no such variant").
		Note("`%s` declares %s", enumRef.Enum.Name.Name, variantList(enumRef.Enum))
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
	} else {
		b.Note("a name must be declared in this module, imported with `use`, or come from the prelude")
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
		// Only the lambdas *between* the binding's frame and this reference capture it.
		// A lambda whose own frame declares the binding reads it as a local; recording a
		// capture there too gave it a capture slot that had to be filled at the point the
		// closure was built, which is before the binding exists -- so it was filled with
		// zero, and every read through it was silently wrong.
		if lc.depth <= local.fnDepth {
			continue
		}
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
		*ast.SelfExpr, *ast.ErrorExpr:
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
				Label("not inside a loop").
				Note("`break` and `continue` bind to the innermost enclosing loop")
		}
		r.resolveExpr(v.Value)

	case *ast.Continue:
		if r.loopDepth == 0 {
			r.bag.Errorf("E0433", v.Span(), "`continue` outside of a loop").
				Label("not inside a loop").
				Note("`break` and `continue` bind to the innermost enclosing loop")
		}

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
	saved, savedDepth, savedLoop := r.scope, r.fnDepth, r.loopDepth
	r.scope = newScope(r.scope)
	r.fnDepth++

	lc := &lambdaCtx{node: l.NodeID(), captured: map[*Local]bool{}, depth: r.fnDepth}
	r.lambdas = append(r.lambdas, lc)
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
