package ast

import (
	"fmt"
	"strconv"
	"strings"
)

// Dump renders a node as a parenthesized tree. It exists for tests and for
// `originc check --dump-ast`; it is not a formatter and makes no promise of being
// re-parseable. Snapshot tests compare its output, so its shape is stable: changing it
// means updating goldens deliberately.
//
// It is a *complete* description of the tree, and that is load-bearing rather than
// decorative: tests/selfhost holds stage1's parser to this one by requiring the two dumps
// to agree, so anything Dump leaves out is syntax the second parser may discard with
// nothing to notice. Bounds, `where` predicates, supertraits, an impl's generics and a
// trait reference's type arguments were all omitted here until Phase 9, and stage1's
// parser had duly thrown every one of them away.
func Dump(n Node) string {
	var sb strings.Builder
	dump(&sb, n, 0)
	return sb.String()
}

func indent(sb *strings.Builder, depth int) {
	sb.WriteString(strings.Repeat("  ", depth))
}

func dump(sb *strings.Builder, n Node, depth int) {
	if n == nil || isNilNode(n) {
		indent(sb, depth)
		sb.WriteString("<nil>\n")
		return
	}
	switch v := n.(type) {
	case *File:
		line(sb, depth, "file")
		for _, u := range v.Uses {
			dump(sb, u, depth+1)
		}
		for _, it := range v.Items {
			dump(sb, it, depth+1)
		}

	case *Use:
		names := ""
		if len(v.Names) > 0 {
			var parts []string
			for _, nm := range v.Names {
				parts = append(parts, nm.Name)
			}
			names = "::{" + strings.Join(parts, ", ") + "}"
		}
		line(sb, depth, "use %s%s", v.Path, names)

	case *FnDecl:
		line(sb, depth, "fn %s%s%s", pubMark(v.Pub), v.Name.Name, generics(v.Generics))
		dumpBounds(sb, v.Generics, depth+1)
		dumpWhere(sb, v.Where, depth+1)
		if v.Self != nil {
			line(sb, depth+1, "self%s", mutMark(v.Self.Mut))
		}
		for _, p := range v.Params {
			line(sb, depth+1, "param%s", mutMark(p.Mut))
			dump(sb, p.Pat, depth+2)
			dump(sb, p.Type, depth+2)
		}
		if v.Ret != nil {
			line(sb, depth+1, "returns")
			dump(sb, v.Ret, depth+2)
		}
		if v.Body != nil {
			dump(sb, v.Body, depth+1)
		}

	case *StructDecl:
		line(sb, depth, "struct %s%s%s", pubMark(v.Pub), v.Name.Name, generics(v.Generics))
		dumpBounds(sb, v.Generics, depth+1)
		dumpWhere(sb, v.Where, depth+1)
		for _, f := range v.Fields {
			line(sb, depth+1, "field %s%s%s", pubMark(f.Pub), mutPrefix(f.Mut), f.Name.Name)
			dump(sb, f.Type, depth+2)
		}

	case *EnumDecl:
		line(sb, depth, "enum %s%s%s", pubMark(v.Pub), v.Name.Name, generics(v.Generics))
		dumpBounds(sb, v.Generics, depth+1)
		dumpWhere(sb, v.Where, depth+1)
		for _, va := range v.Variants {
			switch va.Kind {
			case UnitVariant:
				line(sb, depth+1, "variant %s", va.Name.Name)
			case TupleVariant:
				line(sb, depth+1, "variant %s(..)", va.Name.Name)
				for _, t := range va.Types {
					dump(sb, t, depth+2)
				}
			case StructVariant:
				line(sb, depth+1, "variant %s{..}", va.Name.Name)
				for _, f := range va.Fields {
					line(sb, depth+2, "field %s%s", mutPrefix(f.Mut), f.Name.Name)
					dump(sb, f.Type, depth+3)
				}
			}
		}

	case *TraitDecl:
		line(sb, depth, "trait %s%s%s", pubMark(v.Pub), v.Name.Name, generics(v.Generics))
		dumpBounds(sb, v.Generics, depth+1)
		for _, st := range v.Supertraits {
			line(sb, depth+1, "super")
			dumpTraitRef(sb, st, depth+2)
		}
		dumpWhere(sb, v.Where, depth+1)
		for _, at := range v.AssocTypes {
			line(sb, depth+1, "assoc-type %s", at.Name.Name)
			for _, b := range at.Bounds {
				dumpTraitRef(sb, b, depth+2)
			}
		}
		for _, m := range v.Methods {
			dump(sb, m, depth+1)
		}

	case *ImplDecl:
		if v.Trait != nil {
			line(sb, depth, "impl%s %s for", generics(v.Generics), v.Trait.Path)
			for _, a := range v.Trait.Args {
				line(sb, depth+1, "trait-arg")
				dump(sb, a, depth+2)
			}
		} else {
			line(sb, depth, "impl%s", generics(v.Generics))
		}
		dumpBounds(sb, v.Generics, depth+1)
		dumpWhere(sb, v.Where, depth+1)
		dump(sb, v.Type, depth+1)
		for _, at := range v.AssocTypes {
			line(sb, depth+1, "assoc-type %s =", at.Name.Name)
			dump(sb, at.Type, depth+2)
		}
		for _, m := range v.Methods {
			dump(sb, m, depth+1)
		}

	case *TypeAliasDecl:
		line(sb, depth, "type %s%s%s =", pubMark(v.Pub), v.Name.Name, generics(v.Generics))
		dumpBounds(sb, v.Generics, depth+1)
		dump(sb, v.Type, depth+1)

	case *ConstDecl:
		line(sb, depth, "const %s%s", pubMark(v.Pub), v.Name.Name)
		dump(sb, v.Type, depth+1)
		dump(sb, v.Value, depth+1)

	case *ErrorItem:
		line(sb, depth, "<error item>")

	// Types
	case *PathType:
		if len(v.Args) == 0 {
			line(sb, depth, "type %s", v.Path)
		} else {
			line(sb, depth, "type %s[..]", v.Path)
			for _, a := range v.Args {
				dump(sb, a, depth+1)
			}
		}
	case *TupleType:
		line(sb, depth, "tuple-type")
		for _, e := range v.Elems {
			dump(sb, e, depth+1)
		}
	case *UnitType:
		line(sb, depth, "unit-type")
	case *FnType:
		line(sb, depth, "fn-type")
		for _, pt := range v.Params {
			dump(sb, pt, depth+1)
		}
		line(sb, depth+1, "->")
		dump(sb, v.Ret, depth+2)
	case *SelfType:
		line(sb, depth, "Self")
	case *ErrorType:
		line(sb, depth, "<error type>")

	// Statements
	case *LetStmt:
		line(sb, depth, "let%s", mutMark(v.Mut))
		dump(sb, v.Pat, depth+1)
		if v.Type != nil {
			dump(sb, v.Type, depth+1)
		}
		dump(sb, v.Value, depth+1)
	case *ExprStmt:
		line(sb, depth, "expr-stmt%s", semiMark(v.Semi))
		dump(sb, v.X, depth+1)
	case *ItemStmt:
		line(sb, depth, "item-stmt")
		dump(sb, v.Item, depth+1)

	// Expressions
	case *IntLit:
		line(sb, depth, "int %d%s%s", v.Value, overflowMark(v.Overflow), suffixMark(v.Suffix))
	case *FloatLit:
		line(sb, depth, "float %s%s", strconv.FormatFloat(v.Value, 'g', -1, 64), suffixMark(v.Suffix))
	case *StrLit:
		line(sb, depth, "str %q", v.Value)
	case *CharLit:
		line(sb, depth, "char %q", v.Value)
	case *BoolLit:
		line(sb, depth, "bool %v", v.Value)
	case *PathExpr:
		if len(v.Args) == 0 {
			line(sb, depth, "path %s", v.Path)
		} else {
			line(sb, depth, "path %s[..]", v.Path)
			for _, a := range v.Args {
				dump(sb, a, depth+1)
			}
		}
	case *SelfExpr:
		line(sb, depth, "self")
	case *StructLit:
		if len(v.Args) == 0 {
			line(sb, depth, "struct-lit %s", v.Path)
		} else {
			line(sb, depth, "struct-lit %s[..]", v.Path)
			for _, a := range v.Args {
				dump(sb, a, depth+1)
			}
		}
		for _, f := range v.Fields {
			line(sb, depth+1, "field %s", f.Name.Name)
			dump(sb, f.Value, depth+2)
		}
	case *TupleExpr:
		if len(v.Elems) == 0 {
			line(sb, depth, "unit")
		} else {
			line(sb, depth, "tuple")
			for _, e := range v.Elems {
				dump(sb, e, depth+1)
			}
		}
	case *Lambda:
		line(sb, depth, "lambda")
		for _, lp := range v.Params {
			line(sb, depth+1, "param")
			dump(sb, lp.Pat, depth+2)
			if lp.Type != nil {
				dump(sb, lp.Type, depth+2)
			}
		}
		if v.Ret != nil {
			line(sb, depth+1, "returns")
			dump(sb, v.Ret, depth+2)
		}
		dump(sb, v.Body, depth+1)
	case *Block:
		line(sb, depth, "block")
		for _, s := range v.Stmts {
			dump(sb, s, depth+1)
		}
		if v.Tail != nil {
			line(sb, depth+1, "tail")
			dump(sb, v.Tail, depth+2)
		}
	case *If:
		line(sb, depth, "if")
		dump(sb, v.Cond, depth+1)
		dump(sb, v.Then, depth+1)
		if v.Else != nil {
			line(sb, depth+1, "else")
			dump(sb, v.Else, depth+2)
		}
	case *Match:
		line(sb, depth, "match")
		dump(sb, v.Scrutinee, depth+1)
		for _, arm := range v.Arms {
			line(sb, depth+1, "arm")
			dump(sb, arm.Pat, depth+2)
			if arm.Guard != nil {
				line(sb, depth+2, "guard")
				dump(sb, arm.Guard, depth+3)
			}
			dump(sb, arm.Body, depth+2)
		}
	case *While:
		line(sb, depth, "while")
		dump(sb, v.Cond, depth+1)
		dump(sb, v.Body, depth+1)
	case *For:
		line(sb, depth, "for")
		dump(sb, v.Pat, depth+1)
		dump(sb, v.Iter, depth+1)
		dump(sb, v.Body, depth+1)
	case *Loop:
		line(sb, depth, "loop")
		dump(sb, v.Body, depth+1)
	case *Break:
		line(sb, depth, "break")
		if v.Value != nil {
			dump(sb, v.Value, depth+1)
		}
	case *Continue:
		line(sb, depth, "continue")
	case *Return:
		line(sb, depth, "return")
		if v.Value != nil {
			dump(sb, v.Value, depth+1)
		}
	case *Unary:
		line(sb, depth, "unary %s", v.Op)
		dump(sb, v.X, depth+1)
	case *Binary:
		line(sb, depth, "binary %s", v.Op)
		dump(sb, v.L, depth+1)
		dump(sb, v.R, depth+1)
	case *Assign:
		line(sb, depth, "assign %s", v.Op)
		dump(sb, v.Place, depth+1)
		dump(sb, v.Value, depth+1)
	case *Cast:
		line(sb, depth, "cast")
		dump(sb, v.X, depth+1)
		dump(sb, v.Type, depth+1)
	case *Call:
		line(sb, depth, "call")
		dump(sb, v.Fn, depth+1)
		for _, a := range v.Args {
			dump(sb, a, depth+1)
		}
	case *MethodCall:
		line(sb, depth, "method %s", v.Name.Name)
		dump(sb, v.Recv, depth+1)
		for _, a := range v.Args {
			dump(sb, a, depth+1)
		}
	case *FieldAccess:
		line(sb, depth, "field %s", v.Name.Name)
		dump(sb, v.Recv, depth+1)
	case *Try:
		line(sb, depth, "try")
		dump(sb, v.X, depth+1)
	case *ErrorExpr:
		line(sb, depth, "<error expr>")

	// Patterns
	case *WildcardPat:
		line(sb, depth, "pat _")
	case *LitPat:
		neg := ""
		if v.Neg {
			neg = "-"
		}
		line(sb, depth, "pat lit %s", neg)
		dump(sb, v.Lit, depth+1)
	case *BindPat:
		line(sb, depth, "pat bind%s %s", mutMark(v.Mut), v.Name.Name)
		if v.Sub != nil {
			dump(sb, v.Sub, depth+1)
		}
	case *PathPat:
		switch v.Kind {
		case UnitVariant:
			line(sb, depth, "pat path %s", v.Path)
		case TupleVariant:
			line(sb, depth, "pat path %s(..)", v.Path)
			for _, e := range v.Elems {
				dump(sb, e, depth+1)
			}
		case StructVariant:
			rest := ""
			if v.Rest {
				rest = " .."
			}
			line(sb, depth, "pat path %s{..}%s", v.Path, rest)
			for _, f := range v.Fields {
				line(sb, depth+1, "field %s", f.Name.Name)
				dump(sb, f.Pat, depth+2)
			}
		}
	case *TuplePat:
		line(sb, depth, "pat tuple")
		for _, e := range v.Elems {
			dump(sb, e, depth+1)
		}
	case *OrPat:
		line(sb, depth, "pat or")
		for _, a := range v.Alts {
			dump(sb, a, depth+1)
		}
	case *ErrorPat:
		line(sb, depth, "<error pat>")

	default:
		line(sb, depth, "<unknown node %T>", v)
	}
}

func line(sb *strings.Builder, depth int, format string, args ...any) {
	indent(sb, depth)
	fmt.Fprintf(sb, format, args...)
	sb.WriteByte('\n')
}

func pubMark(pub bool) string {
	if pub {
		return "pub "
	}
	return ""
}

func mutMark(mut bool) string {
	if mut {
		return " mut"
	}
	return ""
}

func mutPrefix(mut bool) string {
	if mut {
		return "mut "
	}
	return ""
}

func semiMark(semi bool) string {
	if semi {
		return " ;"
	}
	return ""
}

func suffixMark(s string) string {
	if s == "" {
		return ""
	}
	return " " + s
}

// dumpBounds renders the bounds a generic parameter list carries. The names themselves
// are already inline in `generics`; this is the part that has type arguments and so
// cannot be.
func dumpBounds(sb *strings.Builder, gs []*GenericParam, depth int) {
	for _, g := range gs {
		if len(g.Bounds) == 0 {
			continue
		}
		line(sb, depth, "bound %s", g.Name.Name)
		for _, b := range g.Bounds {
			dumpTraitRef(sb, b, depth+1)
		}
	}
}

// dumpWhere renders the `where` predicates, each as a type and the bounds on it.
func dumpWhere(sb *strings.Builder, preds []*WherePred, depth int) {
	for _, w := range preds {
		line(sb, depth, "where")
		dump(sb, w.Type, depth+1)
		for _, b := range w.Bounds {
			dumpTraitRef(sb, b, depth+1)
		}
	}
}

// dumpTraitRef renders one trait reference, with its type arguments as children so that
// a nested application dumps the same way a nested type does.
func dumpTraitRef(sb *strings.Builder, tr *TraitRef, depth int) {
	if tr == nil {
		line(sb, depth, "<nil>")
		return
	}
	if len(tr.Args) == 0 {
		line(sb, depth, "trait %s", tr.Path)
		return
	}
	line(sb, depth, "trait %s[..]", tr.Path)
	for _, a := range tr.Args {
		dump(sb, a, depth+1)
	}
}

// overflowMark marks an integer literal too large for any type to hold. The lexer records
// it rather than rejecting it, so that the checker can say which type it did not fit.
func overflowMark(overflow bool) string {
	if overflow {
		return " overflow"
	}
	return ""
}

func generics(gs []*GenericParam) string {
	if len(gs) == 0 {
		return ""
	}
	var parts []string
	for _, g := range gs {
		parts = append(parts, g.Name.Name)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// isNilNode reports whether n is a typed nil, which a nil field of interface type
// produces and which would otherwise panic on a method call.
func isNilNode(n Node) bool {
	switch v := n.(type) {
	case *Block:
		return v == nil
	case *Path:
		return v == nil
	}
	return false
}
