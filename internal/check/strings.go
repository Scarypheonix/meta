package check

import (
	"github.com/scarypheonix/meta/internal/types"
)

// The signatures of the compiler-provided string operations (spec/14-strings.md).
//
// Six operations, and every one of them is here for the same reason `Array[T]`'s are
// (ADR-0028): its body has to read or allocate raw bytes, which Origin has no way to say.
// The rest of what a `String` can do is the `Str` trait's default method bodies, written in
// Origin in the prelude over exactly these.
//
// None of them is generic and none returns an `Option`, so there is nothing here to infer
// and nothing for a runtime to construct -- the same arrangement §12 and §13 arrived at,
// for the same reason: the native backend has no way to build a prelude type.

// strBuiltinType gives the operations in std::str their signatures, or nil for a name it
// does not own.
func (c *Checker) strBuiltinType(name string) types.Type {
	str := types.P(types.String)
	i64 := types.P(types.I64)

	switch name {
	case "str::len":
		return &types.FnT{Params: []types.Type{str}, Ret: i64}
	case "str::byte_at", "str::char_width":
		return &types.FnT{Params: []types.Type{str, i64}, Ret: i64}
	case "str::char_at":
		return &types.FnT{Params: []types.Type{str, i64}, Ret: types.P(types.Char)}
	case "str::slice":
		return &types.FnT{Params: []types.Type{str, i64, i64}, Ret: str}
	case "str::concat":
		return &types.FnT{Params: []types.Type{str, str}, Ret: str}

	// The file operations (spec/15-files.md). Each returns a status or a primitive, for
	// the reason every other compiler-provided operation does: a runtime that had to
	// build a `Result` would have to know what the prelude's `Result` is, and the native
	// backend has no way to construct a prelude type.
	case "fs::read_file":
		return &types.FnT{Params: []types.Type{str}, Ret: i64}
	case "fs::taken_text":
		return &types.FnT{Ret: str}
	case "fs::write_file":
		return &types.FnT{Params: []types.Type{str, str}, Ret: i64}
	case "fs::file_exists":
		return &types.FnT{Params: []types.Type{str}, Ret: types.P(types.Bool)}

	// The float's bits (spec/16-floats.md). A `f64` and a `u64` are the same sixty-four
	// bits; these two say so, and nothing else in the language does. They exist so that
	// the decimal rendering of a float can be Origin source in the prelude rather than a
	// shortest-round-trip algorithm written three times, once per engine.
	// `char::from_u32` is the one compiler-provided operation whose result is a prelude
	// type. It is spelled that way because spec/04-expressions.md and the diagnostic for a
	// rejected `as char` both name it, and because the alternative -- a predicate plus an
	// unchecked conversion -- would put a way to make an invalid `char` in reach. Like
	// `checked_add`, internal/compile builds the `Option` itself, so no runtime has to.
	case "char::from_u32":
		return &types.FnT{Params: []types.Type{types.P(types.U32)}, Ret: c.named("Option", types.P(types.Char))}

	case "float::bits":
		return &types.FnT{Params: []types.Type{types.P(types.F64)}, Ret: types.P(types.U64)}
	case "float::from_bits":
		return &types.FnT{Params: []types.Type{types.P(types.U64)}, Ret: types.P(types.F64)}
	}
	return nil
}
