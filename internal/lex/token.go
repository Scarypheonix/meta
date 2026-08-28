// Package lex turns Origin source text into tokens, per docs/spec/01-lexical.md.
package lex

import "fmt"

// Kind identifies a token's lexical category.
type Kind int

// Token kinds. The ordering groups literals, keywords and punctuation so that range
// checks like IsKeyword are a comparison rather than a map lookup.
const (
	EOF Kind = iota
	Ident
	Int
	Float
	Str
	Char

	keywordStart
	KwAs
	KwBreak
	KwConst
	KwContinue
	KwElse
	KwEnum
	KwFalse
	KwFn
	KwFor
	KwIf
	KwImpl
	KwIn
	KwLet
	KwLoop
	KwMatch
	KwMut
	KwPub
	KwReturn
	KwSelfValue // self
	KwSelfType  // Self
	KwStruct
	KwTrait
	KwTrue
	KwType
	KwUse
	KwWhere
	KwWhile
	keywordEnd

	LParen
	RParen
	LBrace
	RBrace
	LBracket
	RBracket
	Comma
	Semi
	Colon
	ColonColon
	Dot
	DotDot
	Arrow    // ->
	FatArrow // =>
	Underscore
	At

	Plus
	Minus
	Star
	Slash
	Percent
	Amp
	Pipe
	Caret
	Bang
	Shl
	Shr

	Assign // =
	PlusEq
	MinusEq
	StarEq
	SlashEq
	PercentEq

	EqEq
	BangEq
	Lt
	Le
	Gt
	Ge

	AmpAmp
	PipePipe
	Question
)

var kindNames = map[Kind]string{
	EOF: "end of file", Ident: "identifier", Int: "integer literal",
	Float: "float literal", Str: "string literal", Char: "character literal",

	KwAs: "as", KwBreak: "break", KwConst: "const", KwContinue: "continue",
	KwElse: "else", KwEnum: "enum", KwFalse: "false", KwFn: "fn", KwFor: "for",
	KwIf: "if", KwImpl: "impl", KwIn: "in", KwLet: "let", KwLoop: "loop",
	KwMatch: "match", KwMut: "mut", KwPub: "pub", KwReturn: "return",
	KwSelfValue: "self", KwSelfType: "Self", KwStruct: "struct", KwTrait: "trait",
	KwTrue: "true", KwType: "type", KwUse: "use", KwWhere: "where", KwWhile: "while",

	LParen: "(", RParen: ")", LBrace: "{", RBrace: "}", LBracket: "[", RBracket: "]",
	Comma: ",", Semi: ";", Colon: ":", ColonColon: "::", Dot: ".", DotDot: "..",
	Arrow: "->", FatArrow: "=>", Underscore: "_", At: "@",

	Plus: "+", Minus: "-", Star: "*", Slash: "/", Percent: "%", Amp: "&",
	Pipe: "|", Caret: "^", Bang: "!", Shl: "<<", Shr: ">>",

	Assign: "=", PlusEq: "+=", MinusEq: "-=", StarEq: "*=", SlashEq: "/=", PercentEq: "%=",

	EqEq: "==", BangEq: "!=", Lt: "<", Le: "<=", Gt: ">", Ge: ">=",
	AmpAmp: "&&", PipePipe: "||", Question: "?",
}

// String returns the token kind as it would be written in source, or a description for
// kinds that have no fixed spelling. Diagnostics quote this, so it must read naturally
// in "expected `X`, found `Y`".
func (k Kind) String() string {
	if n, ok := kindNames[k]; ok {
		return n
	}
	return fmt.Sprintf("token(%d)", int(k))
}

// IsKeyword reports whether k is a reserved word.
func (k Kind) IsKeyword() bool { return k > keywordStart && k < keywordEnd }

// keywords maps source text to its keyword kind.
var keywords = map[string]Kind{
	"as": KwAs, "break": KwBreak, "const": KwConst, "continue": KwContinue,
	"else": KwElse, "enum": KwEnum, "false": KwFalse, "fn": KwFn, "for": KwFor,
	"if": KwIf, "impl": KwImpl, "in": KwIn, "let": KwLet, "loop": KwLoop,
	"match": KwMatch, "mut": KwMut, "pub": KwPub, "return": KwReturn,
	"self": KwSelfValue, "Self": KwSelfType, "struct": KwStruct, "trait": KwTrait,
	"true": KwTrue, "type": KwType, "use": KwUse, "where": KwWhere, "while": KwWhile,
}

// reserved are words held for future use. Using one as an identifier is rejected with a
// diagnostic that says so, rather than silently accepted and broken later.
var reserved = map[string]bool{
	"async": true, "await": true, "box": true, "dyn": true, "extern": true,
	"macro": true, "move": true, "ref": true, "static": true, "super": true,
	"unsafe": true, "yield": true,
}

// Suffixes recognized on numeric literals.
var intSuffixes = map[string]bool{
	"i8": true, "i16": true, "i32": true, "i64": true,
	"u8": true, "u16": true, "u32": true, "u64": true,
}

var floatSuffixes = map[string]bool{"f32": true, "f64": true}
