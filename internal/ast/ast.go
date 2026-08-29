// Package ast defines Origin's abstract syntax tree.
//
// Per ADR-0015 the AST is a sealed interface with one struct per node kind. Nodes are
// immutable after parsing and carry no semantic information: resolved names, inferred
// types and instantiations live in side tables keyed by NodeID, owned by the pass that
// computes them. A pass that wants to write into a node is a pass that has broken the
// layering.
package ast

import (
	"fmt"

	"github.com/scarypheonix/meta/internal/diag"
)

// NodeID identifies a node uniquely within one parsed file. Side tables are keyed by it.
type NodeID uint32

// NoID is the zero NodeID and never identifies a real node.
const NoID NodeID = 0

// IDGen allocates node ids.
//
// One generator serves a whole compilation, not one file: the side tables that resolve
// and check produce are keyed by NodeID, so two files whose ids both start at 1 would
// silently share entries. One generator per file was a real bug, found when the checker
// first walked the prelude and the user's file together.
type IDGen struct{ next NodeID }

// NewIDGen returns a generator whose first id is 1.
func NewIDGen() *IDGen { return &IDGen{next: 1} }

// Next returns the next unused node id.
func (g *IDGen) Next() NodeID {
	id := g.next
	g.next++
	return id
}

// Node is implemented by every AST node.
//
// The seal is the unexported isNode method on Base. Embedding Base outside this package
// would defeat it; nothing does, and a linter could enforce it if that ever changes.
type Node interface {
	NodeID() NodeID
	Span() diag.Span
	isNode()
}

// Base carries the identity every node has. Every node struct embeds it by value and is
// used through a pointer.
type Base struct {
	ID  NodeID
	Loc diag.Span
}

func (b *Base) NodeID() NodeID  { return b.ID }
func (b *Base) Span() diag.Span { return b.Loc }
func (b *Base) isNode()         {}

// ---------------------------------------------------------------------------
// Names and paths
// ---------------------------------------------------------------------------

// Ident is a name occurrence. It is not a Node: it has no independent identity and
// nothing attaches semantic information to it directly.
type Ident struct {
	Name string
	Loc  diag.Span
}

func (i Ident) String() string { return i.Name }

// Path is a `::`-separated sequence of identifiers, e.g. `std::io::println`.
type Path struct {
	Base
	Segments []Ident
}

// Last returns the final segment, which is the name being referred to.
func (p *Path) Last() Ident {
	if len(p.Segments) == 0 {
		return Ident{}
	}
	return p.Segments[len(p.Segments)-1]
}

func (p *Path) String() string {
	s := ""
	for i, seg := range p.Segments {
		if i > 0 {
			s += "::"
		}
		s += seg.Name
	}
	return s
}

// ---------------------------------------------------------------------------
// File and items
// ---------------------------------------------------------------------------

// File is one parsed .origin source file: its imports, then its items.
type File struct {
	Base
	Uses  []*Use
	Items []Item
}

// Item is a top-level declaration.
type Item interface {
	Node
	isItem()
}

// Use imports names from one path. Origin 0.1 has no globs and no renaming.
type Use struct {
	Base
	Path *Path
	// Names is the braced list, empty when the import is a single path.
	Names []Ident
}

func (*Use) isItem() {}

// GenericParam is one declared type parameter with its bounds.
type GenericParam struct {
	Base
	Name   Ident
	Bounds []*TraitRef
}

// TraitRef names a trait, optionally applied to type arguments.
type TraitRef struct {
	Base
	Path *Path
	Args []Type
}

// WherePred is one `where` clause predicate: a type and the bounds it must satisfy.
type WherePred struct {
	Base
	Type   Type
	Bounds []*TraitRef
}

// Param is one function parameter.
type Param struct {
	Base
	Mut  bool
	Pat  Pattern
	Type Type
}

// SelfParam is the receiver of a method.
type SelfParam struct {
	Base
	Mut bool
}

// FnDecl is a function or method declaration. Body is nil for a trait's required
// methods, which declare a signature without providing one.
type FnDecl struct {
	Base
	Pub      bool
	Name     Ident
	Generics []*GenericParam
	Self     *SelfParam
	Params   []*Param
	Ret      Type // nil means the unit type
	Where    []*WherePred
	Body     *Block
}

func (*FnDecl) isItem() {}

// Field is one struct or struct-variant field declaration.
type Field struct {
	Base
	Pub  bool
	Mut  bool
	Name Ident
	Type Type
}

// StructDecl declares a nominal record type.
type StructDecl struct {
	Base
	Pub      bool
	Name     Ident
	Generics []*GenericParam
	Where    []*WherePred
	Fields   []*Field
}

func (*StructDecl) isItem() {}

// VariantKind distinguishes the three shapes an enum variant can take.
type VariantKind int

const (
	// UnitVariant carries no payload: `None`.
	UnitVariant VariantKind = iota
	// TupleVariant carries positional types: `Some(T)`.
	TupleVariant
	// StructVariant carries named fields: `Rect { w: f64, h: f64 }`.
	StructVariant
)

// Variant is one enum variant.
type Variant struct {
	Base
	Kind   VariantKind
	Name   Ident
	Types  []Type   // TupleVariant
	Fields []*Field // StructVariant
}

// EnumDecl declares an algebraic data type.
type EnumDecl struct {
	Base
	Pub      bool
	Name     Ident
	Generics []*GenericParam
	Where    []*WherePred
	Variants []*Variant
}

func (*EnumDecl) isItem() {}

// AssocTypeDecl is a trait's associated type requirement.
type AssocTypeDecl struct {
	Base
	Name   Ident
	Bounds []*TraitRef
}

// TraitDecl declares a trait: associated types and method signatures.
type TraitDecl struct {
	Base
	Pub         bool
	Name        Ident
	Generics    []*GenericParam
	Supertraits []*TraitRef
	Where       []*WherePred
	AssocTypes  []*AssocTypeDecl
	Methods     []*FnDecl
}

func (*TraitDecl) isItem() {}

// AssocTypeDef is an impl's definition of an associated type.
type AssocTypeDef struct {
	Base
	Name Ident
	Type Type
}

// ImplDecl is an inherent impl (Trait == nil) or a trait impl.
type ImplDecl struct {
	Base
	Generics   []*GenericParam
	Trait      *TraitRef
	Type       Type
	Where      []*WherePred
	AssocTypes []*AssocTypeDef
	Methods    []*FnDecl
}

func (*ImplDecl) isItem() {}

// TypeAliasDecl declares a transparent alias.
type TypeAliasDecl struct {
	Base
	Pub      bool
	Name     Ident
	Generics []*GenericParam
	Type     Type
}

func (*TypeAliasDecl) isItem() {}

// ConstDecl declares a compile-time constant.
type ConstDecl struct {
	Base
	Pub   bool
	Name  Ident
	Type  Type
	Value Expr
}

func (*ConstDecl) isItem() {}

// ErrorItem stands in for an item the parser could not read. Its presence means a
// diagnostic was already reported; later passes must not report again (spec/09 rule 4).
type ErrorItem struct{ Base }

func (*ErrorItem) isItem() {}

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// Type is a syntactic type expression. It is not a semantic type; see internal/types.
type Type interface {
	Node
	isType()
}

// PathType is a named type, optionally applied to arguments: `Map[K, V]`.
type PathType struct {
	Base
	Path *Path
	Args []Type
}

func (*PathType) isType() {}

// TupleType is `(A, B)`. A one-element tuple is written `(A,)`.
type TupleType struct {
	Base
	Elems []Type
}

func (*TupleType) isType() {}

// UnitType is `()`.
type UnitType struct{ Base }

func (*UnitType) isType() {}

// FnType is `fn(A, B) -> C`.
type FnType struct {
	Base
	Params []Type
	Ret    Type
}

func (*FnType) isType() {}

// SelfType is `Self` inside a trait or impl.
type SelfType struct{ Base }

func (*SelfType) isType() {}

// ErrorType stands in for a type the parser could not read.
type ErrorType struct{ Base }

func (*ErrorType) isType() {}

// ---------------------------------------------------------------------------
// Statements
// ---------------------------------------------------------------------------

// Stmt is a statement inside a block.
type Stmt interface {
	Node
	isStmt()
}

// LetStmt binds a pattern. Type is nil when the annotation is omitted.
type LetStmt struct {
	Base
	Mut   bool
	Pat   Pattern
	Type  Type
	Value Expr
}

func (*LetStmt) isStmt() {}

// ExprStmt is an expression evaluated for its effect. Semi records whether the source
// wrote the terminating semicolon, which block-bodied expressions may omit.
type ExprStmt struct {
	Base
	X    Expr
	Semi bool
}

func (*ExprStmt) isStmt() {}

// ItemStmt is an item declared inside a block.
type ItemStmt struct {
	Base
	Item Item
}

func (*ItemStmt) isStmt() {}

// ---------------------------------------------------------------------------
// Expressions
// ---------------------------------------------------------------------------

// Expr is an expression.
type Expr interface {
	Node
	isExpr()
}

// IntLit is an integer literal. Value is the magnitude; negation is a unary operator.
// Overflow reports that the literal does not fit in 64 bits at all.
type IntLit struct {
	Base
	Value    uint64
	Overflow bool
	Suffix   string
}

func (*IntLit) isExpr() {}

// FloatLit is a float literal.
type FloatLit struct {
	Base
	Value  float64
	Suffix string
}

func (*FloatLit) isExpr() {}

// StrLit is a string literal with escapes already decoded.
type StrLit struct {
	Base
	Value string
}

func (*StrLit) isExpr() {}

// CharLit is a character literal.
type CharLit struct {
	Base
	Value rune
}

func (*CharLit) isExpr() {}

// BoolLit is `true` or `false`.
type BoolLit struct {
	Base
	Value bool
}

func (*BoolLit) isExpr() {}

// PathExpr names a value, optionally instantiated: `f[i64]`.
type PathExpr struct {
	Base
	Path *Path
	Args []Type
}

func (*PathExpr) isExpr() {}

// SelfExpr is the `self` receiver.
type SelfExpr struct{ Base }

func (*SelfExpr) isExpr() {}

// FieldInit is one field of a struct literal.
type FieldInit struct {
	Base
	Name  Ident
	Value Expr
}

// StructLit constructs a struct or struct variant.
type StructLit struct {
	Base
	Path   *Path
	Args   []Type
	Fields []*FieldInit
}

func (*StructLit) isExpr() {}

// TupleExpr is `(a, b)`. A zero-element TupleExpr is the unit value `()`.
type TupleExpr struct {
	Base
	Elems []Expr
}

func (*TupleExpr) isExpr() {}

// LambdaParam is one parameter of a lambda; Type is nil when inferred.
type LambdaParam struct {
	Base
	Pat  Pattern
	Type Type
}

// Lambda is `|x| e` or `|x| -> T { ... }`.
type Lambda struct {
	Base
	Params []*LambdaParam
	Ret    Type
	Body   Expr
}

func (*Lambda) isExpr() {}

// Block is `{ stmts; tail }`. Tail is nil when the block's value is `()`.
type Block struct {
	Base
	Stmts []Stmt
	Tail  Expr
}

func (*Block) isExpr() {}

// If is `if cond { .. } else ..`. Else is nil, a *Block, or another *If.
type If struct {
	Base
	Cond Expr
	Then *Block
	Else Expr
}

func (*If) isExpr() {}

// MatchArm is one arm of a match. Guard is nil when the arm is unguarded.
type MatchArm struct {
	Base
	Pat   Pattern
	Guard Expr
	Body  Expr
}

// Match is `match scrutinee { arms }`.
type Match struct {
	Base
	Scrutinee Expr
	Arms      []*MatchArm
}

func (*Match) isExpr() {}

// While is `while cond { .. }`.
type While struct {
	Base
	Cond Expr
	Body *Block
}

func (*While) isExpr() {}

// For is `for pat in iter { .. }`.
type For struct {
	Base
	Pat  Pattern
	Iter Expr
	Body *Block
}

func (*For) isExpr() {}

// Loop is `loop { .. }`, whose value comes from its breaks.
type Loop struct {
	Base
	Body *Block
}

func (*Loop) isExpr() {}

// Break exits the innermost loop, optionally with a value.
type Break struct {
	Base
	Value Expr
}

func (*Break) isExpr() {}

// Continue restarts the innermost loop.
type Continue struct{ Base }

func (*Continue) isExpr() {}

// Return exits the enclosing function.
type Return struct {
	Base
	Value Expr
}

func (*Return) isExpr() {}

// UnaryOp is a prefix operator.
type UnaryOp int

const (
	// Neg is arithmetic negation `-`.
	Neg UnaryOp = iota
	// Not is logical negation `!`.
	Not
)

func (o UnaryOp) String() string {
	if o == Not {
		return "!"
	}
	return "-"
}

// Unary is a prefix operator application.
type Unary struct {
	Base
	Op UnaryOp
	X  Expr
}

func (*Unary) isExpr() {}

// BinaryOp is an infix operator.
type BinaryOp int

// The infix operators, in the precedence order of spec/02-grammar.md.
const (
	OrOr BinaryOp = iota
	AndAnd
	Eq
	Ne
	Lt
	Le
	Gt
	Ge
	BitOr
	BitXor
	BitAnd
	Shl
	Shr
	Add
	Sub
	Mul
	Div
	Rem
)

var binaryOpNames = [...]string{
	OrOr: "||", AndAnd: "&&",
	Eq: "==", Ne: "!=", Lt: "<", Le: "<=", Gt: ">", Ge: ">=",
	BitOr: "|", BitXor: "^", BitAnd: "&", Shl: "<<", Shr: ">>",
	Add: "+", Sub: "-", Mul: "*", Div: "/", Rem: "%",
}

func (o BinaryOp) String() string {
	if int(o) < len(binaryOpNames) && binaryOpNames[o] != "" {
		return binaryOpNames[o]
	}
	return fmt.Sprintf("binop(%d)", int(o))
}

// IsComparison reports whether o is one of the non-associative comparison operators.
func (o BinaryOp) IsComparison() bool { return o >= Eq && o <= Ge }

// Binary is an infix operator application.
type Binary struct {
	Base
	Op   BinaryOp
	L, R Expr
}

func (*Binary) isExpr() {}

// AssignOp distinguishes plain assignment from the compound forms.
type AssignOp int

const (
	// Set is plain `=`.
	Set AssignOp = iota
	// AddAssign is `+=`, and so on for the rest.
	AddAssign
	SubAssign
	MulAssign
	DivAssign
	RemAssign
)

var assignOpNames = [...]string{Set: "=", AddAssign: "+=", SubAssign: "-=", MulAssign: "*=", DivAssign: "/=", RemAssign: "%="}

func (o AssignOp) String() string {
	if int(o) < len(assignOpNames) && assignOpNames[o] != "" {
		return assignOpNames[o]
	}
	return fmt.Sprintf("assignop(%d)", int(o))
}

// BinaryOp returns the arithmetic operator a compound assignment applies, and false for
// plain assignment.
func (o AssignOp) BinaryOp() (BinaryOp, bool) {
	switch o {
	case AddAssign:
		return Add, true
	case SubAssign:
		return Sub, true
	case MulAssign:
		return Mul, true
	case DivAssign:
		return Div, true
	case RemAssign:
		return Rem, true
	}
	return 0, false
}

// Assign stores into a place. Its value is always `()`.
type Assign struct {
	Base
	Op    AssignOp
	Place Expr
	Value Expr
}

func (*Assign) isExpr() {}

// Cast is `x as T`, the only conversion in Origin.
type Cast struct {
	Base
	X    Expr
	Type Type
}

func (*Cast) isExpr() {}

// Call applies a function value to arguments.
type Call struct {
	Base
	Fn   Expr
	Args []Expr
}

func (*Call) isExpr() {}

// MethodCall is `recv.name(args)`.
type MethodCall struct {
	Base
	Recv Expr
	Name Ident
	Args []Expr
}

func (*MethodCall) isExpr() {}

// FieldAccess is `recv.name`.
type FieldAccess struct {
	Base
	Recv Expr
	Name Ident
}

func (*FieldAccess) isExpr() {}

// Try is the `?` operator.
type Try struct {
	Base
	X Expr
}

func (*Try) isExpr() {}

// ErrorExpr stands in for an expression the parser could not read.
type ErrorExpr struct{ Base }

func (*ErrorExpr) isExpr() {}

// ---------------------------------------------------------------------------
// Patterns
// ---------------------------------------------------------------------------

// Pattern is a match pattern.
type Pattern interface {
	Node
	isPattern()
}

// WildcardPat is `_`.
type WildcardPat struct{ Base }

func (*WildcardPat) isPattern() {}

// LitPat matches a literal value.
type LitPat struct {
	Base
	// Neg records a leading `-` on a numeric literal pattern.
	Neg bool
	Lit Expr
}

func (*LitPat) isPattern() {}

// BindPat introduces a binding, or matches a unit variant or constant when the name
// resolves to one. Which of the two it is is decided by name resolution, not parsing
// (spec/02-grammar.md).
type BindPat struct {
	Base
	Mut  bool
	Name Ident
	// Sub is the `x @ p` subpattern, nil when absent.
	Sub Pattern
}

func (*BindPat) isPattern() {}

// PathPat matches a constructor: a unit, tuple or struct variant.
type PathPat struct {
	Base
	Path *Path
	Kind VariantKind
	// Elems is set for TupleVariant.
	Elems []Pattern
	// Fields is set for StructVariant; Rest records a trailing `..`.
	Fields []*FieldPat
	Rest   bool
}

func (*PathPat) isPattern() {}

// FieldPat is one field of a struct pattern.
type FieldPat struct {
	Base
	Name Ident
	Pat  Pattern
}

// TuplePat matches a tuple element-wise.
type TuplePat struct {
	Base
	Elems []Pattern
}

func (*TuplePat) isPattern() {}

// OrPat matches any of its alternatives, which must bind identical names.
type OrPat struct {
	Base
	Alts []Pattern
}

func (*OrPat) isPattern() {}

// ErrorPat stands in for a pattern the parser could not read.
type ErrorPat struct{ Base }

func (*ErrorPat) isPattern() {}
