// Origin syntax highlighting for CodeMirror (ADR-0034).
//
// This is a highlighter, not a parser. The compiler in the same page is the authority on
// whether a program is valid; this decides colour and nothing else, and where the two
// disagree this file is wrong by definition. It is written against docs/spec/01-lexical.md
// so that "wrong" is a question with an answer.
//
// The three things it has to get right that a naive tokenizer gets wrong, all from §01:
//
//   - Block comments NEST. `/* /* */ */` is one comment, so the depth is state.
//   - A string literal MAY SPAN LINES. It stays open across a newline, which means the
//     string is state too, not a per-line affair.
//   - `\(expr)` inside a string is an INTERPOLATION holding a real expression. The
//     expression is tokenized as code and ends at the `)` matching its own `(`, so the
//     nesting is a stack -- a string inside an interpolation inside a string is legal and
//     the corpus contains one.

(function () {
  "use strict";

  // §01: reserved, and never usable as identifiers.
  const KEYWORDS = new Set([
    "as", "break", "const", "continue", "else", "enum", "false", "fn",
    "for", "if", "impl", "in", "let", "loop", "match", "mut",
    "pub", "return", "self", "Self", "struct", "trait", "true", "type",
    "use", "where", "while",
  ]);

  // §01: reserved for future use, and REJECTED as identifiers with a "reserved word"
  // diagnostic. They are coloured as keywords because that is what they are -- a program
  // may not use them as names, and showing them as ordinary identifiers would suggest it
  // could.
  const RESERVED = new Set([
    "async", "await", "box", "dyn", "extern", "macro", "move", "ref",
    "static", "super", "unsafe", "yield",
  ]);

  // §01's primitive type names, plus the wildcard. `Self` is a keyword above.
  const PRIMITIVES = new Set([
    "i8", "i16", "i32", "i64", "u8", "u16", "u32", "u64",
    "f32", "f64", "bool", "char", "String",
  ]);

  const INT_SUFFIX = /^(i8|i16|i32|i64|u8|u16|u32|u64)/;
  const FLOAT_SUFFIX = /^(f32|f64)/;

  function isIdentStart(ch) {
    return /[A-Za-z_]/.test(ch) || ch.charCodeAt(0) > 127;
  }
  function isIdentChar(ch) {
    return /[A-Za-z0-9_]/.test(ch) || ch.charCodeAt(0) > 127;
  }

  // top is the innermost frame: "code", "string", or an interpolation carrying the paren
  // depth of the expression inside it.
  function top(state) {
    return state.stack[state.stack.length - 1];
  }

  // eatBlockComment consumes as much of a nested block comment as this line holds,
  // maintaining the depth. §01: the nesting is real, and an unterminated one is an error
  // the compiler reports -- here it simply stays open.
  function eatBlockComment(stream, state) {
    while (!stream.eol()) {
      if (stream.match("*/")) {
        state.block--;
        if (state.block === 0) return "comment";
        continue;
      }
      if (stream.match("/*")) {
        state.block++;
        continue;
      }
      stream.next();
    }
    return "comment";
  }

  // eatString consumes string content up to the closing quote, an interpolation, or the
  // end of the line -- the last of which leaves the string open, because §01 allows a
  // literal to span lines.
  function eatString(stream, state) {
    while (!stream.eol()) {
      const ch = stream.next();
      if (ch === "\\") {
        if (stream.peek() === "(") {
          // Back up so the interpolation's own token starts at the backslash.
          stream.backUp(1);
          return "string";
        }
        stream.next(); // the escaped character; §01 decides which are legal, not this
        continue;
      }
      if (ch === '"') {
        state.stack.pop();
        return "string";
      }
    }
    return "string";
  }

  function token(stream, state) {
    if (state.block > 0) return eatBlockComment(stream, state);

    const frame = top(state);

    if (frame.t === "string") {
      if (stream.match("\\(")) {
        state.stack.push({ t: "interp", depth: 0 });
        return "interpolation";
      }
      return eatString(stream, state);
    }

    if (stream.eatSpace()) return null;

    // Comments, before the `/` operator: maximal munch (§01).
    if (stream.match("//")) {
      stream.skipToEnd();
      return "comment";
    }
    if (stream.match("/*")) {
      state.block = 1;
      return eatBlockComment(stream, state);
    }

    const ch = stream.peek();

    // A string opens a frame rather than being consumed here, so that it survives a
    // newline.
    if (ch === '"') {
      stream.next();
      state.stack.push({ t: "string" });
      return eatString(stream, state);
    }

    // §01: a character literal must close on the line it opens, so it needs no state.
    if (ch === "'") {
      stream.next();
      while (!stream.eol()) {
        const c = stream.next();
        if (c === "\\") {
          stream.next();
          continue;
        }
        if (c === "'") return "character";
      }
      return "invalid"; // unterminated on its line: §01 rejects it, and so does the colour
    }

    // Numbers. §01: hex, octal and binary bases, `_` separators, an optional width suffix,
    // and floats with an exponent.
    if (/[0-9]/.test(ch)) {
      if (stream.match(/^0[xX][0-9a-fA-F_]+/) ||
          stream.match(/^0[oO][0-7_]+/) ||
          stream.match(/^0[bB][01_]+/)) {
        stream.match(INT_SUFFIX);
        return "number";
      }
      stream.match(/^[0-9][0-9_]*/);
      let isFloat = false;
      // A `.` followed by a digit is a fraction; `1.to_str()` is a method call, and the
      // difference is exactly whether a digit follows.
      if (stream.match(/^\.[0-9][0-9_]*/)) isFloat = true;
      if (stream.match(/^[eE][+-]?[0-9][0-9_]*/)) isFloat = true;
      if (isFloat) {
        stream.match(FLOAT_SUFFIX);
      } else if (!stream.match(FLOAT_SUFFIX)) {
        stream.match(INT_SUFFIX);
      }
      return "number";
    }

    if (isIdentStart(ch)) {
      let word = "";
      while (!stream.eol() && isIdentChar(stream.peek())) word += stream.next();
      if (KEYWORDS.has(word) || RESERVED.has(word)) return "keyword";
      if (PRIMITIVES.has(word)) return "typeName";
      // §01's naming convention: types, traits and enum variants are UpperCamelCase,
      // everything nameable by a program is lower_snake_case. The linter enforces it, so
      // this is reading the project's own rule rather than guessing at one.
      if (/^[A-Z]/.test(word)) return "typeName";
      // A name followed by `(` is being called. Colouring it separately is the one thing
      // here that is about reading rather than about lexing.
      const rest = stream.string.slice(stream.pos);
      if (/^\s*\(/.test(rest)) return "functionName";
      return "variableName";
    }

    // Interpolation frames end at the `)` that matches their own `(`, counted in tokens
    // (§01) -- which is what tracking depth here does.
    if (frame.t === "interp") {
      if (ch === "(") {
        stream.next();
        frame.depth++;
        return "punctuation";
      }
      if (ch === ")") {
        stream.next();
        if (frame.depth === 0) {
          state.stack.pop(); // back into the string that opened this
          return "interpolation";
        }
        frame.depth--;
        return "punctuation";
      }
    }

    // §01's operators, longest first: the lexer uses maximal munch and so does this.
    if (stream.match(/^(<<|>>|==|!=|<=|>=|&&|\|\||->|=>|::|\+=|-=|\*=|\/=|%=)/)) {
      return "operator";
    }
    if (stream.match(/^[+\-*/%&|^!<>=?]/)) return "operator";
    if (stream.match(/^[(){}\[\],;:.@]/)) return "punctuation";

    stream.next();
    return "invalid";
  }

  window.originMode = function (CM) {
    const t = CM.tags;
    return CM.StreamLanguage.define({
      name: "origin",
      startState() {
        return { stack: [{ t: "code" }], block: 0 };
      },
      copyState(state) {
        // The frames are mutated in place (an interpolation's depth), so a shallow copy of
        // the array would let one line's tokenizing corrupt another's.
        return {
          stack: state.stack.map((f) => Object.assign({}, f)),
          block: state.block,
        };
      },
      token,
      languageData: {
        commentTokens: { line: "//", block: { open: "/*", close: "*/" } },
        closeBrackets: { brackets: ["(", "[", "{", '"'] },
        indentOnInput: /^\s*[}\])]$/,
      },
      tokenTable: {
        keyword: t.keyword,
        typeName: t.typeName,
        functionName: t.function(t.variableName),
        variableName: t.variableName,
        number: t.number,
        string: t.string,
        character: t.character,
        interpolation: t.special(t.string),
        comment: t.lineComment,
        operator: t.operator,
        punctuation: t.punctuation,
        invalid: t.invalid,
      },
    });
  };
})();
