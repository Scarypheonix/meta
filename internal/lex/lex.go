package lex

import (
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/source"
)

// Token is one lexed token. The literal payload fields are meaningful only for the
// matching Kind; reading Float on an Int token is a bug, not a conversion.
type Token struct {
	Kind Kind
	Span diag.Span

	// Text is the identifier's name (Ident) or the raw source slice for other kinds.
	Text string
	// Int holds an integer literal's magnitude. It is unsigned because negation is a
	// unary operator, not part of the literal (spec/01-lexical.md).
	Int uint64
	// IntOverflow reports that the literal does not fit in 64 bits at all. The value of
	// Int is then meaningless and the parser reports the range error.
	IntOverflow bool
	// Float holds a float literal's value.
	Float float64
	// Str holds a string literal with escapes already decoded.
	Str string
	// Char holds a character literal's scalar value.
	Char rune
	// Suffix is the literal's type suffix ("i32", "f64"), or "" if absent.
	Suffix string
}

// String renders a token for diagnostics and test failures.
func (t Token) String() string {
	switch t.Kind {
	case Ident:
		return "`" + t.Text + "`"
	case Int, Float, Str, Char:
		return t.Kind.String()
	case EOF:
		return "end of file"
	default:
		return "`" + t.Kind.String() + "`"
	}
}

// Lexer produces tokens from one file, reporting problems into a diagnostic bag.
//
// It never stops at the first error: on a malformed token it reports, resynchronizes as
// described in spec/01-lexical.md, and continues, so one run reports every lexical
// error in the file.
type Lexer struct {
	file *source.File
	src  string
	pos  int
	bag  *diag.Bag
}

// New returns a Lexer over f, reporting into bag.
func New(f *source.File, bag *diag.Bag) *Lexer {
	l := &Lexer{file: f, src: f.Src, bag: bag}
	l.checkUTF8()
	l.skipBOM()
	return l
}

// checkUTF8 rejects a file that is not well-formed UTF-8, naming the byte offset of the
// first invalid sequence (spec/01-lexical.md).
//
// This has to happen before tokenizing rather than during it: an invalid byte inside a
// string literal or a comment is consumed by those scanners and would otherwise be
// silently replaced by U+FFFD. The fuzzer found exactly that.
func (l *Lexer) checkUTF8() {
	for i := 0; i < len(l.src); {
		r, size := utf8.DecodeRuneInString(l.src[i:])
		if r == utf8.RuneError && size <= 1 {
			end := i + 1
			if end > len(l.src) {
				end = len(l.src)
			}
			l.errorf(i, end, "invalid UTF-8 at byte offset %d", i).
				Label("not a well-formed UTF-8 sequence").
				Note("Origin source files must be valid UTF-8")
			return
		}
		i += size
	}
	// A byte-order mark is permitted only at the start of a file.
	if idx := strings.Index(l.src[minInt(len(bom), len(l.src)):], bom); idx >= 0 {
		at := idx + minInt(len(bom), len(l.src))
		l.errorf(at, at+len(bom), "byte-order mark inside the file").
			Label("U+FEFF is allowed only as the first character").
			Help("delete it")
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Tokens lexes the whole file, returning every token including a final EOF.
func Tokens(f *source.File, bag *diag.Bag) []Token {
	l := New(f, bag)
	var out []Token
	for {
		t := l.Next()
		out = append(out, t)
		if t.Kind == EOF {
			return out
		}
	}
}

// bom is the UTF-8 encoding of U+FEFF. It is skipped at the start of a file and
// rejected anywhere else (spec/01-lexical.md).
const bom = "\uFEFF"

func (l *Lexer) skipBOM() {
	if strings.HasPrefix(l.src, bom) {
		l.pos += len(bom)
	}
}

func (l *Lexer) span(start, end int) diag.Span { return diag.NewSpan(l.file, start, end) }

func (l *Lexer) errorf(start, end int, format string, args ...any) *diag.Builder {
	return l.bag.Errorf("E0001", l.span(start, end), format, args...)
}

// peek returns the byte at pos+n without advancing, or 0 past the end.
func (l *Lexer) peek(n int) byte {
	if l.pos+n >= len(l.src) {
		return 0
	}
	return l.src[l.pos+n]
}

func (l *Lexer) atEnd() bool { return l.pos >= len(l.src) }

// nextRune decodes the scalar value at pos and advances past it.
func (l *Lexer) nextRune() rune {
	r, size := utf8.DecodeRuneInString(l.src[l.pos:])
	if size == 0 {
		size = 1
	}
	l.pos += size
	return r
}

// Next returns the next token, skipping whitespace and comments.
func (l *Lexer) Next() Token {
	l.skipTrivia()
	start := l.pos
	if l.atEnd() {
		return Token{Kind: EOF, Span: l.span(start, start)}
	}

	c := l.src[l.pos]
	switch {
	case c == '"':
		return l.lexString()
	case c == '\'':
		return l.lexChar()
	case c >= '0' && c <= '9':
		return l.lexNumber()
	case isIdentStartByte(c):
		return l.lexIdent()
	}
	if c >= utf8.RuneSelf {
		// A multi-byte scalar value: an identifier if XID_Start, otherwise invalid.
		r, _ := utf8.DecodeRuneInString(l.src[l.pos:])
		if isIdentStart(r) {
			return l.lexIdent()
		}
		l.nextRune()
		l.errorf(start, l.pos, "invalid character %q in source", r).
			Label("not valid anywhere in an Origin program")
		return l.Next()
	}
	if tok, ok := l.lexPunct(); ok {
		return tok
	}
	l.pos++
	l.errorf(start, l.pos, "invalid character %q in source", rune(c)).
		Label("not valid anywhere in an Origin program")
	return l.Next()
}

// skipTrivia consumes whitespace and comments. Block comments nest; an unterminated one
// is reported against its outermost opener.
func (l *Lexer) skipTrivia() {
	for !l.atEnd() {
		c := l.src[l.pos]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f' {
			l.pos++
			continue
		}
		if c == '/' && l.peek(1) == '/' {
			for !l.atEnd() && l.src[l.pos] != '\n' {
				l.pos++
			}
			continue
		}
		if c == '/' && l.peek(1) == '*' {
			l.skipBlockComment()
			continue
		}
		return
	}
}

func (l *Lexer) skipBlockComment() {
	outermost := l.pos
	depth := 0
	for !l.atEnd() {
		if l.src[l.pos] == '/' && l.peek(1) == '*' {
			depth++
			l.pos += 2
			continue
		}
		if l.src[l.pos] == '*' && l.peek(1) == '/' {
			depth--
			l.pos += 2
			if depth == 0 {
				return
			}
			continue
		}
		l.pos++
	}
	l.errorf(outermost, outermost+2, "unterminated block comment").
		Label("this comment is never closed").
		Note("block comments nest, so an inner `/*` needs its own `*/`")
}

func isIdentStartByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// isIdentStart and isIdentContinue approximate XID_Start and XID_Continue with the
// Unicode categories Go exposes. The approximation accepts a slightly larger set than
// UAX #31 in rare cases; narrowing it needs the XID tables, which is not worth a
// dependency until someone writes Origin in a script this gets wrong.
func isIdentStart(r rune) bool {
	return r == '_' || unicode.IsLetter(r)
}

func isIdentContinue(r rune) bool {
	return isIdentStart(r) || unicode.IsDigit(r) || unicode.IsMark(r)
}

func (l *Lexer) lexIdent() Token {
	start := l.pos
	for !l.atEnd() {
		r, size := utf8.DecodeRuneInString(l.src[l.pos:])
		if !isIdentContinue(r) {
			break
		}
		l.pos += size
	}
	text := l.src[start:l.pos]
	span := l.span(start, l.pos)

	if text == "_" {
		return Token{Kind: Underscore, Span: span, Text: text}
	}
	if k, ok := keywords[text]; ok {
		return Token{Kind: k, Span: span, Text: text}
	}
	if reserved[text] {
		l.errorf(start, l.pos, "`%s` is a reserved word", text).
			Label("reserved for a future version of Origin").
			Help("rename this identifier")
		// Still produce an identifier so the parser can carry on.
	}
	return Token{Kind: Ident, Span: span, Text: text}
}

// lexNumber handles every integer and float literal form.
func (l *Lexer) lexNumber() Token {
	start := l.pos

	if l.src[l.pos] == '0' && l.pos+1 < len(l.src) {
		switch l.src[l.pos+1] {
		case 'x', 'X':
			return l.lexRadix(start, 16, isHexDigit, "hexadecimal")
		case 'o', 'O':
			return l.lexRadix(start, 8, isOctDigit, "octal")
		case 'b', 'B':
			return l.lexRadix(start, 2, isBinDigit, "binary")
		}
	}

	for !l.atEnd() && (isDecDigit(l.src[l.pos]) || l.src[l.pos] == '_') {
		l.pos++
	}
	isFloat := false

	// A '.' begins a fraction only when a digit follows it. `1.to_str()` is therefore a
	// method call on an integer, and a bare `1.` is left for the parser to reject.
	if l.peek(0) == '.' && isDecDigit(l.peek(1)) {
		isFloat = true
		l.pos++
		for !l.atEnd() && (isDecDigit(l.src[l.pos]) || l.src[l.pos] == '_') {
			l.pos++
		}
	}
	if c := l.peek(0); c == 'e' || c == 'E' {
		n := 1
		if l.peek(1) == '+' || l.peek(1) == '-' {
			n = 2
		}
		if isDecDigit(l.peek(n)) {
			isFloat = true
			l.pos += n
			for !l.atEnd() && (isDecDigit(l.src[l.pos]) || l.src[l.pos] == '_') {
				l.pos++
			}
		}
	}

	digits := l.src[start:l.pos]
	suffix := l.lexSuffix(start, isFloat)
	span := l.span(start, l.pos)

	if err := checkUnderscores(digits, 0); err != "" {
		l.errorf(start, l.pos, "%s", err).Label("in this literal")
		if isFloat {
			return Token{Kind: Float, Span: span, Text: digits, Suffix: suffix}
		}
		return Token{Kind: Int, Span: span, Text: digits, Suffix: suffix}
	}

	clean := strings.ReplaceAll(digits, "_", "")
	if isFloat {
		v, err := strconv.ParseFloat(clean, 64)
		if err != nil || math.IsInf(v, 0) {
			l.errorf(start, l.pos, "float literal is out of range").
				Label("this value rounds to infinity")
			v = 0
		}
		return Token{Kind: Float, Span: span, Text: digits, Float: v, Suffix: suffix}
	}
	v, err := strconv.ParseUint(clean, 10, 64)
	return Token{Kind: Int, Span: span, Text: digits, Int: v, IntOverflow: err != nil, Suffix: suffix}
}

func (l *Lexer) lexRadix(start, base int, isDigit func(byte) bool, name string) Token {
	l.pos += 2 // the 0x / 0o / 0b prefix
	digitStart := l.pos
	for !l.atEnd() && (isDigit(l.src[l.pos]) || l.src[l.pos] == '_') {
		l.pos++
	}
	digits := l.src[digitStart:l.pos]
	suffix := l.lexSuffix(start, false)
	span := l.span(start, l.pos)

	if digits == "" {
		l.errorf(start, l.pos, "%s literal has no digits", name).
			Label("expected at least one digit after the base prefix")
		return Token{Kind: Int, Span: span, Text: l.src[start:l.pos], Suffix: suffix}
	}
	if err := checkUnderscores(digits, 0); err != "" {
		l.errorf(start, l.pos, "%s", err).Label("in this literal")
		return Token{Kind: Int, Span: span, Text: l.src[start:l.pos], Suffix: suffix}
	}
	v, err := strconv.ParseUint(strings.ReplaceAll(digits, "_", ""), base, 64)
	return Token{Kind: Int, Span: span, Text: l.src[start:l.pos], Int: v, IntOverflow: err != nil, Suffix: suffix}
}

// lexSuffix reads a trailing type suffix and validates it against the literal's kind.
func (l *Lexer) lexSuffix(litStart int, isFloat bool) string {
	if l.atEnd() || !isIdentStartByte(l.src[l.pos]) {
		return ""
	}
	sufStart := l.pos
	for !l.atEnd() {
		r, size := utf8.DecodeRuneInString(l.src[l.pos:])
		if !isIdentContinue(r) {
			break
		}
		l.pos += size
	}
	suf := l.src[sufStart:l.pos]

	if isFloat {
		if floatSuffixes[suf] {
			return suf
		}
		if intSuffixes[suf] {
			l.errorf(sufStart, l.pos, "`%s` is not a float suffix", suf).
				Label("a float literal cannot have an integer type").
				Help("use `f32` or `f64`, or remove the fraction")
			return ""
		}
	} else {
		if intSuffixes[suf] {
			return suf
		}
		if floatSuffixes[suf] {
			return suf // 1f64 is a float literal written without a point
		}
	}
	l.errorf(sufStart, l.pos, "unknown numeric suffix `%s`", suf).
		Label("not a numeric type").
		Help("valid suffixes are i8 i16 i32 i64 u8 u16 u32 u64 f32 f64")
	_ = litStart
	return ""
}

// checkUnderscores enforces that '_' is neither the first nor the last character of a
// digit run (spec/01-lexical.md). It returns "" when the run is well formed.
func checkUnderscores(digits string, _ int) string {
	if digits == "" {
		return ""
	}
	if digits[0] == '_' {
		return "a digit separator cannot come first in a literal"
	}
	if digits[len(digits)-1] == '_' {
		return "a digit separator cannot come last in a literal"
	}
	return ""
}

func isDecDigit(c byte) bool { return c >= '0' && c <= '9' }
func isOctDigit(c byte) bool { return c >= '0' && c <= '7' }
func isBinDigit(c byte) bool { return c == '0' || c == '1' }
func isHexDigit(c byte) bool {
	return isDecDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func (l *Lexer) lexString() Token {
	start := l.pos
	l.pos++ // opening quote
	var sb strings.Builder
	for {
		if l.atEnd() || l.src[l.pos] == '\n' {
			l.errorf(start, min(l.pos, len(l.src)), "unterminated string literal").
				Label("this string is never closed").
				Note("a string literal may span lines, but this one reaches the end of the file")
			return Token{Kind: Str, Span: l.span(start, l.pos), Str: sb.String()}
		}
		if l.src[l.pos] == '"' {
			l.pos++
			return Token{Kind: Str, Span: l.span(start, l.pos), Str: sb.String()}
		}
		if l.src[l.pos] == '\\' {
			r, ok := l.lexEscape()
			if ok {
				sb.WriteRune(r)
			}
			continue
		}
		sb.WriteRune(l.nextRune())
	}
}

func (l *Lexer) lexChar() Token {
	start := l.pos
	l.pos++ // opening quote
	var r rune
	switch {
	case l.atEnd() || l.src[l.pos] == '\n':
		l.errorf(start, min(l.pos, len(l.src)), "unterminated character literal").
			Label("this character literal is never closed")
		return Token{Kind: Char, Span: l.span(start, l.pos)}
	case l.src[l.pos] == '\'':
		l.pos++
		l.errorf(start, l.pos, "empty character literal").
			Label("a character literal must contain exactly one character")
		return Token{Kind: Char, Span: l.span(start, l.pos)}
	case l.src[l.pos] == '\\':
		var ok bool
		r, ok = l.lexEscape()
		if !ok {
			r = 0
		}
	default:
		r = l.nextRune()
	}
	if !l.atEnd() && l.src[l.pos] == '\'' {
		l.pos++
		return Token{Kind: Char, Span: l.span(start, l.pos), Char: r}
	}
	// More than one character, or no closing quote: consume to the quote or end of line.
	for !l.atEnd() && l.src[l.pos] != '\'' && l.src[l.pos] != '\n' {
		l.nextRune()
	}
	if !l.atEnd() && l.src[l.pos] == '\'' {
		l.pos++
		l.errorf(start, l.pos, "character literal contains more than one character").
			Label("a `char` is exactly one Unicode scalar value").
			Help("use a string literal with double quotes for text")
	} else {
		l.errorf(start, l.pos, "unterminated character literal").
			Label("this character literal is never closed")
	}
	return Token{Kind: Char, Span: l.span(start, l.pos), Char: r}
}

// lexEscape consumes a backslash escape and returns the scalar value it denotes.
func (l *Lexer) lexEscape() (rune, bool) {
	start := l.pos
	l.pos++ // backslash
	if l.atEnd() {
		l.errorf(start, l.pos, "unterminated escape sequence").Label("expected an escape character")
		return 0, false
	}
	c := l.src[l.pos]
	l.pos++
	switch c {
	case 'n':
		return '\n', true
	case 'r':
		return '\r', true
	case 't':
		return '\t', true
	case '0':
		return 0, true
	case '\\':
		return '\\', true
	case '\'':
		return '\'', true
	case '"':
		return '"', true
	case 'x':
		return l.lexHexEscape(start)
	case 'u':
		return l.lexUnicodeEscape(start)
	default:
		l.errorf(start, l.pos, "unknown escape sequence `\\%c`", rune(c)).
			Label("not a valid escape").
			Help(`valid escapes are \n \r \t \0 \\ \' \" \xNN and \u{...}`)
		return 0, false
	}
}

func (l *Lexer) lexHexEscape(start int) (rune, bool) {
	if l.pos+1 >= len(l.src) || !isHexDigit(l.src[l.pos]) || !isHexDigit(l.src[l.pos+1]) {
		l.errorf(start, min(l.pos+2, len(l.src)), `\x needs exactly two hexadecimal digits`).
			Label("malformed byte escape")
		return 0, false
	}
	v, _ := strconv.ParseUint(l.src[l.pos:l.pos+2], 16, 32)
	l.pos += 2
	if v > 0x7F {
		l.errorf(start, l.pos, `\x escapes are limited to \x00 through \x7F`).
			Label("value above 0x7F").
			Note("a byte above 0x7F is not a Unicode scalar value on its own").
			Help(`use \u{...} to write a scalar value`)
		return 0, false
	}
	return rune(v), true
}

func (l *Lexer) lexUnicodeEscape(start int) (rune, bool) {
	if l.atEnd() || l.src[l.pos] != '{' {
		l.errorf(start, l.pos, `\u must be followed by "{"`).Label("malformed unicode escape")
		return 0, false
	}
	l.pos++
	digitStart := l.pos
	for !l.atEnd() && isHexDigit(l.src[l.pos]) {
		l.pos++
	}
	digits := l.src[digitStart:l.pos]
	if l.atEnd() || l.src[l.pos] != '}' {
		l.errorf(start, l.pos, `unterminated unicode escape; expected "}"`).Label("malformed unicode escape")
		return 0, false
	}
	l.pos++
	if len(digits) == 0 || len(digits) > 6 {
		l.errorf(start, l.pos, `\u{...} takes 1 to 6 hexadecimal digits, found %d`, len(digits)).
			Label("malformed unicode escape")
		return 0, false
	}
	v, _ := strconv.ParseUint(digits, 16, 32)
	r := rune(v)
	if r > unicode.MaxRune {
		l.errorf(start, l.pos, "0x%X is above the maximum scalar value 0x10FFFF", v).
			Label("not a Unicode scalar value")
		return 0, false
	}
	if r >= 0xD800 && r <= 0xDFFF {
		l.errorf(start, l.pos, "0x%X is a surrogate, not a Unicode scalar value", v).
			Label("surrogates cannot appear in Origin text").
			Note("Origin strings are UTF-8 and `char` is a scalar value")
		return 0, false
	}
	return r, true
}

// lexPunct implements maximal munch over the operator table.
func (l *Lexer) lexPunct() (Token, bool) {
	start := l.pos
	two := ""
	if l.pos+1 < len(l.src) {
		two = l.src[l.pos : l.pos+2]
	}
	if k, ok := twoCharPunct[two]; ok {
		l.pos += 2
		return Token{Kind: k, Span: l.span(start, l.pos), Text: two}, true
	}
	one := l.src[l.pos : l.pos+1]
	if k, ok := oneCharPunct[one]; ok {
		l.pos++
		return Token{Kind: k, Span: l.span(start, l.pos), Text: one}, true
	}
	return Token{}, false
}

var twoCharPunct = map[string]Kind{
	"::": ColonColon, "..": DotDot, "->": Arrow, "=>": FatArrow,
	"<<": Shl, ">>": Shr,
	"+=": PlusEq, "-=": MinusEq, "*=": StarEq, "/=": SlashEq, "%=": PercentEq,
	"==": EqEq, "!=": BangEq, "<=": Le, ">=": Ge,
	"&&": AmpAmp, "||": PipePipe,
}

var oneCharPunct = map[string]Kind{
	"(": LParen, ")": RParen, "{": LBrace, "}": RBrace, "[": LBracket, "]": RBracket,
	",": Comma, ";": Semi, ":": Colon, ".": Dot, "@": At,
	"+": Plus, "-": Minus, "*": Star, "/": Slash, "%": Percent,
	"&": Amp, "|": Pipe, "^": Caret, "!": Bang,
	"=": Assign, "<": Lt, ">": Gt, "?": Question,
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
