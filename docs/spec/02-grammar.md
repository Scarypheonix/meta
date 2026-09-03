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
Lambda       = "|" [ LambdaParams ] "|" ( Expr | "->" Type Block ) ;
LambdaParams = LambdaParam { "," LambdaParam } [ "," ] ;
LambdaParam  = Pattern [ ":" Type ] ;

IfExpr       = "if" ExprNoStruct Block [ "else" ( IfExpr | Block ) ] ;
MatchExpr    = "match" ExprNoStruct "{" { MatchArm } "}" ;
MatchArm     = Pattern [ "if" Expr ] "=>" ( ExprWithBlock [ "," ] | Expr "," ) ;
WhileExpr    = "while" ExprNoStruct Block ;
ForExpr      = "for" Pattern "in" ExprNoStruct Block ;
LoopExpr     = "loop" Block ;
```

`FieldInit` written as a bare `Ident` is shorthand for `Ident: Ident`.

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
a unit variant unintentionally is a common bug, so the compiler MUST emit a warning when
a binding pattern's name differs from an in-scope unit variant only by case.

In Origin 0.1 this rule can only fire for a `const`: an enum variant is never in scope
unqualified, because there are no glob imports (§07) and a variant is always written
`Enum::Variant`. The unit-variant half of the rule becomes reachable when glob imports
land (Phase 7), and is specified now so that adding them does not silently change the
meaning of existing patterns.

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
