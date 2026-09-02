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
	}
	return nil
}
