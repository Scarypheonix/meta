package parse

import (
	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/lex"
)

// List literals (spec/13-collections.md, ADR-0039).
//
// `[a, b, c]` is desugared here, in the parser, into the calls it means:
//
//	{ let xs = std::list::new(); xs.push(a); xs.push(b); xs.push(c); xs }
//
// Which is `interp.go`'s move again, and it is what keeps collections a library
// (ADR-0028): `List` stays an ordinary prelude type, and the literal is a spelling of two
// calls rather than a form the checker or any engine has to know about. Hardcoding two
// prelude names in the parser is what `for` already does with `into_iter` and `next`, and
// interpolation with `to_str` and `concat`.
//
// Evaluation order falls out right: §04 requires strict left-to-right, and the pushes are
// emitted in source order.
//
// The path is fully qualified because a literal is a form, not a call to a name the program
// has to have in scope -- `[1, 2]` must work in a file with no `use std::list;`, and
// `std::list::new` resolves from anywhere without one.
//
// The element type is left to inference: each `push` constrains it, so `[1, 2]` is a
// `List[i64]`. `[]` constrains nothing and fails as an ordinary E0309, fixed by an
// annotation, rather than needing a rule of its own.

// listBinding is the name the desugaring binds the new list to. It is deliberately not a
// valid identifier, so no program can name it and no user binding can collide with one --
// including a nested literal's, since each literal is its own block. It appears verbatim in
// an AST dump, which is correct: the binding is synthesized, and disguising it as an
// ordinary name would be lying about the tree.
const listBinding = "[list]"

// parseListLit parses `[e1, e2, ..., en]` with `[` current.
func (p *Parser) parseListLit(start int) ast.Expr {
	saved := p.noStruct
	p.noStruct = false
	defer func() { p.noStruct = saved }()

	p.advance() // [
	var elems []ast.Expr
	for !p.at(lex.RBracket) && !p.atEOF() {
		elems = append(elems, p.parseExpr())
		if !p.eat(lex.Comma) {
			break
		}
	}
	p.expect(lex.RBracket)

	name := ast.Ident{Name: listBinding, Loc: p.base(start).Loc}

	bind := &ast.BindPat{Name: name}
	bind.Base = p.base(start)
	let := &ast.LetStmt{Pat: bind, Value: p.call(p.path(start, "std", "list", "new"), start)}
	let.Base = p.base(start)

	stmts := []ast.Stmt{let}
	for _, e := range elems {
		push := &ast.ExprStmt{X: p.methodCall(p.pathIdent(start, name), "push", start, e), Semi: true}
		push.Base = p.base(start)
		stmts = append(stmts, push)
	}

	block := &ast.Block{Stmts: stmts, Tail: p.pathIdent(start, name)}
	block.Base = p.base(start)
	return block
}

// path builds a multi-segment path expression such as `std::list::new`.
func (p *Parser) path(start int, segs ...string) ast.Expr {
	base := p.base(start)
	idents := make([]ast.Ident, 0, len(segs))
	for _, s := range segs {
		idents = append(idents, ast.Ident{Name: s, Loc: base.Loc})
	}
	path := &ast.Path{Segments: idents}
	path.Base = p.base(start)
	e := &ast.PathExpr{Path: path}
	e.Base = base
	return e
}

// pathIdent builds a one-segment path expression naming an existing binding.
func (p *Parser) pathIdent(start int, name ast.Ident) ast.Expr {
	path := &ast.Path{Segments: []ast.Ident{name}}
	path.Base = p.base(start)
	e := &ast.PathExpr{Path: path}
	e.Base = p.base(start)
	return e
}

// call builds `fn(args...)`.
func (p *Parser) call(fn ast.Expr, start int, args ...ast.Expr) ast.Expr {
	c := &ast.Call{Fn: fn, Args: args}
	c.Base = p.base(start)
	return c
}
