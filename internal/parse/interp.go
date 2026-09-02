package parse

import (
	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/lex"
)

// String interpolation (spec/14-strings.md).
//
// `"total: \(n + 1)"` is desugared here, in the parser, into the method calls it means:
//
//	"total: ".concat((n + 1).to_str())
//
// Desugaring to nodes that already exist is the whole point. Nothing downstream learns a
// new form: the checker sees `concat` and `to_str` and applies `Str` and `Show` to them
// like any other call, monomorphization instantiates them like any other call, and the
// three engines already run both. The bound a program gets for free -- an interpolated
// value must implement `Show` -- is the one it would have got by writing the call itself.
//
// The alternative, an `ast.Interp` node carried to the checker, would have needed a rule
// in every one of those places to say the same thing.

// interpolated builds the expression an interpolated string literal denotes. The token's
// parts alternate literal text and expression tokens, and the lexer has already made sure
// there is at least one of the latter.
func (p *Parser) interpolated(t lex.Token, start int) ast.Expr {
	var out ast.Expr
	for _, part := range t.Parts {
		var piece ast.Expr
		if part.Expr == nil {
			if part.Text == "" {
				continue // an empty chunk between two interpolations contributes nothing
			}
			lit := &ast.StrLit{Value: part.Text}
			lit.Base = p.base(start)
			piece = lit
		} else {
			piece = p.methodCall(p.subExpr(part.Expr, start), "to_str", start)
		}
		if out == nil {
			out = piece
			continue
		}
		out = p.methodCall(out, "concat", start, piece)
	}
	if out == nil {
		// Every part was an empty literal chunk, which the lexer only produces for `""`
		// itself -- an interpolated literal always has an expression part.
		lit := &ast.StrLit{Value: ""}
		lit.Base = p.base(start)
		return lit
	}
	return out
}

// subExpr parses one interpolation's tokens with a parser of its own.
//
// It shares the file, the diagnostic bag and the id generator, so spans point into the
// real source and node ids stay unique across the whole compilation -- which is what lets
// a diagnostic inside an interpolation underline the expression the programmer wrote.
func (p *Parser) subExpr(toks []lex.Token, start int) ast.Expr {
	sub := &Parser{file: p.file, toks: toks, bag: p.bag, ids: p.ids}
	e := sub.parseExpr()
	if !sub.atEOF() {
		sub.errorAt(sub.cur().Span, "unexpected %s after the interpolated expression", sub.cur()).
			Label("an interpolation holds exactly one expression")
	}
	if e == nil {
		lit := &ast.StrLit{Value: ""}
		lit.Base = p.base(start)
		return lit
	}
	return e
}

// methodCall builds `recv.name(args...)` with the whole literal's span, since none of
// these calls is anything the programmer wrote a location for.
func (p *Parser) methodCall(recv ast.Expr, name string, start int, args ...ast.Expr) ast.Expr {
	base := p.base(start)
	call := &ast.MethodCall{
		Recv: recv,
		Name: ast.Ident{Name: name, Loc: base.Loc},
		Args: args,
	}
	call.Base = base
	return call
}
