package resolve

import (
	"fmt"

	"github.com/scarypheonix/meta/internal/diag"
)

// The resolution trace: one line per event, in the order the resolver produces them.
//
// It exists so that a second resolver can be held to this one. internal/resolve keys its
// side tables by ast.NodeID and stage1's syntax tree deliberately has no node ids, so the
// tables themselves cannot be compared. What can be is the *sequence*: the two resolvers
// walk the same tree in the same order, so a line's position in the trace identifies the
// node, and what the line says identifies what the node resolved to.
//
// A declaration is named by where it was written -- `<file>:<offset>` of its own name --
// rather than by an index into anything. That is what makes shadowing observable: two
// bindings called `x` are the same only if they were declared in the same place.

// note records one resolution.
func (r *resolver) note(ref Ref) {
	if r.trace == nil {
		return
	}
	*r.trace = append(*r.trace, "ref "+describeRef(ref))
}

// noteBinding records a pattern introducing a binding.
func (r *resolver) noteBinding(l *Local) {
	if r.trace == nil {
		return
	}
	*r.trace = append(*r.trace, fmt.Sprintf("bind %s %v %s", l.Name, l.Mut, spanText(l.Decl)))
}

// noteCaptures records what a lambda captured, in the order the captures were found.
func (r *resolver) noteCaptures(order []*Local) {
	if r.trace == nil {
		return
	}
	line := fmt.Sprintf("lambda %d", len(order))
	for _, l := range order {
		line += fmt.Sprintf(" %s@%s", l.Name, spanText(l.Decl))
	}
	*r.trace = append(*r.trace, line)
}

// describeRef renders what one identifier occurrence resolved to.
func describeRef(ref Ref) string {
	switch ref.Kind {
	case LocalVar:
		return fmt.Sprintf("local %s %s", ref.Local.Name, spanText(ref.Local.Decl))
	case Fn:
		return fmt.Sprintf("fn %s %s", ref.Name, spanText(ref.Fn.Name.Loc))
	case Struct:
		return fmt.Sprintf("struct %s %s", ref.Name, spanText(ref.Struct.Name.Loc))
	case Enum:
		return fmt.Sprintf("enum %s %s", ref.Name, spanText(ref.Enum.Name.Loc))
	case Variant:
		return fmt.Sprintf("variant %s %s", ref.Name, spanText(ref.Variant.Name.Loc))
	case Const:
		return fmt.Sprintf("const %s %s", ref.Name, spanText(ref.Const.Name.Loc))
	case Trait:
		return fmt.Sprintf("trait %s %s", ref.Name, spanText(ref.Trait.Name.Loc))
	case TypeAlias:
		return fmt.Sprintf("alias %s %s", ref.Name, spanText(ref.Alias.Name.Loc))
	case Builtin:
		return "builtin " + ref.Builtin
	case Prim:
		return "prim " + ref.Name
	case PrimConst:
		return fmt.Sprintf("primconst %s::%s", ref.Name, ref.Member)
	case SelfTy:
		return "selfty"
	case TypeParam:
		return "typeparam " + ref.Name
	case ModuleRef:
		return "module " + ref.Mod.Path()
	case Assoc:
		return fmt.Sprintf("assoc %s %s::%s", kindWord(ref.BaseKind), ref.Name, ref.Member)
	}
	return "unresolved"
}

// kindWord names a kind for the trace. Only the two an associated-type projection can be
// based on are needed, and anything else is a bug rather than a case to render.
func kindWord(k Kind) string {
	switch k {
	case SelfTy:
		return "selfty"
	case TypeParam:
		return "typeparam"
	}
	return "?"
}

// spanText names a declaration by the file it is in and the byte offset its name starts
// at, which is the identity the trace compares.
func spanText(s diag.Span) string {
	if s.File == nil {
		return "?:0"
	}
	return fmt.Sprintf("%s:%d", s.File.Name, s.Start)
}
