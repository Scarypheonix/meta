package parse

import (
	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/lex"
)

// ---------------------------------------------------------------------------
// Blocks and statements
// ---------------------------------------------------------------------------

// parseBlock reads `{ stmt* tail? }`. A block's value is its trailing expression, or
// `()` when there is none.
func (p *Parser) parseBlock() *ast.Block {
	start := p.pos
	saved := p.noStruct
	p.noStruct = false
	defer func() { p.noStruct = saved }()

	if _, ok := p.expect(lex.LBrace); !ok {
		return &ast.Block{Base: p.base(start)}
	}
	b := &ast.Block{}
	for !p.at(lex.RBrace) && !p.atEOF() {
		before := p.pos
		stmt, tail := p.parseStmt()
		if tail != nil {
			b.Tail = tail
			break
		}
		if stmt != nil {
			b.Stmts = append(b.Stmts, stmt)
		}
		if p.pos == before {
			p.advance() // guarantee progress
		}
	}
	p.expect(lex.RBrace)
	b.Base = p.base(start)
	return b
}

// parseStmt returns either a statement or, when the construct is the block's trailing
// expression, that expression. Exactly one of the two is non-nil.
func (p *Parser) parseStmt() (ast.Stmt, ast.Expr) {
	start := p.pos

	if p.at(lex.KwLet) {
		return p.parseLet(start), nil
	}
	if isItemStart(p.cur().Kind) {
		item := p.parseItem()
		s := &ast.ItemStmt{Item: item}
		s.Base = p.base(start)
		return s, nil
	}

	e := p.parseExpr()

	switch {
	case p.eat(lex.Semi):
		s := &ast.ExprStmt{X: e, Semi: true}
		s.Base = p.base(start)
		return s, nil
	case p.at(lex.RBrace) || p.atEOF():
		return nil, e // the block's value
	case isBlockExpr(e):
		// A block-bodied expression used as a statement needs no semicolon.
		s := &ast.ExprStmt{X: e, Semi: false}
		s.Base = p.base(start)
		return s, nil
	default:
		p.errorExpected("`;` after this expression")
		p.syncStmt()
		s := &ast.ExprStmt{X: e, Semi: false}
		s.Base = p.base(start)
		return s, nil
	}
}

// isBlockExpr reports whether e is one of the expressions whose body is braced, and
// which may therefore be used as a statement without a trailing semicolon.
func isBlockExpr(e ast.Expr) bool {
	switch e.(type) {
	case *ast.Block, *ast.If, *ast.Match, *ast.While, *ast.For, *ast.Loop:
		return true
	}
	return false
}

func (p *Parser) parseLet(start int) ast.Stmt {
	p.advance() // let
	l := &ast.LetStmt{}
	l.Mut = p.eat(lex.KwMut)
	l.Pat = p.parsePattern()
	if p.eat(lex.Colon) {
		l.Type = p.parseType()
	}
	if _, ok := p.expect(lex.Assign); ok {
		l.Value = p.parseExpr()
	} else {
		l.Value = &ast.ErrorExpr{Base: p.base(start)}
		p.syncStmt()
		l.Base = p.base(start)
		return l
	}
	p.expect(lex.Semi)
	l.Base = p.base(start)
	return l
}

// ---------------------------------------------------------------------------
// Expressions, by descending precedence (spec/02-grammar.md)
// ---------------------------------------------------------------------------

func (p *Parser) parseExpr() ast.Expr { return p.parseAssign() }

// parseExprNoStruct parses an expression in condition position, where a top-level
// struct literal would be ambiguous with the following block.
func (p *Parser) parseExprNoStruct() ast.Expr {
	saved := p.noStruct
	p.noStruct = true
	e := p.parseExpr()
	p.noStruct = saved
	return e
}

var assignOps = map[lex.Kind]ast.AssignOp{
	lex.Assign: ast.Set, lex.PlusEq: ast.AddAssign, lex.MinusEq: ast.SubAssign,
	lex.StarEq: ast.MulAssign, lex.SlashEq: ast.DivAssign, lex.PercentEq: ast.RemAssign,
}

func (p *Parser) parseAssign() ast.Expr {
	start := p.pos
	lhs := p.parseOr()
	op, ok := assignOps[p.cur().Kind]
	if !ok {
		return lhs
	}
	p.advance()
	rhs := p.parseAssign() // right-associative
	a := &ast.Assign{Op: op, Place: lhs, Value: rhs}
	a.Base = p.base(start)
	return a
}

func (p *Parser) parseOr() ast.Expr {
	start := p.pos
	left := p.parseAnd()
	for p.at(lex.PipePipe) {
		p.advance()
		right := p.parseAnd()
		b := &ast.Binary{Op: ast.OrOr, L: left, R: right}
		b.Base = p.base(start)
		left = b
	}
	return left
}

func (p *Parser) parseAnd() ast.Expr {
	start := p.pos
	left := p.parseCmp()
	for p.at(lex.AmpAmp) {
		p.advance()
		right := p.parseCmp()
		b := &ast.Binary{Op: ast.AndAnd, L: left, R: right}
		b.Base = p.base(start)
		left = b
	}
	return left
}

var cmpOps = map[lex.Kind]ast.BinaryOp{
	lex.EqEq: ast.Eq, lex.BangEq: ast.Ne,
	lex.Lt: ast.Lt, lex.Le: ast.Le, lex.Gt: ast.Gt, lex.Ge: ast.Ge,
}

// parseCmp implements the non-associativity of comparison: `a < b < c` is rejected with
// a diagnostic that names the fix rather than silently parsing as `(a < b) < c`.
func (p *Parser) parseCmp() ast.Expr {
	start := p.pos
	left := p.parseBitOr()
	op, ok := cmpOps[p.cur().Kind]
	if !ok {
		return left
	}
	opTok := p.advance()
	right := p.parseBitOr()
	b := &ast.Binary{Op: op, L: left, R: right}
	b.Base = p.base(start)

	if next, chained := cmpOps[p.cur().Kind]; chained {
		p.errorAt(opTok.Span.To(p.cur().Span),
			"comparison operators are non-associative").
			Label("`%s` and `%s` cannot be chained", op, next).
			Help("parenthesize: write `(a %s b) %s c`", op, next)
		// Consume the rest so one mistake yields one diagnostic, not a cascade.
		p.advance()
		p.parseBitOr()
	}
	return b
}

// binaryLevel parses one left-associative precedence level.
func (p *Parser) binaryLevel(ops map[lex.Kind]ast.BinaryOp, next func() ast.Expr) ast.Expr {
	start := p.pos
	left := next()
	for {
		op, ok := ops[p.cur().Kind]
		if !ok {
			return left
		}
		p.advance()
		right := next()
		b := &ast.Binary{Op: op, L: left, R: right}
		b.Base = p.base(start)
		left = b
	}
}

var (
	bitOrOps  = map[lex.Kind]ast.BinaryOp{lex.Pipe: ast.BitOr}
	bitXorOps = map[lex.Kind]ast.BinaryOp{lex.Caret: ast.BitXor}
	bitAndOps = map[lex.Kind]ast.BinaryOp{lex.Amp: ast.BitAnd}
	shiftOps  = map[lex.Kind]ast.BinaryOp{lex.Shl: ast.Shl, lex.Shr: ast.Shr}
	addOps    = map[lex.Kind]ast.BinaryOp{lex.Plus: ast.Add, lex.Minus: ast.Sub}
	mulOps    = map[lex.Kind]ast.BinaryOp{lex.Star: ast.Mul, lex.Slash: ast.Div, lex.Percent: ast.Rem}
)

func (p *Parser) parseBitOr() ast.Expr  { return p.binaryLevel(bitOrOps, p.parseBitXor) }
func (p *Parser) parseBitXor() ast.Expr { return p.binaryLevel(bitXorOps, p.parseBitAnd) }
func (p *Parser) parseBitAnd() ast.Expr { return p.binaryLevel(bitAndOps, p.parseShift) }
func (p *Parser) parseShift() ast.Expr  { return p.binaryLevel(shiftOps, p.parseAdd) }
func (p *Parser) parseAdd() ast.Expr    { return p.binaryLevel(addOps, p.parseMul) }
func (p *Parser) parseMul() ast.Expr    { return p.binaryLevel(mulOps, p.parseCast) }

func (p *Parser) parseCast() ast.Expr {
	start := p.pos
	e := p.parseUnary()
	for p.at(lex.KwAs) {
		p.advance()
		ty := p.parseType()
		c := &ast.Cast{X: e, Type: ty}
		c.Base = p.base(start)
		e = c
	}
	return e
}

func (p *Parser) parseUnary() ast.Expr {
	start := p.pos
	switch {
	case p.at(lex.Minus):
		p.advance()
		u := &ast.Unary{Op: ast.Neg, X: p.parseUnary()}
		u.Base = p.base(start)
		return u
	case p.at(lex.Bang):
		p.advance()
		u := &ast.Unary{Op: ast.Not, X: p.parseUnary()}
		u.Base = p.base(start)
		return u
	}
	return p.parsePostfix()
}

func (p *Parser) parsePostfix() ast.Expr {
	start := p.pos
	e := p.parsePrimary()
	for {
		switch {
		case p.at(lex.Dot):
			p.advance()
			name, ok := p.parseIdent()
			if !ok {
				return &ast.ErrorExpr{Base: p.base(start)}
			}
			if p.at(lex.LParen) {
				args := p.parseCallArgs()
				m := &ast.MethodCall{Recv: e, Name: name, Args: args}
				m.Base = p.base(start)
				e = m
			} else {
				f := &ast.FieldAccess{Recv: e, Name: name}
				f.Base = p.base(start)
				e = f
			}
		case p.at(lex.LParen):
			args := p.parseCallArgs()
			c := &ast.Call{Fn: e, Args: args}
			c.Base = p.base(start)
			e = c
		case p.at(lex.Question):
			p.advance()
			tr := &ast.Try{X: e}
			tr.Base = p.base(start)
			e = tr
		default:
			return e
		}
	}
}

func (p *Parser) parseCallArgs() []ast.Expr {
	saved := p.noStruct
	p.noStruct = false
	defer func() { p.noStruct = saved }()

	p.expect(lex.LParen)
	var args []ast.Expr
	for !p.at(lex.RParen) && !p.atEOF() {
		before := p.pos
		args = append(args, p.parseExpr())
		if p.pos == before {
			p.syncList(lex.RParen)
		}
		if !p.eat(lex.Comma) {
			break
		}
	}
	p.expect(lex.RParen)
	return args
}

func (p *Parser) parsePrimary() ast.Expr {
	start := p.pos
	t := p.cur()

	switch t.Kind {
	case lex.Int:
		p.advance()
		e := &ast.IntLit{Value: t.Int, Overflow: t.IntOverflow, Suffix: t.Suffix}
		e.Base = p.base(start)
		return e
	case lex.Float:
		p.advance()
		e := &ast.FloatLit{Value: t.Float, Suffix: t.Suffix}
		e.Base = p.base(start)
		return e
	case lex.Str:
		p.advance()
		if t.Parts != nil {
			return p.interpolated(t, start)
		}
		e := &ast.StrLit{Value: t.Str}
		e.Base = p.base(start)
		return e
	case lex.Char:
		p.advance()
		e := &ast.CharLit{Value: t.Char}
		e.Base = p.base(start)
		return e
	case lex.KwTrue, lex.KwFalse:
		p.advance()
		e := &ast.BoolLit{Value: t.Kind == lex.KwTrue}
		e.Base = p.base(start)
		return e
	case lex.KwSelfValue:
		p.advance()
		e := &ast.SelfExpr{}
		e.Base = p.base(start)
		return e

	case lex.LParen:
		return p.parseParenOrTuple(start)
	case lex.LBrace:
		return p.parseBlock()
	case lex.KwIf:
		return p.parseIf(start)
	case lex.KwMatch:
		return p.parseMatch(start)
	case lex.KwWhile:
		p.advance()
		w := &ast.While{Cond: p.parseExprNoStruct(), Body: p.parseBlock()}
		w.Base = p.base(start)
		return w
	case lex.KwFor:
		p.advance()
		f := &ast.For{Pat: p.parsePattern()}
		p.expect(lex.KwIn)
		f.Iter = p.parseExprNoStruct()
		f.Body = p.parseBlock()
		f.Base = p.base(start)
		return f
	case lex.KwLoop:
		p.advance()
		l := &ast.Loop{Body: p.parseBlock()}
		l.Base = p.base(start)
		return l
	case lex.KwBreak:
		p.advance()
		b := &ast.Break{}
		if p.canStartExpr() {
			b.Value = p.parseExpr()
		}
		b.Base = p.base(start)
		return b
	case lex.KwContinue:
		p.advance()
		c := &ast.Continue{}
		c.Base = p.base(start)
		return c
	case lex.KwReturn:
		p.advance()
		r := &ast.Return{}
		if p.canStartExpr() {
			r.Value = p.parseExpr()
		}
		r.Base = p.base(start)
		return r

	case lex.Pipe, lex.PipePipe:
		return p.parseLambda(start)

	case lex.Ident, lex.KwSelfType:
		return p.parsePathExprOrStructLit(start)
	}

	p.errorExpected("an expression")
	p.advance()
	return &ast.ErrorExpr{Base: p.base(start)}
}

// canStartExpr reports whether the current token could begin an expression, which is how
// `break`/`return` tell a value apart from the end of the statement.
func (p *Parser) canStartExpr() bool {
	switch p.cur().Kind {
	case lex.Semi, lex.RBrace, lex.RParen, lex.RBracket, lex.Comma, lex.EOF:
		return false
	}
	return true
}

func (p *Parser) parseParenOrTuple(start int) ast.Expr {
	saved := p.noStruct
	p.noStruct = false
	defer func() { p.noStruct = saved }()

	p.advance() // (
	if p.at(lex.RParen) {
		p.advance()
		e := &ast.TupleExpr{} // the unit value
		e.Base = p.base(start)
		return e
	}
	first := p.parseExpr()
	if !p.at(lex.Comma) {
		p.expect(lex.RParen)
		return first // parenthesized, not a one-tuple
	}
	tup := &ast.TupleExpr{Elems: []ast.Expr{first}}
	for p.eat(lex.Comma) {
		if p.at(lex.RParen) {
			break
		}
		tup.Elems = append(tup.Elems, p.parseExpr())
	}
	p.expect(lex.RParen)
	tup.Base = p.base(start)
	return tup
}

func (p *Parser) parseIf(start int) ast.Expr {
	p.advance() // if
	e := &ast.If{Cond: p.parseExprNoStruct(), Then: p.parseBlock()}
	if p.eat(lex.KwElse) {
		if p.at(lex.KwIf) {
			e.Else = p.parseIf(p.pos)
		} else {
			e.Else = p.parseBlock()
		}
	}
	e.Base = p.base(start)
	return e
}

func (p *Parser) parseMatch(start int) ast.Expr {
	p.advance() // match
	m := &ast.Match{Scrutinee: p.parseExprNoStruct()}
	if _, ok := p.expect(lex.LBrace); !ok {
		m.Base = p.base(start)
		return m
	}
	saved := p.noStruct
	p.noStruct = false
	for !p.at(lex.RBrace) && !p.atEOF() {
		before := p.pos
		astart := p.pos
		arm := &ast.MatchArm{Pat: p.parsePattern()}
		if p.eat(lex.KwIf) {
			arm.Guard = p.parseExpr()
		}
		if _, ok := p.expect(lex.FatArrow); !ok {
			p.syncStmt()
			if p.pos == before {
				p.advance()
			}
			continue
		}
		arm.Body = p.parseExpr()
		arm.Base = p.base(astart)
		m.Arms = append(m.Arms, arm)

		// A brace-bodied arm may omit its comma; any other arm requires one unless it
		// is the last.
		if !p.eat(lex.Comma) && !isBlockExpr(arm.Body) && !p.at(lex.RBrace) {
			p.errorExpected("`,` after this match arm")
			p.syncStmt()
		}
		if p.pos == before {
			p.advance()
		}
	}
	p.noStruct = saved
	p.expect(lex.RBrace)
	m.Base = p.base(start)
	return m
}

func (p *Parser) parseLambda(start int) ast.Expr {
	saved := p.noStruct
	p.noStruct = false
	defer func() { p.noStruct = saved }()

	l := &ast.Lambda{}
	if p.at(lex.PipePipe) {
		p.advance() // `||` is an empty parameter list, not an operator, in this position
	} else {
		p.advance() // |
		for !p.at(lex.Pipe) && !p.atEOF() {
			pstart := p.pos
			// Not parsePattern: an or-pattern would swallow the closing `|`.
			lp := &ast.LambdaParam{Pat: p.parsePatternNoOr()}
			if p.eat(lex.Colon) {
				lp.Type = p.parseType()
			}
			lp.Base = p.base(pstart)
			l.Params = append(l.Params, lp)
			if !p.eat(lex.Comma) {
				break
			}
		}
		p.expect(lex.Pipe)
	}
	if p.eat(lex.Arrow) {
		l.Ret = p.parseType()
		l.Body = p.parseBlock()
	} else {
		l.Body = p.parseExpr()
	}
	l.Base = p.base(start)
	return l
}

func (p *Parser) parsePathExprOrStructLit(start int) ast.Expr {
	path := p.parsePath()
	args := p.parseTypeArgs()

	if p.at(lex.LBrace) && !p.noStruct {
		return p.parseStructLit(start, path, args)
	}
	e := &ast.PathExpr{Path: path, Args: args}
	e.Base = p.base(start)
	return e
}

func (p *Parser) parseStructLit(start int, path *ast.Path, args []ast.Type) ast.Expr {
	saved := p.noStruct
	p.noStruct = false
	defer func() { p.noStruct = saved }()

	p.advance() // {
	lit := &ast.StructLit{Path: path, Args: args}
	for !p.at(lex.RBrace) && !p.atEOF() {
		fstart := p.pos
		name, ok := p.parseIdent()
		if !ok {
			p.syncList(lex.RBrace)
			if !p.eat(lex.Comma) {
				break
			}
			continue
		}
		fi := &ast.FieldInit{Name: name}
		if p.eat(lex.Colon) {
			fi.Value = p.parseExpr()
		} else {
			// Shorthand `Point { x, y }` means `x: x, y: y`.
			pe := &ast.PathExpr{Path: &ast.Path{Base: ast.Base{ID: p.newID(), Loc: name.Loc}, Segments: []ast.Ident{name}}}
			pe.Base = ast.Base{ID: p.newID(), Loc: name.Loc}
			fi.Value = pe
		}
		fi.Base = p.base(fstart)
		lit.Fields = append(lit.Fields, fi)
		if !p.eat(lex.Comma) {
			break
		}
	}
	p.expect(lex.RBrace)
	lit.Base = p.base(start)
	return lit
}

// ---------------------------------------------------------------------------
// Patterns
// ---------------------------------------------------------------------------

func (p *Parser) parsePattern() ast.Pattern {
	start := p.pos
	first := p.parsePatternNoOr()
	if !p.at(lex.Pipe) {
		return first
	}
	or := &ast.OrPat{Alts: []ast.Pattern{first}}
	for p.eat(lex.Pipe) {
		or.Alts = append(or.Alts, p.parsePatternNoOr())
	}
	or.Base = p.base(start)
	return or
}

func (p *Parser) parsePatternNoOr() ast.Pattern {
	start := p.pos
	t := p.cur()

	switch t.Kind {
	case lex.Underscore:
		p.advance()
		w := &ast.WildcardPat{}
		w.Base = p.base(start)
		return w

	case lex.Int, lex.Float, lex.Str, lex.Char, lex.KwTrue, lex.KwFalse:
		lit := p.parseLiteralForPattern()
		lp := &ast.LitPat{Lit: lit}
		lp.Base = p.base(start)
		return lp

	case lex.Minus:
		p.advance()
		if !p.at(lex.Int) && !p.at(lex.Float) {
			p.errorExpected("a numeric literal after `-` in a pattern")
			p.advance()
			return &ast.ErrorPat{Base: p.base(start)}
		}
		lit := p.parseLiteralForPattern()
		lp := &ast.LitPat{Neg: true, Lit: lit}
		lp.Base = p.base(start)
		return lp

	case lex.LParen:
		return p.parseTuplePattern(start)

	case lex.KwMut:
		p.advance()
		name, ok := p.parseIdent()
		if !ok {
			return &ast.ErrorPat{Base: p.base(start)}
		}
		b := &ast.BindPat{Mut: true, Name: name}
		if p.eat(lex.At) {
			b.Sub = p.parsePatternNoOr()
		}
		b.Base = p.base(start)
		return b

	case lex.Ident, lex.KwSelfType:
		path := p.parsePath()
		switch {
		case p.at(lex.LParen):
			return p.parseTupleVariantPattern(start, path)
		case p.at(lex.LBrace):
			return p.parseStructPattern(start, path)
		case len(path.Segments) == 1:
			// A bare name: a binding, unless it resolves to a unit variant or a
			// constant. That decision belongs to name resolution, not to the parser
			// (spec/02-grammar.md).
			b := &ast.BindPat{Name: path.Segments[0]}
			if p.eat(lex.At) {
				b.Sub = p.parsePatternNoOr()
			}
			b.Base = p.base(start)
			return b
		default:
			pp := &ast.PathPat{Path: path, Kind: ast.UnitVariant}
			pp.Base = p.base(start)
			return pp
		}
	}

	p.errorExpected("a pattern")
	p.advance()
	return &ast.ErrorPat{Base: p.base(start)}
}

// parseLiteralForPattern reuses the expression literal parsers; patterns admit only
// literals, never arbitrary expressions.
func (p *Parser) parseLiteralForPattern() ast.Expr {
	start := p.pos
	t := p.advance()
	switch t.Kind {
	case lex.Int:
		e := &ast.IntLit{Value: t.Int, Overflow: t.IntOverflow, Suffix: t.Suffix}
		e.Base = p.base(start)
		return e
	case lex.Float:
		e := &ast.FloatLit{Value: t.Float, Suffix: t.Suffix}
		e.Base = p.base(start)
		return e
	case lex.Str:
		e := &ast.StrLit{Value: t.Str}
		e.Base = p.base(start)
		return e
	case lex.Char:
		e := &ast.CharLit{Value: t.Char}
		e.Base = p.base(start)
		return e
	default:
		e := &ast.BoolLit{Value: t.Kind == lex.KwTrue}
		e.Base = p.base(start)
		return e
	}
}

func (p *Parser) parseTuplePattern(start int) ast.Pattern {
	p.advance() // (
	if p.at(lex.RParen) {
		p.advance()
		tp := &ast.TuplePat{}
		tp.Base = p.base(start)
		return tp
	}
	first := p.parsePattern()
	if !p.at(lex.Comma) {
		p.expect(lex.RParen)
		return first // parenthesized, not a one-tuple
	}
	tp := &ast.TuplePat{Elems: []ast.Pattern{first}}
	for p.eat(lex.Comma) {
		if p.at(lex.RParen) {
			break
		}
		tp.Elems = append(tp.Elems, p.parsePattern())
	}
	p.expect(lex.RParen)
	tp.Base = p.base(start)
	return tp
}

func (p *Parser) parseTupleVariantPattern(start int, path *ast.Path) ast.Pattern {
	p.advance() // (
	pp := &ast.PathPat{Path: path, Kind: ast.TupleVariant}
	for !p.at(lex.RParen) && !p.atEOF() {
		before := p.pos
		pp.Elems = append(pp.Elems, p.parsePattern())
		if p.pos == before {
			p.syncList(lex.RParen)
		}
		if !p.eat(lex.Comma) {
			break
		}
	}
	p.expect(lex.RParen)
	pp.Base = p.base(start)
	return pp
}

func (p *Parser) parseStructPattern(start int, path *ast.Path) ast.Pattern {
	p.advance() // {
	pp := &ast.PathPat{Path: path, Kind: ast.StructVariant}
	for !p.at(lex.RBrace) && !p.atEOF() {
		if p.at(lex.DotDot) {
			p.advance()
			pp.Rest = true
			break
		}
		fstart := p.pos
		name, ok := p.parseIdent()
		if !ok {
			p.syncList(lex.RBrace)
			if !p.eat(lex.Comma) {
				break
			}
			continue
		}
		fp := &ast.FieldPat{Name: name}
		if p.eat(lex.Colon) {
			fp.Pat = p.parsePattern()
		} else {
			b := &ast.BindPat{Name: name}
			b.Base = ast.Base{ID: p.newID(), Loc: name.Loc}
			fp.Pat = b
		}
		fp.Base = p.base(fstart)
		pp.Fields = append(pp.Fields, fp)
		if !p.eat(lex.Comma) {
			break
		}
	}
	p.expect(lex.RBrace)
	pp.Base = p.base(start)
	return pp
}
