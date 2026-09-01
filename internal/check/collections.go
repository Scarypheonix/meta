package check

import (
	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/types"
)

// The signatures of the compiler-provided array operations (spec/13-collections.md).
//
// ADR-0028 put the collections in the prelude rather than in the grammar, so `List` and
// `Map` are ordinary generic types with ordinary methods. What cannot be written in Origin
// is the storage underneath them: a run of elements the collector understands, whose length
// grows. That is `Array[T]`, and these are its operations — the same arrangement Phase 6
// used, where the surface is Origin and the body is the compiler's.
//
// Every one of them returns a primitive or an array. None returns an `Option`, because a
// runtime that had to build one would have to know what the prelude's `Option` is, and the
// native backend has no way to construct a prelude type (spec/12-concurrency.md made the
// same choice for `recv`). `get`, which does return an `Option`, is `List`'s and is written
// in Origin.

// arrayBuiltinType gives the operations in std::array their signatures. It returns nil for
// a name it does not own, so builtinType can carry on with the rest.
func (c *Checker) arrayBuiltinType(name string, targs []ast.Type, span diag.Span) types.Type {
	unit := types.Unit()
	i64 := types.P(types.I64)

	// Each operation is generic in the element type. `array::new[i64](8)` names it
	// directly, which an array that is created and never read would otherwise have no way
	// to fix; everywhere else it is inferred from the array argument.
	elem := func() types.Type {
		if len(targs) == 1 {
			return c.toType(targs[0])
		}
		if len(targs) > 1 {
			c.bag.Errorf("E0107", span,
				"`%s` takes 1 type argument but %d were supplied", name, len(targs)).
				Label("wrong number of type arguments")
		}
		return c.freshFor(span)
	}
	array := func(t types.Type) types.Type { return &types.Named{Def: types.ArrayDef, Args: []types.Type{t}} }

	switch name {
	case "hash::of":
		// hash::of[T](v: T) -> i64
		return &types.FnT{Params: []types.Type{elem()}, Ret: i64}

	case "map::new":
		// map::new[K, V]() -> Map[K, V]
		var k, v types.Type
		switch len(targs) {
		case 0:
			k, v = c.freshFor(span), c.freshFor(span)
		case 2:
			k, v = c.toType(targs[0]), c.toType(targs[1])
		default:
			c.bag.Errorf("E0107", span,
				"`%s` takes 2 type arguments but %d were supplied", name, len(targs)).
				Label("wrong number of type arguments")
			k, v = types.Error, types.Error
		}
		return &types.FnT{Ret: c.named("Map", k, v)}

	case "list::new":
		// list::new[T]() -> List[T]
		return &types.FnT{Ret: c.named("List", elem())}

	case "array::new":
		t := elem()
		return &types.FnT{Params: []types.Type{i64}, Ret: array(t)}
	case "array::len", "array::cap":
		t := elem()
		return &types.FnT{Params: []types.Type{array(t)}, Ret: i64}
	case "array::at":
		t := elem()
		return &types.FnT{Params: []types.Type{array(t), i64}, Ret: t}
	case "array::set":
		t := elem()
		return &types.FnT{Params: []types.Type{array(t), i64, t}, Ret: unit}
	case "array::push":
		t := elem()
		return &types.FnT{Params: []types.Type{array(t), t}, Ret: types.P(types.Bool)}
	case "array::truncate":
		t := elem()
		return &types.FnT{Params: []types.Type{array(t), i64}, Ret: unit}
	}
	return nil
}
