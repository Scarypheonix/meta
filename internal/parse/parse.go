// Package parse turns tokens into an AST, per docs/spec/02-grammar.md.
//
// The parser reports multiple syntax errors in one pass. It never stops at the first
// problem: it records a diagnostic, resynchronizes using the strategy the grammar
// specifies, substitutes an Error node, and continues. An Error node in the tree means
// a diagnostic was already reported, so later passes must stay silent about it
// (spec/09-errors.md rule 4).
package parse

import (
	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/lex"
	"github.com/scarypheonix/meta/internal/source"
)

// syntaxCode is the diagnostic code for every syntax error (docs/spec/codes.md).
const syntaxCode = "E0002"

// Parser holds the state of one file's parse.
type Parser struct {
	file *source.File
	toks []lex.Token
	pos  int
	bag  *diag.Bag

	nextID ast.NodeID

	// noStruct suppresses struct literals at the top level of an expression, which is
	// what makes `if x { }` unambiguous (spec/02-grammar.md, parser restriction 1).
	noStruct bool

	// recovering suppresses a cascade of syntax errors at the same position: after one
	// error, the parser stays quiet until it consumes a token.
	recovering bool
}

// File lexes and parses one source file. It always returns a File, however broken the
// input; check bag.HasErrors to know whether the tree is trustworthy.
func File(f *source.File, bag *diag.Bag) *ast.File {
	p := &Parser{file: f, toks: lex.Tokens(f, bag), bag: bag, nextID: 1}
	return p.parseFile()
}

// ---------------------------------------------------------------------------
// Token helpers
// ---------------------------------------------------------------------------

func (p *Parser) newID() ast.NodeID {
	id := p.nextID
	p.nextID++
	return id
}

// base builds a node identity spanning from token index start through the last token
// consumed.
func (p *Parser) base(start int) ast.Base {
	end := p.pos - 1
	if end < start {
		end = start
	}
	if end >= len(p.toks) {
		end = len(p.toks) - 1
	}
	return ast.Base{ID: p.newID(), Loc: p.toks[start].Span.To(p.toks[end].Span)}
}

func (p *Parser) cur() lex.Token { return p.toks[p.pos] }

func (p *Parser) at(k lex.Kind) bool { return p.toks[p.pos].Kind == k }

func (p *Parser) atEOF() bool { return p.toks[p.pos].Kind == lex.EOF }

// peekIs reports whether the token n ahead has kind k.
func (p *Parser) peekIs(n int, k lex.Kind) bool {
	i := p.pos + n
	if i >= len(p.toks) {
		return k == lex.EOF
	}
	return p.toks[i].Kind == k
}

// advance consumes and returns the current token.
func (p *Parser) advance() lex.Token {
	t := p.toks[p.pos]
	if p.pos < len(p.toks)-1 {
		p.pos++
	}
	p.recovering = false
	return t
}

// eat consumes the current token if it has kind k.
func (p *Parser) eat(k lex.Kind) bool {
	if p.at(k) {
		p.advance()
		return true
	}
	return false
}

// expect consumes a token of kind k, or reports and returns false without consuming.
func (p *Parser) expect(k lex.Kind) (lex.Token, bool) {
	if p.at(k) {
		return p.advance(), true
	}
	p.errorExpected(k.String())
	return p.cur(), false
}

// errorExpected reports "expected X, found Y" at the current token.
func (p *Parser) errorExpected(what string) {
	if p.recovering {
		return // one diagnostic per position; the rest would be noise
	}
	p.recovering = true
	found := p.cur().String()
	p.bag.Errorf(syntaxCode, p.cur().Span, "expected %s, found %s", what, found).
		Label("expected %s here", what)
}

// errorAt reports a syntax error at an explicit span.
func (p *Parser) errorAt(span diag.Span, format string, args ...any) *diag.Builder {
	p.recovering = true
	return p.bag.Errorf(syntaxCode, span, format, args...)
}

// ---------------------------------------------------------------------------
// Recovery
// ---------------------------------------------------------------------------

// itemStart is the set of tokens that can begin an item; syncItem stops at one.
func isItemStart(k lex.Kind) bool {
	switch k {
	case lex.KwPub, lex.KwFn, lex.KwStruct, lex.KwEnum, lex.KwTrait,
		lex.KwImpl, lex.KwType, lex.KwConst, lex.KwUse:
		return true
	}
	return false
}

// syncItem skips to the next item boundary at brace depth zero.
func (p *Parser) syncItem() {
	depth := 0
	for !p.atEOF() {
		switch p.cur().Kind {
		case lex.LBrace:
			depth++
		case lex.RBrace:
			if depth == 0 {
				return
			}
			depth--
		default:
			if depth == 0 && isItemStart(p.cur().Kind) {
				return
			}
		}
		p.advance()
	}
}

// syncStmt skips to the end of the current statement: a `;` (consumed) or a `}` (left
// in place for the block to close on).
func (p *Parser) syncStmt() {
	depth := 0
	for !p.atEOF() {
		switch p.cur().Kind {
		case lex.LBrace, lex.LParen, lex.LBracket:
			depth++
		case lex.RParen, lex.RBracket:
			if depth > 0 {
				depth--
			}
		case lex.RBrace:
			if depth == 0 {
				return
			}
			depth--
		case lex.Semi:
			if depth == 0 {
				p.advance()
				return
			}
		default:
			if depth == 0 && isItemStart(p.cur().Kind) {
				return
			}
		}
		p.advance()
	}
}

// syncList skips to the next `,` (left in place) or the closing delimiter, never past
// it, so a malformed element does not swallow the rest of the construct.
func (p *Parser) syncList(closer lex.Kind) {
	depth := 0
	for !p.atEOF() {
		k := p.cur().Kind
		if depth == 0 && (k == lex.Comma || k == closer) {
			return
		}
		switch k {
		case lex.LParen, lex.LBracket, lex.LBrace:
			depth++
		case lex.RParen, lex.RBracket, lex.RBrace:
			if depth == 0 {
				return // an unexpected closer: let the caller deal with it
			}
			depth--
		}
		p.advance()
	}
}

// ---------------------------------------------------------------------------
// File and items
// ---------------------------------------------------------------------------

func (p *Parser) parseFile() *ast.File {
	start := p.pos
	f := &ast.File{}

	for p.at(lex.KwUse) {
		if u := p.parseUse(); u != nil {
			f.Uses = append(f.Uses, u)
		}
	}
	for !p.atEOF() {
		if p.at(lex.KwUse) {
			u := p.parseUse()
			if u != nil {
				p.errorAt(u.Span(), "`use` declarations must come before all items").
					Label("move this to the top of the file").
					Note("spec/07-modules.md: all imports precede all items")
				f.Uses = append(f.Uses, u)
			}
			continue
		}
		before := p.pos
		f.Items = append(f.Items, p.parseItem())
		if p.pos == before {
			// No progress: force one token forward so the parser always terminates.
			p.advance()
		}
	}
	f.Base = p.base(start)
	return f
}

func (p *Parser) parseUse() *ast.Use {
	start := p.pos
	p.advance() // use
	path := p.parsePath()
	u := &ast.Use{Path: path}
	if p.at(lex.ColonColon) && p.peekIs(1, lex.LBrace) {
		p.advance() // ::
		p.advance() // {
		for !p.at(lex.RBrace) && !p.atEOF() {
			if name, ok := p.parseIdent(); ok {
				u.Names = append(u.Names, name)
			} else {
				p.syncList(lex.RBrace)
			}
			if !p.eat(lex.Comma) {
				break
			}
		}
		p.expect(lex.RBrace)
	}
	p.expect(lex.Semi)
	u.Base = p.base(start)
	return u
}

func (p *Parser) parseIdent() (ast.Ident, bool) {
	if p.at(lex.Ident) {
		t := p.advance()
		return ast.Ident{Name: t.Text, Loc: t.Span}, true
	}
	p.errorExpected("an identifier")
	return ast.Ident{Name: "", Loc: p.cur().Span}, false
}

func (p *Parser) parsePath() *ast.Path {
	start := p.pos
	path := &ast.Path{}
	for {
		switch {
		case p.at(lex.Ident):
			t := p.advance()
			path.Segments = append(path.Segments, ast.Ident{Name: t.Text, Loc: t.Span})
		case p.at(lex.KwSelfType):
			t := p.advance()
			path.Segments = append(path.Segments, ast.Ident{Name: "Self", Loc: t.Span})
		default:
			if len(path.Segments) == 0 {
				p.errorExpected("a path")
				path.Base = p.base(start)
				return path
			}
		}
		if !(p.at(lex.ColonColon) && !p.peekIs(1, lex.LBrace)) {
			break
		}
		p.advance() // ::
	}
	path.Base = p.base(start)
	return path
}

func (p *Parser) parseItem() ast.Item {
	start := p.pos
	pub := p.eat(lex.KwPub)

	switch p.cur().Kind {
	case lex.KwFn:
		return p.parseFn(start, pub)
	case lex.KwStruct:
		return p.parseStruct(start, pub)
	case lex.KwEnum:
		return p.parseEnum(start, pub)
	case lex.KwTrait:
		return p.parseTrait(start, pub)
	case lex.KwImpl:
		if pub {
			p.errorAt(p.toks[start].Span, "`impl` blocks cannot be `pub`").
				Label("remove `pub`").
				Note("an impl is as visible as the trait and type it connects")
		}
		return p.parseImpl(start)
	case lex.KwType:
		return p.parseTypeAlias(start, pub)
	case lex.KwConst:
		return p.parseConst(start, pub)
	}

	p.errorExpected("an item (`fn`, `struct`, `enum`, `trait`, `impl`, `type` or `const`)")
	p.syncItem()
	return &ast.ErrorItem{Base: p.base(start)}
}

func (p *Parser) parseGenerics() []*ast.GenericParam {
	if !p.at(lex.LBracket) {
		return nil
	}
	p.advance() // [
	var out []*ast.GenericParam
	for !p.at(lex.RBracket) && !p.atEOF() {
		start := p.pos
		name, ok := p.parseIdent()
		if !ok {
			p.syncList(lex.RBracket)
			if !p.eat(lex.Comma) {
				break
			}
			continue
		}
		g := &ast.GenericParam{Name: name}
		if p.eat(lex.Colon) {
			g.Bounds = p.parseTraitBounds()
		}
		g.Base = p.base(start)
		out = append(out, g)
		if !p.eat(lex.Comma) {
			break
		}
	}
	p.expect(lex.RBracket)
	return out
}

func (p *Parser) parseTraitBounds() []*ast.TraitRef {
	var out []*ast.TraitRef
	for {
		out = append(out, p.parseTraitRef())
		if !p.eat(lex.Plus) {
			return out
		}
	}
}

func (p *Parser) parseTraitRef() *ast.TraitRef {
	start := p.pos
	tr := &ast.TraitRef{Path: p.parsePath()}
	tr.Args = p.parseTypeArgs()
	tr.Base = p.base(start)
	return tr
}

// parseTypeArgs reads a bracketed type argument list, or returns nil if absent.
func (p *Parser) parseTypeArgs() []ast.Type {
	if !p.at(lex.LBracket) {
		return nil
	}
	p.advance() // [
	var out []ast.Type
	for !p.at(lex.RBracket) && !p.atEOF() {
		out = append(out, p.parseType())
		if !p.eat(lex.Comma) {
			break
		}
	}
	p.expect(lex.RBracket)
	return out
}

func (p *Parser) parseWhere() []*ast.WherePred {
	if !p.eat(lex.KwWhere) {
		return nil
	}
	var out []*ast.WherePred
	for {
		start := p.pos
		w := &ast.WherePred{Type: p.parseType()}
		if _, ok := p.expect(lex.Colon); ok {
			w.Bounds = p.parseTraitBounds()
		}
		w.Base = p.base(start)
		out = append(out, w)
		if !p.eat(lex.Comma) {
			return out
		}
		// A trailing comma before the body is allowed.
		if p.at(lex.LBrace) || p.at(lex.Semi) {
			return out
		}
	}
}

func (p *Parser) parseFn(start int, pub bool) *ast.FnDecl {
	p.advance() // fn
	fn := &ast.FnDecl{Pub: pub}
	fn.Name, _ = p.parseIdent()
	fn.Generics = p.parseGenerics()

	if _, ok := p.expect(lex.LParen); ok {
		p.parseParamList(fn)
	}
	if p.eat(lex.Arrow) {
		fn.Ret = p.parseType()
	}
	fn.Where = p.parseWhere()

	if p.eat(lex.Semi) {
		fn.Base = p.base(start) // a trait's required method: signature only
		return fn
	}
	if p.at(lex.LBrace) {
		fn.Body = p.parseBlock()
	} else {
		p.errorExpected("a function body `{ ... }` or `;`")
		p.syncItem()
	}
	fn.Base = p.base(start)
	return fn
}

func (p *Parser) parseParamList(fn *ast.FnDecl) {
	first := true
	for !p.at(lex.RParen) && !p.atEOF() {
		start := p.pos
		if first && (p.at(lex.KwSelfValue) || (p.at(lex.KwMut) && p.peekIs(1, lex.KwSelfValue))) {
			mut := p.eat(lex.KwMut)
			p.advance() // self
			fn.Self = &ast.SelfParam{Mut: mut}
			fn.Self.Base = p.base(start)
		} else {
			mut := p.eat(lex.KwMut)
			pat := p.parsePattern()
			var ty ast.Type
			if _, ok := p.expect(lex.Colon); ok {
				ty = p.parseType()
			} else {
				ty = &ast.ErrorType{Base: p.base(start)}
				p.syncList(lex.RParen)
			}
			param := &ast.Param{Mut: mut, Pat: pat, Type: ty}
			param.Base = p.base(start)
			fn.Params = append(fn.Params, param)
		}
		first = false
		if !p.eat(lex.Comma) {
			break
		}
	}
	p.expect(lex.RParen)
}

func (p *Parser) parseStruct(start int, pub bool) *ast.StructDecl {
	p.advance() // struct
	s := &ast.StructDecl{Pub: pub}
	s.Name, _ = p.parseIdent()
	s.Generics = p.parseGenerics()
	s.Where = p.parseWhere()
	if _, ok := p.expect(lex.LBrace); ok {
		s.Fields = p.parseFieldList()
		p.expect(lex.RBrace)
	} else {
		p.syncItem()
	}
	s.Base = p.base(start)
	return s
}

func (p *Parser) parseFieldList() []*ast.Field {
	var out []*ast.Field
	for !p.at(lex.RBrace) && !p.atEOF() {
		start := p.pos
		pub := p.eat(lex.KwPub)
		mut := p.eat(lex.KwMut)
		name, ok := p.parseIdent()
		if !ok {
			p.syncList(lex.RBrace)
			if !p.eat(lex.Comma) {
				break
			}
			continue
		}
		f := &ast.Field{Pub: pub, Mut: mut, Name: name}
		if _, ok := p.expect(lex.Colon); ok {
			f.Type = p.parseType()
		} else {
			f.Type = &ast.ErrorType{Base: p.base(start)}
			p.syncList(lex.RBrace)
		}
		f.Base = p.base(start)
		out = append(out, f)
		if !p.eat(lex.Comma) {
			break
		}
	}
	return out
}

func (p *Parser) parseEnum(start int, pub bool) *ast.EnumDecl {
	p.advance() // enum
	e := &ast.EnumDecl{Pub: pub}
	e.Name, _ = p.parseIdent()
	e.Generics = p.parseGenerics()
	e.Where = p.parseWhere()
	if _, ok := p.expect(lex.LBrace); !ok {
		p.syncItem()
		e.Base = p.base(start)
		return e
	}
	for !p.at(lex.RBrace) && !p.atEOF() {
		vstart := p.pos
		name, ok := p.parseIdent()
		if !ok {
			p.syncList(lex.RBrace)
			if !p.eat(lex.Comma) {
				break
			}
			continue
		}
		v := &ast.Variant{Name: name, Kind: ast.UnitVariant}
		switch {
		case p.eat(lex.LParen):
			v.Kind = ast.TupleVariant
			for !p.at(lex.RParen) && !p.atEOF() {
				v.Types = append(v.Types, p.parseType())
				if !p.eat(lex.Comma) {
					break
				}
			}
			p.expect(lex.RParen)
		case p.eat(lex.LBrace):
			v.Kind = ast.StructVariant
			v.Fields = p.parseFieldList()
			p.expect(lex.RBrace)
		}
		v.Base = p.base(vstart)
		e.Variants = append(e.Variants, v)
		if !p.eat(lex.Comma) {
			break
		}
	}
	p.expect(lex.RBrace)
	e.Base = p.base(start)
	return e
}

func (p *Parser) parseTrait(start int, pub bool) *ast.TraitDecl {
	p.advance() // trait
	tr := &ast.TraitDecl{Pub: pub}
	tr.Name, _ = p.parseIdent()
	tr.Generics = p.parseGenerics()
	if p.eat(lex.Colon) {
		tr.Supertraits = p.parseTraitBounds()
	}
	tr.Where = p.parseWhere()
	if _, ok := p.expect(lex.LBrace); !ok {
		p.syncItem()
		tr.Base = p.base(start)
		return tr
	}
	for !p.at(lex.RBrace) && !p.atEOF() {
		before := p.pos
		switch {
		case p.at(lex.KwType):
			mstart := p.pos
			p.advance()
			at := &ast.AssocTypeDecl{}
			at.Name, _ = p.parseIdent()
			if p.eat(lex.Colon) {
				at.Bounds = p.parseTraitBounds()
			}
			p.expect(lex.Semi)
			at.Base = p.base(mstart)
			tr.AssocTypes = append(tr.AssocTypes, at)
		case p.at(lex.KwFn):
			tr.Methods = append(tr.Methods, p.parseFn(p.pos, false))
		default:
			p.errorExpected("`fn` or `type` in a trait body")
			p.syncStmt()
		}
		if p.pos == before {
			p.advance()
		}
	}
	p.expect(lex.RBrace)
	tr.Base = p.base(start)
	return tr
}

func (p *Parser) parseImpl(start int) *ast.ImplDecl {
	p.advance() // impl
	im := &ast.ImplDecl{}
	im.Generics = p.parseGenerics()

	// `impl Trait for Type` and `impl Type` are distinguished by the `for` that follows
	// the first type, so parse a type and reinterpret it if `for` appears.
	first := p.parseType()
	if p.eat(lex.KwFor) {
		im.Trait = typeAsTraitRef(p, first)
		im.Type = p.parseType()
	} else {
		im.Type = first
	}
	im.Where = p.parseWhere()

	if _, ok := p.expect(lex.LBrace); !ok {
		p.syncItem()
		im.Base = p.base(start)
		return im
	}
	for !p.at(lex.RBrace) && !p.atEOF() {
		before := p.pos
		mstart := p.pos
		pub := p.eat(lex.KwPub)
		switch {
		case p.at(lex.KwType):
			p.advance()
			at := &ast.AssocTypeDef{}
			at.Name, _ = p.parseIdent()
			if _, ok := p.expect(lex.Assign); ok {
				at.Type = p.parseType()
			} else {
				at.Type = &ast.ErrorType{Base: p.base(mstart)}
			}
			p.expect(lex.Semi)
			at.Base = p.base(mstart)
			im.AssocTypes = append(im.AssocTypes, at)
		case p.at(lex.KwFn):
			im.Methods = append(im.Methods, p.parseFn(mstart, pub))
		default:
			p.errorExpected("`fn` or `type` in an impl body")
			p.syncStmt()
		}
		if p.pos == before {
			p.advance()
		}
	}
	p.expect(lex.RBrace)
	im.Base = p.base(start)
	return im
}

// typeAsTraitRef reinterprets a parsed type as a trait reference, which is valid only
// for a path type.
func typeAsTraitRef(p *Parser, t ast.Type) *ast.TraitRef {
	if pt, ok := t.(*ast.PathType); ok {
		return &ast.TraitRef{Base: ast.Base{ID: p.newID(), Loc: pt.Span()}, Path: pt.Path, Args: pt.Args}
	}
	p.errorAt(t.Span(), "expected a trait name before `for`").
		Label("only a named trait can appear here")
	return &ast.TraitRef{Base: ast.Base{ID: p.newID(), Loc: t.Span()}, Path: &ast.Path{}}
}

func (p *Parser) parseTypeAlias(start int, pub bool) *ast.TypeAliasDecl {
	p.advance() // type
	a := &ast.TypeAliasDecl{Pub: pub}
	a.Name, _ = p.parseIdent()
	a.Generics = p.parseGenerics()
	if _, ok := p.expect(lex.Assign); ok {
		a.Type = p.parseType()
	} else {
		a.Type = &ast.ErrorType{Base: p.base(start)}
		p.syncItem()
	}
	p.expect(lex.Semi)
	a.Base = p.base(start)
	return a
}

func (p *Parser) parseConst(start int, pub bool) *ast.ConstDecl {
	p.advance() // const
	c := &ast.ConstDecl{Pub: pub}
	c.Name, _ = p.parseIdent()
	if _, ok := p.expect(lex.Colon); ok {
		c.Type = p.parseType()
	} else {
		c.Type = &ast.ErrorType{Base: p.base(start)}
	}
	if _, ok := p.expect(lex.Assign); ok {
		c.Value = p.parseExpr()
	} else {
		c.Value = &ast.ErrorExpr{Base: p.base(start)}
		p.syncStmt()
	}
	p.expect(lex.Semi)
	c.Base = p.base(start)
	return c
}

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

func (p *Parser) parseType() ast.Type {
	start := p.pos
	switch {
	case p.at(lex.KwSelfType):
		// Bare `Self` is the self type; `Self::Item` is an associated-type projection and
		// therefore an ordinary path.
		if !p.peekIs(1, lex.ColonColon) {
			p.advance()
			return &ast.SelfType{Base: p.base(start)}
		}
		pt := &ast.PathType{Path: p.parsePath()}
		pt.Args = p.parseTypeArgs()
		pt.Base = p.base(start)
		return pt

	case p.at(lex.KwFn):
		p.advance()
		ft := &ast.FnType{}
		if _, ok := p.expect(lex.LParen); ok {
			for !p.at(lex.RParen) && !p.atEOF() {
				ft.Params = append(ft.Params, p.parseType())
				if !p.eat(lex.Comma) {
					break
				}
			}
			p.expect(lex.RParen)
		}
		if _, ok := p.expect(lex.Arrow); ok {
			ft.Ret = p.parseType()
		} else {
			ft.Ret = &ast.ErrorType{Base: p.base(start)}
		}
		ft.Base = p.base(start)
		return ft

	case p.at(lex.LParen):
		p.advance()
		if p.eat(lex.RParen) {
			return &ast.UnitType{Base: p.base(start)}
		}
		first := p.parseType()
		if !p.at(lex.Comma) {
			p.expect(lex.RParen)
			return first // parenthesized, not a one-tuple
		}
		tt := &ast.TupleType{Elems: []ast.Type{first}}
		for p.eat(lex.Comma) {
			if p.at(lex.RParen) {
				break
			}
			tt.Elems = append(tt.Elems, p.parseType())
		}
		p.expect(lex.RParen)
		tt.Base = p.base(start)
		return tt

	case p.at(lex.Ident):
		pt := &ast.PathType{Path: p.parsePath()}
		pt.Args = p.parseTypeArgs()
		pt.Base = p.base(start)
		return pt
	}

	p.errorExpected("a type")
	p.advance()
	return &ast.ErrorType{Base: p.base(start)}
}
