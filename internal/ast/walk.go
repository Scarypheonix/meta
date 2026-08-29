package ast

// Inspect walks the tree rooted at n in depth-first order, calling f for every node.
// When f returns false the node's children are skipped.
//
// It exists so that structural invariants can be checked over a whole tree — "every
// type path has a resolution", for one — rather than by remembering to visit each place
// a type can appear. Forgetting one of those places was a real bug: type annotations
// inside function bodies went unresolved, and every `let x: T` silently became the error
// type.
func Inspect(n Node, f func(Node) bool) {
	if n == nil || isNilNode(n) || !f(n) {
		return
	}
	for _, child := range children(n) {
		Inspect(child, f)
	}
}

// children returns a node's direct children, in source order.
func children(n Node) []Node {
	var out []Node
	add := func(ns ...Node) {
		for _, c := range ns {
			if c != nil && !isNilNode(c) {
				out = append(out, c)
			}
		}
	}

	switch v := n.(type) {
	case *File:
		for _, u := range v.Uses {
			add(u)
		}
		for _, it := range v.Items {
			add(it)
		}

	case *Use:
		add(v.Path)

	case *FnDecl:
		for _, g := range v.Generics {
			add(g)
		}
		for _, p := range v.Params {
			add(p)
		}
		add(v.Ret)
		for _, w := range v.Where {
			add(w)
		}
		if v.Body != nil {
			add(v.Body)
		}

	case *Param:
		add(v.Pat, v.Type)

	case *GenericParam:
		for _, b := range v.Bounds {
			add(b)
		}

	case *TraitRef:
		add(v.Path)
		for _, a := range v.Args {
			add(a)
		}

	case *WherePred:
		add(v.Type)
		for _, b := range v.Bounds {
			add(b)
		}

	case *StructDecl:
		for _, g := range v.Generics {
			add(g)
		}
		for _, w := range v.Where {
			add(w)
		}
		for _, f := range v.Fields {
			add(f)
		}

	case *Field:
		add(v.Type)

	case *EnumDecl:
		for _, g := range v.Generics {
			add(g)
		}
		for _, w := range v.Where {
			add(w)
		}
		for _, va := range v.Variants {
			add(va)
		}

	case *Variant:
		for _, t := range v.Types {
			add(t)
		}
		for _, f := range v.Fields {
			add(f)
		}

	case *TraitDecl:
		for _, g := range v.Generics {
			add(g)
		}
		for _, st := range v.Supertraits {
			add(st)
		}
		for _, w := range v.Where {
			add(w)
		}
		for _, at := range v.AssocTypes {
			add(at)
		}
		for _, m := range v.Methods {
			add(m)
		}

	case *AssocTypeDecl:
		for _, b := range v.Bounds {
			add(b)
		}

	case *ImplDecl:
		for _, g := range v.Generics {
			add(g)
		}
		if v.Trait != nil {
			add(v.Trait)
		}
		add(v.Type)
		for _, w := range v.Where {
			add(w)
		}
		for _, at := range v.AssocTypes {
			add(at)
		}
		for _, m := range v.Methods {
			add(m)
		}

	case *AssocTypeDef:
		add(v.Type)

	case *TypeAliasDecl:
		for _, g := range v.Generics {
			add(g)
		}
		add(v.Type)

	case *ConstDecl:
		add(v.Type, v.Value)

	case *PathType:
		add(v.Path)
		for _, a := range v.Args {
			add(a)
		}

	case *TupleType:
		for _, e := range v.Elems {
			add(e)
		}

	case *FnType:
		for _, p := range v.Params {
			add(p)
		}
		add(v.Ret)

	case *LetStmt:
		add(v.Pat, v.Type, v.Value)

	case *ExprStmt:
		add(v.X)

	case *ItemStmt:
		add(v.Item)

	case *Block:
		for _, s := range v.Stmts {
			add(s)
		}
		add(v.Tail)

	case *PathExpr:
		add(v.Path)
		for _, a := range v.Args {
			add(a)
		}

	case *StructLit:
		add(v.Path)
		for _, a := range v.Args {
			add(a)
		}
		for _, f := range v.Fields {
			add(f)
		}

	case *FieldInit:
		add(v.Value)

	case *TupleExpr:
		for _, e := range v.Elems {
			add(e)
		}

	case *Lambda:
		for _, p := range v.Params {
			add(p)
		}
		add(v.Ret, v.Body)

	case *LambdaParam:
		add(v.Pat, v.Type)

	case *If:
		add(v.Cond, v.Then, v.Else)

	case *Match:
		add(v.Scrutinee)
		for _, a := range v.Arms {
			add(a)
		}

	case *MatchArm:
		add(v.Pat, v.Guard, v.Body)

	case *While:
		add(v.Cond, v.Body)

	case *For:
		add(v.Pat, v.Iter, v.Body)

	case *Loop:
		add(v.Body)

	case *Break:
		add(v.Value)

	case *Return:
		add(v.Value)

	case *Unary:
		add(v.X)

	case *Binary:
		add(v.L, v.R)

	case *Assign:
		add(v.Place, v.Value)

	case *Cast:
		add(v.X, v.Type)

	case *Call:
		add(v.Fn)
		for _, a := range v.Args {
			add(a)
		}

	case *MethodCall:
		add(v.Recv)
		for _, a := range v.Args {
			add(a)
		}

	case *FieldAccess:
		add(v.Recv)

	case *Try:
		add(v.X)

	case *LitPat:
		add(v.Lit)

	case *BindPat:
		add(v.Sub)

	case *PathPat:
		add(v.Path)
		for _, e := range v.Elems {
			add(e)
		}
		for _, f := range v.Fields {
			add(f)
		}

	case *FieldPat:
		add(v.Pat)

	case *TuplePat:
		for _, e := range v.Elems {
			add(e)
		}

	case *OrPat:
		for _, a := range v.Alts {
			add(a)
		}
	}
	return out
}
