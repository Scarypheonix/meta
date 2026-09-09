package parse

import (
	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/lex"
)

// `if let` and `while let` (spec/02-grammar.md, ADR-0038).
//
// Both are desugared here, in the parser, into the forms they already mean:
//
//	if let p = e { a } else { b }  ->  match e { p => a, _ => b }
//	if let p = e { a }             ->  match e { p => a, _ => () }
//	while let p = e { body }       ->  loop { match e { p => body, _ => break } }
//
// This is `interp.go`'s move, for its reason: nothing downstream learns a new form. The
// resolver, the checker, exhaustiveness, monomorphization and all three engines see a
// `Match` and a `Loop` they already handle, and the alternative -- an `ast.IfLet` carried
// through the pipeline -- would need a rule in each of them saying the same thing, in two
// languages, since `stage1/src` is a second implementation of this front end.
//
// `break` and `continue` in a `while let` body need nothing special: `internal/resolve`'s
// loopDepth counts `while`, `for` and `loop` and not `match`, so the `loop` introduced here
// is the innermost one, which is the loop the programmer meant. `continue` therefore
// re-evaluates the scrutinee, which is what `while let` means.
//
// The one thing the desugaring cannot hide is a diagnostic. An irrefutable pattern makes
// the `_` arm unreachable, and E0006 against an arm nobody wrote would be a diagnostic that
// lies about where the problem is -- so the Match records which construct it came from and
// the usefulness check reports E0008 instead.

// parseIfLet parses an `if let`, with `if` consumed and `let` current.
func (p *Parser) parseIfLet(start int) ast.Expr {
	pat, scrut := p.parseLetCond()
	then := p.parseBlock()

	var els ast.Expr
	if p.eat(lex.KwElse) {
		if p.at(lex.KwIf) {
			els = p.parseIf(p.pos)
		} else {
			els = p.parseBlock()
		}
	} else {
		els = p.unitExpr(start)
	}
	return p.letMatch(start, ast.LetFormIf, pat, scrut, then, els)
}

// parseWhileLet parses a `while let`, with `while` consumed and `let` current.
func (p *Parser) parseWhileLet(start int) ast.Expr {
	pat, scrut := p.parseLetCond()
	body := p.parseBlock()

	brk := &ast.Break{}
	brk.Base = p.base(start)

	inner := &ast.Block{Tail: p.letMatch(start, ast.LetFormWhile, pat, scrut, body, brk)}
	inner.Base = p.base(start)
	loop := &ast.Loop{Body: inner}
	loop.Base = p.base(start)
	return loop
}

// parseLetCond parses `let p = e`, with `let` current. The scrutinee is parsed with struct
// literals excluded at the top level, for the reason every other condition position does:
// the `{` that follows opens the body.
func (p *Parser) parseLetCond() (ast.Pattern, ast.Expr) {
	p.advance() // let
	pat := p.parsePattern()
	p.expect(lex.Assign)
	return pat, p.parseExprNoStruct()
}

// letMatch builds the two-arm `match` an `if let` or `while let` means. Every synthesized
// node carries the whole construct's span, so a diagnostic inside either arm underlines
// what the programmer wrote rather than a location that exists only in the desugaring.
func (p *Parser) letMatch(start int, form ast.LetForm, pat ast.Pattern, scrut ast.Expr, then *ast.Block, els ast.Expr) ast.Expr {
	hit := &ast.MatchArm{Pat: pat, Body: then}
	hit.Base = p.base(start)

	wild := &ast.WildcardPat{}
	wild.Base = p.base(start)
	miss := &ast.MatchArm{Pat: wild, Body: els}
	miss.Base = p.base(start)

	m := &ast.Match{Scrutinee: scrut, Arms: []*ast.MatchArm{hit, miss}, LetForm: form}
	m.Base = p.base(start)
	return m
}

// unitExpr is `()`, which is what an `if` without an `else` evaluates to.
func (p *Parser) unitExpr(start int) ast.Expr {
	u := &ast.TupleExpr{}
	u.Base = p.base(start)
	return u
}
