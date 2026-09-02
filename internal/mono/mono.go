// Package mono computes a program's monomorphization: which specialized copy of each
// generic function the program actually needs, and which copy every call site reaches.
//
// ADR-0010 chose monomorphization over dictionaries, so this is where a type variable
// stops existing. The rest of the compiler reads the result rather than reasoning about
// generics itself: the bytecode compiler emits one function per instance and looks a
// call site up here, and the backend of Phase 5 will never see a type parameter at all.
//
// It runs as part of checking rather than of code generation, because rejecting
// polymorphic recursion (E0055, spec/06-traits-generics.md) is a decision about whether
// a program is legal, and `originc check` must reach the same verdict as `originc run`.
package mono

import (
	"fmt"
	"strings"

	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/check"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/prelude"
	"github.com/scarypheonix/meta/internal/types"
)

// maxDepth bounds the instantiation chain. Polymorphic recursion — a generic function
// that calls itself at a strictly larger type — has an infinite instantiation set, and
// the depth limit is how spec/06-traits-generics.md says to detect it.
const maxDepth = 64

// Instance is one specialized copy of a function.
type Instance struct {
	// Decl is the function this is a copy of.
	Decl *ast.FnDecl
	// Args are the types its generic parameters were instantiated at, parallel to
	// check.Result.Generics[Decl]. It is empty for a function that is not generic,
	// which therefore has exactly one instance.
	Args []types.Type
	// Subst maps each of those parameters to its type. Compiling the body reads types
	// from the checker's side tables and substitutes through this, so every type the
	// code generator sees is concrete.
	Subst map[*types.Param]types.Type
	// Name is what the instance is called in bytecode dumps: `max2[Money]`.
	Name string
	// Calls maps a call site inside this body to the instance it reaches. A call with
	// no entry is one no instance can be named for: a method whose impl is provided by
	// the compiler, which the code generator lowers to a builtin.
	Calls map[ast.NodeID]*Instance
	// IterCalls maps a `for` loop to the `into_iter` instance it reaches. It is separate
	// from Calls because the loop's other implicit call, `next`, is keyed by the same
	// node (internal/check's Result explains why neither can move).
	IterCalls map[ast.NodeID]*Instance

	key    string
	depth  int
	parent *Instance
}

// Result is the whole program's instantiation set.
type Result struct {
	// Instances are in a deterministic order: the roots in declaration order, then
	// each instance the walk discovered, in the order it was discovered.
	Instances []*Instance
	// Entry is the instance of `main`.
	Entry *Instance

	byKey map[string]*Instance
}

// Lookup returns the instance a call site inside from reaches, if there is one.
func (r *Result) Lookup(from *Instance, node ast.NodeID) (*Instance, bool) {
	if from == nil {
		return nil, false
	}
	inst, ok := from.Calls[node]
	return inst, ok
}

// LookupIter finds the `into_iter` instance one `for` loop reaches, from the table that
// exists because the loop's two implicit calls share a node (internal/check's Result).
func (r *Result) LookupIter(from *Instance, node ast.NodeID) (*Instance, bool) {
	if from == nil {
		return nil, false
	}
	inst, ok := from.IterCalls[node]
	return inst, ok
}

type walker struct {
	bag   *diag.Bag
	tys   *check.Result
	out   *Result
	queue []*Instance
}

// Program computes the instantiation set of a checked program.
func Program(bag *diag.Bag, tys *check.Result, files ...*ast.File) *Result {
	w := &walker{
		bag: bag, tys: tys,
		out: &Result{byKey: map[string]*Instance{}},
	}

	// Every function that is not generic is a root: it is compiled whether or not
	// anything calls it, exactly as before monomorphization existed. Generic functions
	// have no code of their own and appear only through a call site.
	for _, f := range files {
		for _, it := range f.Items {
			// The prelude is a library, not a program: a declaration in it is compiled
			// because something reaches it, and one nothing reaches is not compiled at
			// all. Rooting it the way a user's own declaration is rooted was free while
			// the prelude's non-generic items were a handful, and stopped being free the
			// moment it grew -- every program, string-free ones included, carried
			// `impl Str for String`'s six methods and the UTF-8 validator behind
			// `read_to_string`.
			if isPrelude(it) {
				continue
			}
			switch v := it.(type) {
			case *ast.FnDecl:
				w.root(v)
			case *ast.ImplDecl:
				for _, m := range v.Methods {
					w.root(m)
				}
			case *ast.TraitDecl:
				for _, m := range v.Methods {
					w.root(m)
				}
			}
		}
	}

	for len(w.queue) > 0 {
		inst := w.queue[0]
		w.queue = w.queue[1:]
		w.walkBody(inst)
	}
	return w.out
}

// isPrelude reports whether an item was declared in the prelude, by the file its span
// names. It is the same test internal/check's DeclaredInPrelude makes, for the same
// reason: the prelude is source like any other and is told apart by where it came from.
func isPrelude(it ast.Item) bool {
	s := it.Span()
	return s.Valid() && s.File != nil && s.File.Name == prelude.Name
}

func (w *walker) root(decl *ast.FnDecl) {
	if decl.Body == nil || len(w.tys.Generics[decl]) > 0 {
		return
	}
	inst := w.instance(decl, nil, nil, nil, diag.Span{})
	if inst != nil && decl.Name.Name == "main" && decl.Self == nil && w.out.Entry == nil {
		w.out.Entry = inst
	}
}

// instance finds or creates the copy of decl for one tuple of type arguments.
func (w *walker) instance(decl *ast.FnDecl, params []*types.Param, args []types.Type, from *Instance, span diag.Span) *Instance {
	key := instanceKey(decl, args)
	if inst, ok := w.out.byKey[key]; ok {
		return inst
	}

	depth := 0
	if from != nil {
		depth = from.depth
		if len(args) > 0 {
			depth = from.depth + 1
		}
	}
	if depth > maxDepth {
		w.reportDepth(decl, args, from, span)
		return nil
	}

	subst := map[*types.Param]types.Type{}
	for i, p := range params {
		if i < len(args) {
			subst[p] = args[i]
		}
	}
	inst := &Instance{
		Decl: decl, Args: args, Subst: subst,
		Name:      instanceName(decl, args),
		Calls:     map[ast.NodeID]*Instance{},
		IterCalls: map[ast.NodeID]*Instance{},
		key:       key,
		depth:     depth,
		parent:    from,
	}
	w.out.byKey[key] = inst
	w.out.Instances = append(w.out.Instances, inst)
	w.queue = append(w.queue, inst)
	return inst
}

// walkBody resolves every call site in one instance's body.
//
// Two tables, because a `for` loop makes two calls the source does not write and only one
// node to hang them off: `next` is in Insts with every other call site, and `into_iter` is
// in IterInsts (internal/check's forElementType explains why it cannot share).
func (w *walker) walkBody(inst *Instance) {
	ast.Inspect(inst.Decl.Body, func(n ast.Node) bool {
		if ci, ok := w.tys.Insts[n.NodeID()]; ok {
			if target := w.resolve(inst, ci, n.Span()); target != nil {
				inst.Calls[n.NodeID()] = target
			}
		}
		if ci, ok := w.tys.IterInsts[n.NodeID()]; ok {
			if target := w.resolve(inst, ci, n.Span()); target != nil {
				inst.IterCalls[n.NodeID()] = target
			}
		}
		return true
	})
}

// resolve finds the instance a call site reaches, substituting the caller's own
// instantiation into the types the checker recorded.
func (w *walker) resolve(from *Instance, ci *check.Inst, span diag.Span) *Instance {
	decl, params := ci.Decl, ci.Params
	args := make([]types.Type, 0, len(ci.Args))
	for _, a := range ci.Args {
		args = append(args, substitute(a, from.Subst))
	}

	// A trait method called on a type parameter was resolved against a symbolic
	// receiver, so what the checker recorded is the trait's own declaration. Now that
	// the parameter has a type, the impl that actually runs is known.
	if _, symbolic := types.Prune(ci.Recv).(*types.Param); symbolic {
		recv := substitute(ci.Recv, from.Subst)
		found, ok := w.tys.Lookup.Method(recv, ci.Method)
		if !ok {
			return nil
		}
		decl, params, args = found.Decl, found.Params, found.Args
	}

	if decl == nil || decl.Body == nil {
		// No body to specialize: the impl is one the compiler provides, and the code
		// generator lowers the call to a builtin.
		return nil
	}
	return w.instance(decl, params, args, from, span)
}

func (w *walker) reportDepth(decl *ast.FnDecl, args []types.Type, from *Instance, span diag.Span) {
	chain := []string{instanceName(decl, args)}
	for cur := from; cur != nil; cur = cur.parent {
		chain = append(chain, cur.Name)
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	if len(chain) > maxDepth {
		// Show the ends of the chain rather than all of it: the middle is the same
		// shape repeated, and the point is which type keeps growing.
		chain = append(append([]string{}, chain[:4]...), append([]string{"..."}, chain[len(chain)-4:]...)...)
	}
	w.bag.Errorf("E0055", span,
		"instantiating `%s` exceeds the monomorphization depth limit of %d",
		instanceName(decl, args), maxDepth).
		Label("this call needs a copy at a type that keeps growing").
		Note("the instantiation chain is %s", strings.Join(chain, " -> ")).
		Help("polymorphic recursion is rejected: a generic function must not call itself at a larger type")
}

// instanceName is what the instance is called in a bytecode dump.
func instanceName(decl *ast.FnDecl, args []types.Type) string {
	if len(args) == 0 {
		return decl.Name.Name
	}
	parts := make([]string, 0, len(args))
	for _, a := range args {
		parts = append(parts, types.Prune(a).String())
	}
	return decl.Name.Name + "[" + strings.Join(parts, ", ") + "]"
}

// instanceKey identifies an instance. It is built from pointer identity for nominal
// types rather than from their names, because two types in different modules may share
// a name and must not share a copy.
func instanceKey(decl *ast.FnDecl, args []types.Type) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%p", decl)
	for _, a := range args {
		sb.WriteByte('|')
		sb.WriteString(typeKey(a))
	}
	return sb.String()
}

func typeKey(t types.Type) string {
	switch v := types.Prune(t).(type) {
	case *types.Prim:
		return v.Kind.String()
	case *types.Named:
		var sb strings.Builder
		fmt.Fprintf(&sb, "N%p", v.Def)
		for _, a := range v.Args {
			sb.WriteByte('<')
			sb.WriteString(typeKey(a))
		}
		return sb.String()
	case *types.TupleT:
		var sb strings.Builder
		sb.WriteString("T(")
		for _, e := range v.Elems {
			sb.WriteString(typeKey(e))
			sb.WriteByte(',')
		}
		sb.WriteByte(')')
		return sb.String()
	case *types.FnT:
		var sb strings.Builder
		sb.WriteString("F(")
		for _, p := range v.Params {
			sb.WriteString(typeKey(p))
			sb.WriteByte(',')
		}
		sb.WriteString(")->")
		sb.WriteString(typeKey(v.Ret))
		return sb.String()
	case *types.Param:
		// A parameter that survives here means the caller was itself generic and this
		// walk is over a body that will be specialized again; keying on identity keeps
		// those copies apart.
		return fmt.Sprintf("P%p", v)
	default:
		return fmt.Sprintf("%s", types.Prune(t))
	}
}

// substitute applies an instantiation to a recorded type, pruning as it goes so that an
// inference variable solved during checking is followed to what it was solved to.
func substitute(t types.Type, subst map[*types.Param]types.Type) types.Type {
	if t == nil {
		return nil
	}
	if len(subst) == 0 {
		return types.Prune(t)
	}
	return types.Substitute(t, subst)
}
