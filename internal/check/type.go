package check

import (
	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/resolve"
	"github.com/scarypheonix/meta/internal/types"
)

// primKinds maps a primitive's source name to its kind.
var primKinds = map[string]types.PrimKind{
	"i8": types.I8, "i16": types.I16, "i32": types.I32, "i64": types.I64,
	"u8": types.U8, "u16": types.U16, "u32": types.U32, "u64": types.U64,
	"f32": types.F32, "f64": types.F64,
	"bool": types.Bool, "char": types.Char, "String": types.String,
}

// toType converts a syntactic type to a semantic one, using the current environment for
// generic parameters and `Self`. Every name comes from the resolver's side table; this
// function never consults a scope.
func (c *Checker) toType(t ast.Type) types.Type {
	switch v := t.(type) {
	case nil:
		return types.Unit()

	case *ast.ErrorType:
		return types.Error

	case *ast.UnitType:
		return types.Unit()

	case *ast.SelfType:
		if c.env.self == nil {
			c.bag.Errorf("E0411", v.Span(), "`Self` is not available here").
				Label("`Self` names the implementing type").
				Note("it is only in scope inside a trait or an impl")
			return types.Error
		}
		return c.env.self

	case *ast.TupleType:
		elems := make([]types.Type, 0, len(v.Elems))
		for _, e := range v.Elems {
			elems = append(elems, c.toType(e))
		}
		return &types.TupleT{Elems: elems}

	case *ast.FnType:
		params := make([]types.Type, 0, len(v.Params))
		for _, p := range v.Params {
			params = append(params, c.toType(p))
		}
		return &types.FnT{Params: params, Ret: c.toType(v.Ret)}

	case *ast.PathType:
		return c.pathType(v)
	}
	return types.Error
}

func (c *Checker) pathType(v *ast.PathType) types.Type {
	ref, ok := c.res.Ref(v.NodeID())
	if !ok || ref.Kind == resolve.Unresolved {
		return types.Error // the resolver already reported it
	}

	args := make([]types.Type, 0, len(v.Args))
	for _, a := range v.Args {
		args = append(args, c.toType(a))
	}

	switch ref.Kind {
	case resolve.Prim:
		if len(args) > 0 {
			c.bag.Errorf("E0109", v.Span(), "`%s` takes no type arguments", ref.Name).
				Label("primitive types are not generic")
		}
		return types.P(primKinds[ref.Name])

	case resolve.TypeParam:
		p, ok := c.env.params[ref.Name]
		if !ok {
			c.bag.Errorf("E0412", v.Span(), "type parameter `%s` is not in scope here", ref.Name).
				Label("not available in this declaration")
			return types.Error
		}
		if len(args) > 0 {
			c.bag.Errorf("E0109", v.Span(), "type parameter `%s` takes no type arguments", ref.Name).
				Label("a type parameter is not generic")
		}
		return p

	case resolve.SelfTy:
		if c.env.self == nil {
			c.bag.Errorf("E0411", v.Span(), "`Self` is not available here").
				Label("`Self` names the implementing type")
			return types.Error
		}
		return c.env.self

	case resolve.Struct:
		return c.applyDef(v, c.defs[ref.Struct], args)

	case resolve.Enum:
		return c.applyDef(v, c.defs[ref.Enum], args)

	case resolve.TypeAlias:
		// Aliases are transparent: expand and remember the alias for diagnostics.
		if expanded, ok := c.aliases[ref.Alias]; ok {
			return expanded
		}
		return types.Error

	case resolve.Assoc:
		return c.assocProjection(v, ref)
	}

	c.bag.Errorf("E0573", v.Span(), "`%s` is not a type", v.Path).
		Label("expected a type").
		Note("`%s` names %s", v.Path, describeRef(ref))
	return types.Error
}

// applyDef instantiates a nominal type, checking its arity.
func (c *Checker) applyDef(v *ast.PathType, def *types.Def, args []types.Type) types.Type {
	if def == nil {
		return types.Error
	}
	if len(args) != len(def.Params) {
		c.bag.Errorf("E0107", v.Span(),
			"`%s` takes %d type argument(s) but %d were supplied", def.Name, len(def.Params), len(args)).
			Label("wrong number of type arguments")
		// Recover with fresh variables so one arity mistake does not cascade.
		args = make([]types.Type, len(def.Params))
		for i := range args {
			args[i] = types.Error
		}
	}
	return &types.Named{Def: def, Args: args}
}

// assocProjection builds `T::Item`, finding which of T's bounds declares the member.
func (c *Checker) assocProjection(v *ast.PathType, ref resolve.Ref) types.Type {
	var self types.Type
	var bounds []Bound

	if ref.BaseKind == resolve.SelfTy {
		if c.env.self == nil {
			c.bag.Errorf("E0411", v.Span(), "`Self` is not available here").
				Label("`Self` names the implementing type")
			return types.Error
		}
		self = c.env.self
		if c.env.selfTrait != nil {
			// Inside the trait that declares it, `Self::Item` refers to the trait's own
			// associated type.
			if containsString(c.env.selfTrait.AssocTypes, ref.Member) {
				return &types.AssocT{Trait: c.env.selfTrait.Decl, Member: ref.Member, Self: self}
			}
		}
		bounds = c.boundsOn(self)
	} else {
		p, ok := c.env.params[ref.Name]
		if !ok {
			c.bag.Errorf("E0412", v.Span(), "type parameter `%s` is not in scope here", ref.Name).
				Label("not available in this declaration")
			return types.Error
		}
		self = p
		bounds = c.boundsOn(p)
	}

	for _, b := range bounds {
		if containsString(b.Trait.AssocTypes, ref.Member) {
			return &types.AssocT{Trait: b.Trait.Decl, Member: ref.Member, Self: self}
		}
	}
	c.bag.Errorf("E0220", v.Span(), "cannot determine `%s::%s`", ref.Name, ref.Member).
		Label("no bound on `%s` declares `%s`", ref.Name, ref.Member).
		Help("add a bound such as `%s: Iterator`", ref.Name)
	return types.Error
}

// boundsOn returns the declared bounds that apply to a rigid type in the current
// declaration.
func (c *Checker) boundsOn(t types.Type) []Bound {
	var out []Bound
	for _, b := range c.env.bounds {
		if types.Unify(b.Type, t) == nil {
			out = append(out, b)
		}
	}
	return out
}

func describeRef(ref resolve.Ref) string {
	switch ref.Kind {
	case resolve.Fn:
		return "a function"
	case resolve.Const:
		return "a constant"
	case resolve.Trait:
		return "a trait"
	case resolve.Variant:
		return "an enum variant"
	case resolve.LocalVar:
		return "a local binding"
	case resolve.Builtin:
		return "a builtin"
	}
	return "something that is not a type"
}
