# 02 — Surface Grammar

EBNF notation: `{ x }` is zero or more, `[ x ]` is optional, `|` alternates, quoted
text is literal, `X = ... ;` defines a nonterminal. Terminals produced by the lexer
(§01) are `Ident`, `IntLit`, `FloatLit`, `StringLit`, `CharLit`.

This grammar is **LL(2)** apart from two documented restrictions (§ "Parser
restrictions" below). No production requires knowing whether a name denotes a type or
a value; that distinction is made during name resolution (§07).

## Source file

```ebnf
SourceFile   = { UseDecl } { Item } EOF ;

UseDecl      = "use" Path [ "::" "{" UseList "}" ] ";" ;
UseList      = Ident { "," Ident } [ "," ] ;
Path         = Ident { "::" Ident } ;

Item         = [ "pub" ] ItemKind ;
ItemKind     = FnDecl
             | StructDecl
             | EnumDecl
             | TraitDecl
             | ImplDecl
             | TypeAliasDecl
             | ConstDecl ;
```

## Items

```ebnf
FnDecl       = "fn" Ident [ Generics ] "(" [ ParamList ] ")"
               [ "->" Type ] [ WhereClause ] Block ;

ParamList    = ( SelfParam | Param ) { "," Param } [ "," ] ;
SelfParam    = [ "mut" ] "self" ;
Param        = [ "mut" ] Pattern ":" Type ;

StructDecl   = "struct" Ident [ Generics ] [ WhereClause ]
               "{" [ FieldList ] "}" ;
FieldList    = Field { "," Field } [ "," ] ;
Field        = [ "pub" ] [ "mut" ] Ident ":" Type ;

EnumDecl     = "enum" Ident [ Generics ] [ WhereClause ]
               "{" [ VariantList ] "}" ;
VariantList  = Variant { "," Variant } [ "," ] ;
Variant      = Ident [ "(" TypeList ")" | "{" FieldList "}" ] ;

TraitDecl    = "trait" Ident [ Generics ] [ ":" TraitBounds ] [ WhereClause ]
               "{" { TraitMember } "}" ;
TraitMember  = AssocTypeDecl | TraitFn ;
AssocTypeDecl= "type" Ident [ ":" TraitBounds ] ";" ;
TraitFn      = "fn" Ident [ Generics ] "(" [ ParamList ] ")"
               [ "->" Type ] [ WhereClause ] ( Block | ";" ) ;

ImplDecl     = "impl" [ Generics ] [ TraitRef "for" ] Type [ WhereClause ]
               "{" { ImplMember } "}" ;
ImplMember   = [ "pub" ] ( AssocTypeDef | FnDecl ) ;
AssocTypeDef = "type" Ident "=" Type ";" ;

TypeAliasDecl= "type" Ident [ Generics ] "=" Type ";" ;
ConstDecl    = "const" Ident ":" Type "=" Expr ";" ;
```

## Generics

```ebnf
Generics     = "[" GenericParam { "," GenericParam } [ "," ] "]" ;
GenericParam = Ident [ ":" TraitBounds ] ;
TraitBounds  = TraitRef { "+" TraitRef } ;
TraitRef     = Path [ "[" TypeList "]" ] ;
WhereClause  = "where" WherePred { "," WherePred } [ "," ] ;
WherePred    = Type ":" TraitBounds ;
```

## Types

```ebnf
Type         = PathType | TupleType | UnitType | FnType | SelfType ;
PathType     = Path [ "[" TypeList "]" ] ;
TypeList     = Type { "," Type } [ "," ] ;
TupleType    = "(" Type "," [ Type { "," Type } [ "," ] ] ")" ;
UnitType     = "(" ")" ;
FnType       = "fn" "(" [ TypeList ] ")" "->" Type ;
SelfType     = "Self" ;
```

`(T)` is the type `T` in parentheses, not a one-tuple. A one-tuple is written `(T,)`.
The same rule holds for tuple expressions and tuple patterns.

## Statements and blocks

```ebnf
Block        = "{" { Stmt } [ Expr ] "}" ;
Stmt         = LetStmt | ItemStmt | ExprStmt ;
LetStmt      = "let" [ "mut" ] Pattern [ ":" Type ] "=" Expr ";" ;
ItemStmt     = Item ;
ExprStmt     = ExprWithBlock [ ";" ] | ExprWithoutBlock ";" ;
```

A block's value is its trailing `Expr` if present, otherwise `()`. `ExprWithBlock` is
any of `Block`, `IfExpr`, `MatchExpr`, `WhileExpr`, `ForExpr`, `LoopExpr`; used as a
statement it needs no `;`, and it **ends there**: the parser does not carry on into a
binary operator. So

```origin
if b > 0 { return 1; }
-1
```

is a statement and then the block's value, not a subtraction from `()`.

## Expressions

Precedence, lowest to highest. All levels are left-associative except assignment
(right) and comparison (non-associative).

| Level | Operators | Associativity |
|---|---|---|
| 1 | `=` `+=` `-=` `*=` `/=` `%=` | right |
| 2 | `\|\|` | left |
| 3 | `&&` | left |
| 4 | `==` `!=` `<` `<=` `>` `>=` | **non-associative** |
| 5 | `\|` | left |
| 6 | `^` | left |
| 7 | `&` | left |
| 8 | `<<` `>>` | left |
| 9 | `+` `-` | left |
| 10 | `*` `/` `%` | left |
| 11 | `as` | left |
| 12 | `-` `!` (prefix) | — |
| 13 | `.field` `.method(...)` `(...)` `?` (postfix) | left |
| 14 | primary | — |

`a < b < c` is REJECTED with "comparison operators are non-associative; parenthesize".

```ebnf
Expr         = Assign ;
Assign       = Or [ AssignOp Assign ] ;
AssignOp     = "=" | "+=" | "-=" | "*=" | "/=" | "%=" ;
Or           = And { "||" And } ;
And          = BitOr { "&&" BitOr } ;
Cmp          = BitOr [ CmpOp BitOr ] ;
CmpOp        = "==" | "!=" | "<" | "<=" | ">" | ">=" ;
BitOr        = BitXor { "|" BitXor } ;
BitXor       = BitAnd { "^" BitAnd } ;
BitAnd       = Shift { "&" Shift } ;
Shift        = Additive { ( "<<" | ">>" ) Additive } ;
Additive     = Multiplicative { ( "+" | "-" ) Multiplicative } ;
Multiplicative = Cast { ( "*" | "/" | "%" ) Cast } ;
Cast         = Unary { "as" Type } ;
Unary        = [ "-" | "!" ] Unary | Postfix ;
Postfix      = Primary { PostfixOp } ;
PostfixOp    = "." Ident [ "(" [ ArgList ] ")" ]
             | "(" [ ArgList ] ")"
             | "?" ;
ArgList      = Expr { "," Expr } [ "," ] ;
```

Note: `Cmp` sits at level 4 between `And` and `BitOr` in the operator table; the
production chain above is written flat for readability, and `And = Cmp { "&&" Cmp }`
is the operative rule.

```ebnf
Primary      = Literal
             | PathExpr
             | StructLit
             | TupleOrParen
             | ListLit
             | Lambda
             | Block
             | IfExpr
             | MatchExpr
             | WhileExpr
             | ForExpr
             | LoopExpr
             | "break" [ Expr ]
             | "continue"
             | "return" [ Expr ]
             | "self" ;

Literal      = IntLit | FloatLit | StringLit | CharLit | "true" | "false" ;
PathExpr     = Path [ "[" TypeList "]" ] ;
StructLit    = Path [ "[" TypeList "]" ] "{" [ FieldInitList ] "}" ;
FieldInitList= FieldInit { "," FieldInit } [ "," ] ;
FieldInit    = Ident ":" Expr | Ident ;
TupleOrParen = "(" ")"
             | "(" Expr ")"
             | "(" Expr "," [ Expr { "," Expr } [ "," ] ] ")" ;
ListLit      = "[" [ Expr { "," Expr } [ "," ] ] "]" ;
Lambda       = "|" [ LambdaParams ] "|" ( Expr | "->" Type Block ) ;
LambdaParams = LambdaParam { "," LambdaParam } [ "," ] ;
LambdaParam  = Pattern [ ":" Type ] ;

IfExpr       = "if" ( LetCond | ExprNoStruct ) Block [ "else" ( IfExpr | Block ) ] ;
MatchExpr    = "match" ExprNoStruct "{" { MatchArm } "}" ;
MatchArm     = Pattern [ "if" Expr ] "=>" ( ExprWithBlock [ "," ] | Expr "," ) ;
WhileExpr    = "while" ( LetCond | ExprNoStruct ) Block ;
LetCond      = "let" Pattern "=" ExprNoStruct ;
ForExpr      = "for" Pattern "in" ExprNoStruct Block ;
LoopExpr     = "loop" Block ;
```

`ListLit` is the one place `[` begins an expression rather than type arguments, and the
two never compete: `[` is a list literal only where an expression may *start*, and
ADR-0013's rule — `[` after an expression is always type application — is unchanged. In
`foo [1, 2]` the parser is still inside `foo`'s expression, so that is `foo` instantiated
at two types; `ExprStmt` requires a `;` after a non-block expression, so writing the two as
separate statements (`foo; [1, 2]`) is unambiguous.

`FieldInit` written as a bare `Ident` is shorthand for `Ident: Ident`.

## Desugarings

Three surface forms are defined by rewriting, in the parser, into forms that already
exist. This is normative: the rewritten program is what the rest of the specification
applies to, so nothing downstream — name resolution, typing, exhaustiveness, evaluation
order — needs a separate rule for any of them.

| Source | Means |
|---|---|
| `if let p = e { a } else { b }` | `match e { p => a, _ => b }` |
| `if let p = e { a }` | `match e { p => a, _ => () }` |
| `while let p = e { body }` | `loop { match e { p => body, _ => break } }` |
| `[e1, e2, e3]` | a block that builds a `List`, pushes `e1`, `e2`, `e3` in order, and evaluates to it |

Consequences that follow from the rewriting rather than from a rule of their own:

- `break` and `continue` inside a `while let` body bind to the `loop` the rewriting
  introduces, because `match` is not a loop. `continue` therefore re-evaluates the
  scrutinee, which is what `while let` means.
- The `else` of an `if let` is reached when the pattern does not match, and an `if let`
  without one has type `()`, exactly as an `if` without an `else` does (§04).
- A list literal evaluates its elements strictly left to right (§04), because the pushes
  are emitted in source order.
- The element type of a list literal is inferred from its elements. `[]` constrains
  nothing and is REJECTED as `E0309` unless context supplies the type, as in
  `let xs: List[i64] = [];`.

An `if let` or `while let` whose pattern is **irrefutable** is REJECTED as `E0008`, with a
help naming `let`. This is the mirror of `E0005` — a refutable pattern in a `let` — and the
two are errors for the same reason: a pattern in the wrong position is a mistake about
what the code does, not a stylistic choice.

## Patterns

```ebnf
Pattern      = PatternNoOr { "|" PatternNoOr } ;
PatternNoOr  = "_"
             | Literal
             | [ "mut" ] Ident [ "@" PatternNoOr ]
             | Path [ "(" [ PatternList ] ")" | "{" [ FieldPatList ] "}" ]
             | TuplePattern ;
PatternList  = Pattern { "," Pattern } [ "," ] ;
FieldPatList = FieldPat { "," FieldPat } [ "," ] [ ".." ] ;
FieldPat     = Ident ":" Pattern | Ident ;
TuplePattern = "(" ")"
             | "(" Pattern ")"
             | "(" Pattern "," [ Pattern { "," Pattern } [ "," ] ] ")" ;
```

A bare `Ident` pattern is ambiguous between a fresh binding and a reference to a
unit enum variant or a `const`. **Resolution rule:** if the identifier resolves in scope
to a unit variant or a constant, the pattern matches that value; otherwise it introduces
a binding. This is a name-resolution decision, not a parsing one. A pattern that shadows
a unit variant unintentionally is a common bug, so the compiler MUST emit `W0003` when a
binding pattern's name differs from an in-scope unit variant only by case, **in a
refutable position** — a `match` arm or an `if let`/`while let` pattern.

The warning is confined to refutable positions because that is where the confusion is
possible. In a `let`, a parameter or a `for`, a name that resolved to a unit variant would
make the pattern refutable and `E0005` already rejects it, so a binding there cannot
silently have become a match. Warning about them as well would fire on
`Ord::cmp(self, other: Self)` for every implementor, since `IoError::Other` is a unit
variant in scope.

Both halves of the rule are reachable. A `const` has always been; a unit variant became
one when the prelude's enums put their variants in scope (§07, ADR-0037). A variant of a
user enum is still written `Enum::Variant` and cannot fire the rule, because there are no
glob imports (§07).

## Parser restrictions

Two restrictions keep the grammar unambiguous. Both are REJECTED with a diagnostic that
names the fix.

1. **No struct literals in condition position.** `ExprNoStruct` is `Expr` with
   `StructLit` removed at the top level of the expression (nested inside parentheses,
   call arguments, or a block, struct literals are fine). This makes
   `if x { }` unambiguously an `if` with condition `x`. To use a struct literal there,
   parenthesize: `if (Point { x: 1.0, y: 2.0 }).is_origin() { }`.
2. **No index operator.** `a[i]` is never an expression. `[` after an expression always
   begins type arguments, so `f[i64](x)` is an explicit instantiation and needs no
   turbofish. Indexing is done with methods: `v.get(i) -> Option[T]` and `v.at(i) -> T`
   (which TRAPS out of range). See ADR-0013.

## Parser error recovery

The parser MUST report multiple syntax errors in a single pass (Phase 1 exit
criterion). The recovery strategy is normative:

- Errors are recorded in a diagnostic bag; parsing continues.
- On an error inside an item, skip tokens until a token in the item-start set
  (`pub fn struct enum trait impl type const use`) at brace-depth zero, or EOF.
- On an error inside a block, skip until `;` or `}` at the current brace depth.
- On an error inside a delimited list, skip until `,` or the closing delimiter.
- Brace/paren/bracket matching is tracked so that recovery never scans past the closing
  delimiter of the construct being parsed.
- Erroneous subtrees are represented by an explicit `Error` AST node with a span. Later
  passes MUST treat `Error` as "already reported" and MUST NOT emit cascading
  diagnostics from it.
- The parser MUST terminate on every input. The fuzz target `tests/fuzz/parse` asserts
  no panic, no hang, and that at least one diagnostic exists whenever the AST contains
  an `Error` node.

## Worked examples

| Source | Parse |
|---|---|
| `a + b * c` | `(a + (b * c))` |
| `a * b as i64` | `(a * (b as i64))` — cast binds tighter than `*` |
| `-x as i64` | `((-x) as i64)` — unary binds tighter than `as` |
| `a == b == c` | REJECTED, non-associative |
| `x = y = 1` | `x = (y = 1)`, right-associative; both must be places |
| `f[i64](3)` | call of `f` instantiated at `i64` |
| `v.get(0)` | method call; there is no `v[0]` |
| `if p { 1 } else { 2 }` | `if` expression of type `i64` |
| `if Point { x: 1.0 } .. ` | REJECTED, struct literal in condition position |
| `(1,)` | one-tuple |
| `(1)` | the integer `1`, parenthesized |
| `\|x\| x + 1` | lambda |
| `match v { Some(n) if n > 0 => n, _ => 0 }` | guarded arm |
| `[1, 2, 3]` | a `List[i64]` of three elements |
| `[]` | REJECTED as `E0309` unless context gives the element type |
| `f [i64]` | `f` instantiated at `i64`, not `f` then a list — `[` after an expression is type application |
| `if let Some(n) = o { n } else { 0 }` | `match o { Some(n) => n, _ => 0 }` |
| `while let Some(n) = it.next() { .. }` | `loop { match it.next() { Some(n) => .., _ => break } }` |
| `if let x = e { .. }` | REJECTED as `E0008` — irrefutable pattern |
