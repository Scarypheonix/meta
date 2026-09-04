#!/usr/bin/env python3
"""Origin -- a small dynamically typed scripting language.

Usage:
    python3 origin.py program.ori     run a file
    python3 origin.py                 start the REPL
"""

import sys

# ---------------------------------------------------------------- errors

class OriginError(Exception):
    def __init__(self, message, line=None):
        self.message = message
        self.line = line
        super().__init__(message)

    def __str__(self):
        where = f" (line {self.line})" if self.line else ""
        return f"{self.message}{where}"


# ---------------------------------------------------------------- lexer

KEYWORDS = {
    "let", "fn", "return", "if", "else", "while", "for", "in",
    "true", "false", "nil", "and", "or", "not", "break", "continue",
}

TWO_CHAR = {"==", "!=", "<=", ">=", "->"}
ONE_CHAR = set("+-*/%(){}[],:.<>=!")


class Token:
    def __init__(self, kind, value, line):
        self.kind = kind      # num str name kw op eof
        self.value = value
        self.line = line

    def __repr__(self):
        return f"Token({self.kind},{self.value!r},{self.line})"


def tokenize(src):
    tokens = []
    i, line = 0, 1
    n = len(src)
    while i < n:
        c = src[i]
        if c == "\n":
            line += 1
            i += 1
            continue
        if c in " \t\r":
            i += 1
            continue
        if c == "#":
            while i < n and src[i] != "\n":
                i += 1
            continue
        if c.isdigit() or (c == "." and i + 1 < n and src[i + 1].isdigit()):
            start = i
            seen_dot = False
            while i < n and (src[i].isdigit() or (src[i] == "." and not seen_dot)):
                if src[i] == ".":
                    seen_dot = True
                i += 1
            text = src[start:i]
            tokens.append(Token("num", float(text) if seen_dot else int(text), line))
            continue
        if c.isalpha() or c == "_":
            start = i
            while i < n and (src[i].isalnum() or src[i] == "_"):
                i += 1
            word = src[start:i]
            tokens.append(Token("kw" if word in KEYWORDS else "name", word, line))
            continue
        if c == '"':
            i += 1
            buf = []
            while True:
                if i >= n:
                    raise OriginError("unterminated string", line)
                ch = src[i]
                if ch == '"':
                    i += 1
                    break
                if ch == "\\":
                    if i + 1 >= n:
                        raise OriginError("unterminated escape", line)
                    esc = src[i + 1]
                    buf.append({"n": "\n", "t": "\t", '"': '"', "\\": "\\"}.get(esc, esc))
                    i += 2
                    continue
                if ch == "\n":
                    line += 1
                buf.append(ch)
                i += 1
            tokens.append(Token("str", "".join(buf), line))
            continue
        pair = src[i:i + 2]
        if pair in TWO_CHAR:
            tokens.append(Token("op", pair, line))
            i += 2
            continue
        if c in ONE_CHAR:
            tokens.append(Token("op", c, line))
            i += 1
            continue
        raise OriginError(f"unexpected character {c!r}", line)
    tokens.append(Token("eof", None, line))
    return tokens


# ---------------------------------------------------------------- ast
# Nodes are plain tuples: (kind, line, ...payload)

class Parser:
    def __init__(self, tokens):
        self.toks = tokens
        self.pos = 0

    # -- helpers
    @property
    def cur(self):
        return self.toks[self.pos]

    def at(self, kind, value=None):
        t = self.cur
        return t.kind == kind and (value is None or t.value == value)

    def take(self):
        t = self.cur
        self.pos += 1
        return t

    def accept(self, kind, value=None):
        if self.at(kind, value):
            return self.take()
        return None

    def expect(self, kind, value=None):
        if self.at(kind, value):
            return self.take()
        got = self.cur.value if self.cur.kind != "eof" else "end of file"
        want = value or kind
        raise OriginError(f"expected {want!r} but found {got!r}", self.cur.line)

    # -- entry
    def parse(self):
        body = []
        while not self.at("eof"):
            body.append(self.statement())
        return body

    def block(self):
        self.expect("op", "{")
        body = []
        while not self.at("op", "}"):
            if self.at("eof"):
                raise OriginError("unclosed block, expected '}'", self.cur.line)
            body.append(self.statement())
        self.expect("op", "}")
        return body

    # -- statements
    def statement(self):
        t = self.cur
        if t.kind == "kw":
            if t.value == "let":
                return self.let_stmt()
            if t.value == "fn" and self.toks[self.pos + 1].kind == "name":
                return self.fn_decl()
            if t.value == "if":
                return self.if_stmt()
            if t.value == "while":
                return self.while_stmt()
            if t.value == "for":
                return self.for_stmt()
            if t.value == "return":
                self.take()
                value = None if self.at("op", "}") else self.expression()
                return ("return", t.line, value)
            if t.value == "break":
                self.take()
                return ("break", t.line)
            if t.value == "continue":
                self.take()
                return ("continue", t.line)
        expr = self.expression()
        if self.at("op", "="):
            line = self.take().line
            value = self.expression()
            if expr[0] not in ("name", "index", "field"):
                raise OriginError("left side of '=' is not assignable", line)
            return ("assign", line, expr, value)
        return ("expr", t.line, expr)

    def let_stmt(self):
        line = self.expect("kw", "let").line
        name = self.expect("name").value
        self.expect("op", "=")
        return ("let", line, name, self.expression())

    def fn_decl(self):
        line = self.expect("kw", "fn").line
        name = self.expect("name").value
        params = self.params()
        return ("let", line, name, ("fn", line, name, params, self.block()))

    def params(self):
        self.expect("op", "(")
        params = []
        while not self.at("op", ")"):
            params.append(self.expect("name").value)
            if not self.accept("op", ","):
                break
        self.expect("op", ")")
        return params

    def if_stmt(self):
        line = self.expect("kw", "if").line
        cond = self.expression()
        then = self.block()
        otherwise = None
        if self.accept("kw", "else"):
            otherwise = [self.if_stmt()] if self.at("kw", "if") else self.block()
        return ("if", line, cond, then, otherwise)

    def while_stmt(self):
        line = self.expect("kw", "while").line
        cond = self.expression()
        return ("while", line, cond, self.block())

    def for_stmt(self):
        line = self.expect("kw", "for").line
        name = self.expect("name").value
        self.expect("kw", "in")
        seq = self.expression()
        return ("for", line, name, seq, self.block())

    # -- expressions (precedence climbing)
    def expression(self):
        return self.or_expr()

    def or_expr(self):
        left = self.and_expr()
        while self.at("kw", "or"):
            line = self.take().line
            left = ("or", line, left, self.and_expr())
        return left

    def and_expr(self):
        left = self.equality()
        while self.at("kw", "and"):
            line = self.take().line
            left = ("and", line, left, self.equality())
        return left

    def equality(self):
        left = self.comparison()
        while self.at("op", "==") or self.at("op", "!="):
            tok = self.take()
            left = ("binary", tok.line, tok.value, left, self.comparison())
        return left

    def comparison(self):
        left = self.additive()
        while any(self.at("op", o) for o in ("<", ">", "<=", ">=")):
            tok = self.take()
            left = ("binary", tok.line, tok.value, left, self.additive())
        return left

    def additive(self):
        left = self.multiplicative()
        while self.at("op", "+") or self.at("op", "-"):
            tok = self.take()
            left = ("binary", tok.line, tok.value, left, self.multiplicative())
        return left

    def multiplicative(self):
        left = self.unary()
        while any(self.at("op", o) for o in ("*", "/", "%")):
            tok = self.take()
            left = ("binary", tok.line, tok.value, left, self.unary())
        return left

    def unary(self):
        if self.at("op", "-") or self.at("kw", "not"):
            tok = self.take()
            return ("unary", tok.line, tok.value, self.unary())
        return self.postfix()

    def postfix(self):
        node = self.primary()
        while True:
            if self.at("op", "("):
                line = self.cur.line
                node = ("call", line, node, self.args())
            elif self.at("op", "["):
                line = self.take().line
                idx = self.expression()
                self.expect("op", "]")
                node = ("index", line, node, idx)
            elif self.at("op", "."):
                line = self.take().line
                node = ("field", line, node, self.expect("name").value)
            else:
                return node

    def args(self):
        self.expect("op", "(")
        args = []
        while not self.at("op", ")"):
            args.append(self.expression())
            if not self.accept("op", ","):
                break
        self.expect("op", ")")
        return args

    def primary(self):
        t = self.cur
        if t.kind in ("num", "str"):
            self.take()
            return ("lit", t.line, t.value)
        if t.kind == "kw":
            if t.value == "true":
                self.take()
                return ("lit", t.line, True)
            if t.value == "false":
                self.take()
                return ("lit", t.line, False)
            if t.value == "nil":
                self.take()
                return ("lit", t.line, None)
            if t.value == "fn":
                self.take()
                return ("fn", t.line, None, self.params(), self.block())
        if t.kind == "name":
            self.take()
            return ("name", t.line, t.value)
        if self.at("op", "("):
            self.take()
            inner = self.expression()
            self.expect("op", ")")
            return inner
        if self.at("op", "["):
            line = self.take().line
            items = []
            while not self.at("op", "]"):
                items.append(self.expression())
                if not self.accept("op", ","):
                    break
            self.expect("op", "]")
            return ("list", line, items)
        if self.at("op", "{"):
            line = self.take().line
            pairs = []
            while not self.at("op", "}"):
                key = self.expression()
                self.expect("op", ":")
                pairs.append((key, self.expression()))
                if not self.accept("op", ","):
                    break
            self.expect("op", "}")
            return ("map", line, pairs)
        raise OriginError(f"unexpected {t.value!r}", t.line)


def parse(src):
    return Parser(tokenize(src)).parse()


# ---------------------------------------------------------------- runtime

class Env:
    def __init__(self, parent=None):
        self.vars = {}
        self.parent = parent

    def get(self, name, line):
        env = self
        while env is not None:
            if name in env.vars:
                return env.vars[name]
            env = env.parent
        raise OriginError(f"undefined name {name!r}", line)

    def define(self, name, value):
        self.vars[name] = value

    def assign(self, name, value, line):
        env = self
        while env is not None:
            if name in env.vars:
                env.vars[name] = value
                return
            env = env.parent
        raise OriginError(f"cannot assign to undefined name {name!r}; use 'let'", line)


class Function:
    def __init__(self, name, params, body, env):
        self.name = name or "<anonymous>"
        self.params = params
        self.body = body
        self.env = env


class Builtin:
    def __init__(self, name, arity, fn):
        self.name = name
        self.arity = arity     # int, or (lo, hi) with hi=None for open ended
        self.fn = fn


class _Return(Exception):
    def __init__(self, value):
        self.value = value


class _Break(Exception):
    pass


class _Continue(Exception):
    pass


def to_text(v):
    if v is None:
        return "nil"
    if v is True:
        return "true"
    if v is False:
        return "false"
    if isinstance(v, float) and v.is_integer():
        return str(int(v))
    if isinstance(v, list):
        return "[" + ", ".join(to_text(x) for x in v) + "]"
    if isinstance(v, dict):
        return "{" + ", ".join(f"{to_text(k)}: {to_text(x)}" for k, x in v.items()) + "}"
    if isinstance(v, Function):
        return f"<fn {v.name}>"
    if isinstance(v, Builtin):
        return f"<builtin {v.name}>"
    return str(v)


def type_name(v):
    if v is None:
        return "nil"
    if isinstance(v, bool):
        return "bool"
    if isinstance(v, (int, float)):
        return "num"
    if isinstance(v, str):
        return "str"
    if isinstance(v, list):
        return "list"
    if isinstance(v, dict):
        return "map"
    return "fn"


def truthy(v):
    return v is not None and v is not False


class Interpreter:
    def __init__(self, out=None):
        self.out = out or sys.stdout
        self.globals = Env()
        self.install_builtins()

    # -- builtins
    def install_builtins(self):
        def _print(*args):
            self.out.write(" ".join(to_text(a) for a in args) + "\n")
            return None

        def _len(v):
            if isinstance(v, (str, list, dict)):
                return len(v)
            raise OriginError(f"len() needs a str, list or map, got {type_name(v)}")

        def _push(lst, v):
            if not isinstance(lst, list):
                raise OriginError(f"push() needs a list, got {type_name(lst)}")
            lst.append(v)
            return lst

        def _pop(lst):
            if not isinstance(lst, list):
                raise OriginError(f"pop() needs a list, got {type_name(lst)}")
            if not lst:
                raise OriginError("pop() on an empty list")
            return lst.pop()

        def _num(v):
            try:
                text = v.strip() if isinstance(v, str) else v
                f = float(text)
                return int(f) if f.is_integer() and "." not in str(text) else f
            except (TypeError, ValueError):
                raise OriginError(f"cannot convert {to_text(v)} to a num")

        def _keys(m):
            if not isinstance(m, dict):
                raise OriginError(f"keys() needs a map, got {type_name(m)}")
            return list(m.keys())

        def _has(m, k):
            if not isinstance(m, dict):
                raise OriginError(f"has() needs a map, got {type_name(m)}")
            return k in m

        def _range(*a):
            start, stop, step = (0, a[0], 1) if len(a) == 1 else (a[0], a[1], a[2] if len(a) > 2 else 1)
            for x in (start, stop, step):
                if not isinstance(x, (int, float)) or isinstance(x, bool):
                    raise OriginError("range() needs nums")
            if step == 0:
                raise OriginError("range() step cannot be 0")
            out, cur = [], start
            while (cur < stop) if step > 0 else (cur > stop):
                out.append(cur)
                cur += step
            return out

        def _split(s, sep):
            if not isinstance(s, str) or not isinstance(sep, str):
                raise OriginError("split() needs two strs")
            return s.split(sep) if sep else list(s)

        def _join(lst, sep):
            if not isinstance(lst, list) or not isinstance(sep, str):
                raise OriginError("join() needs a list and a str")
            return sep.join(to_text(x) for x in lst)

        table = [
            ("print", (0, None), _print),
            ("len", 1, _len),
            ("push", 2, _push),
            ("pop", 1, _pop),
            ("str", 1, to_text),
            ("num", 1, _num),
            ("type", 1, type_name),
            ("keys", 1, _keys),
            ("has", 2, _has),
            ("range", (1, 3), _range),
            ("split", 2, _split),
            ("join", 2, _join),
            ("input", (0, 1), lambda *a: input(a[0] if a else "")),
        ]
        for name, arity, fn in table:
            self.globals.define(name, Builtin(name, arity, fn))

    # -- driving
    def run(self, src):
        result = None
        for node in parse(src):
            result = self.execute(node, self.globals)
        return result

    def execute_block(self, body, env):
        result = None
        for node in body:
            result = self.execute(node, env)
        return result

    def execute(self, node, env):
        kind, line = node[0], node[1]
        if kind == "expr":
            return self.eval(node[2], env)
        if kind == "let":
            value = self.eval(node[3], env)
            env.define(node[2], value)
            return None
        if kind == "assign":
            return self.assign(node[2], self.eval(node[3], env), env, line)
        if kind == "if":
            if truthy(self.eval(node[2], env)):
                return self.execute_block(node[3], Env(env))
            if node[4] is not None:
                return self.execute_block(node[4], Env(env))
            return None
        if kind == "while":
            while truthy(self.eval(node[2], env)):
                try:
                    self.execute_block(node[3], Env(env))
                except _Break:
                    break
                except _Continue:
                    continue
            return None
        if kind == "for":
            seq = self.eval(node[3], env)
            if isinstance(seq, dict):
                seq = list(seq.keys())
            if not isinstance(seq, (list, str)):
                raise OriginError(f"cannot loop over a {type_name(seq)}", line)
            for item in list(seq):
                loop_env = Env(env)
                loop_env.define(node[2], item)
                try:
                    self.execute_block(node[4], loop_env)
                except _Break:
                    break
                except _Continue:
                    continue
            return None
        if kind == "return":
            raise _Return(self.eval(node[2], env) if node[2] is not None else None)
        if kind == "break":
            raise _Break()
        if kind == "continue":
            raise _Continue()
        raise OriginError(f"cannot execute {kind!r}", line)

    def assign(self, target, value, env, line):
        if target[0] == "name":
            env.assign(target[2], value, line)
        elif target[0] == "index":
            container = self.eval(target[2], env)
            key = self.eval(target[3], env)
            self.set_index(container, key, value, line)
        elif target[0] == "field":
            container = self.eval(target[2], env)
            if not isinstance(container, dict):
                raise OriginError(f"cannot set a field on a {type_name(container)}", line)
            container[target[3]] = value
        return value

    def set_index(self, container, key, value, line):
        if isinstance(container, list):
            if not isinstance(key, int) or isinstance(key, bool):
                raise OriginError("list index must be a whole num", line)
            if key < 0 or key >= len(container):
                raise OriginError(f"index {key} out of range (len {len(container)})", line)
            container[key] = value
        elif isinstance(container, dict):
            if not isinstance(key, (str, int, float)) or isinstance(key, bool):
                raise OriginError("map keys must be a str or num", line)
            container[key] = value
        else:
            raise OriginError(f"cannot index-assign into a {type_name(container)}", line)

    # -- expressions
    def eval(self, node, env):
        kind, line = node[0], node[1]
        if kind == "lit":
            return node[2]
        if kind == "name":
            return env.get(node[2], line)
        if kind == "list":
            return [self.eval(x, env) for x in node[2]]
        if kind == "map":
            out = {}
            for key_node, val_node in node[2]:
                key = self.eval(key_node, env)
                if not isinstance(key, (str, int, float)) or isinstance(key, bool):
                    raise OriginError("map keys must be a str or num", line)
                out[key] = self.eval(val_node, env)
            return out
        if kind == "fn":
            return Function(node[2], node[3], node[4], env)
        if kind == "and":
            left = self.eval(node[2], env)
            return self.eval(node[3], env) if truthy(left) else left
        if kind == "or":
            left = self.eval(node[2], env)
            return left if truthy(left) else self.eval(node[3], env)
        if kind == "unary":
            value = self.eval(node[3], env)
            if node[2] == "not":
                return not truthy(value)
            if isinstance(value, bool) or not isinstance(value, (int, float)):
                raise OriginError(f"cannot negate a {type_name(value)}", line)
            return -value
        if kind == "binary":
            return self.binary(node[2], self.eval(node[3], env), self.eval(node[4], env), line)
        if kind == "index":
            return self.index(self.eval(node[2], env), self.eval(node[3], env), line)
        if kind == "field":
            container = self.eval(node[2], env)
            if not isinstance(container, dict):
                raise OriginError(f"cannot read a field off a {type_name(container)}", line)
            if node[3] not in container:
                raise OriginError(f"map has no key {node[3]!r}", line)
            return container[node[3]]
        if kind == "call":
            callee = self.eval(node[2], env)
            args = [self.eval(a, env) for a in node[3]]
            return self.call(callee, args, line)
        raise OriginError(f"cannot evaluate {kind!r}", line)

    def index(self, container, key, line):
        if isinstance(container, (list, str)):
            if not isinstance(key, int) or isinstance(key, bool):
                raise OriginError("index must be a whole num", line)
            if key < 0:
                key += len(container)
            if key < 0 or key >= len(container):
                raise OriginError(f"index out of range (len {len(container)})", line)
            return container[key]
        if isinstance(container, dict):
            if key not in container:
                raise OriginError(f"map has no key {to_text(key)}", line)
            return container[key]
        raise OriginError(f"cannot index a {type_name(container)}", line)

    def binary(self, op, a, b, line):
        if op == "==":
            return self.equal(a, b)
        if op == "!=":
            return not self.equal(a, b)
        if op == "+":
            if isinstance(a, str) and isinstance(b, str):
                return a + b
            if isinstance(a, list) and isinstance(b, list):
                return a + b
            if self.is_num(a) and self.is_num(b):
                return a + b
            raise OriginError(f"cannot add a {type_name(a)} and a {type_name(b)}", line)
        if op in ("<", ">", "<=", ">="):
            if (self.is_num(a) and self.is_num(b)) or (isinstance(a, str) and isinstance(b, str)):
                return {"<": a < b, ">": a > b, "<=": a <= b, ">=": a >= b}[op]
            raise OriginError(f"cannot compare a {type_name(a)} with a {type_name(b)}", line)
        if not (self.is_num(a) and self.is_num(b)):
            raise OriginError(f"cannot use '{op}' on a {type_name(a)} and a {type_name(b)}", line)
        if op == "-":
            return a - b
        if op == "*":
            return a * b
        if b == 0:
            raise OriginError("division by zero", line)
        if op == "/":
            result = a / b
            return int(result) if isinstance(a, int) and isinstance(b, int) and result.is_integer() else result
        if op == "%":
            return a % b
        raise OriginError(f"unknown operator {op!r}", line)

    @staticmethod
    def is_num(v):
        return isinstance(v, (int, float)) and not isinstance(v, bool)

    def equal(self, a, b):
        if type_name(a) != type_name(b):
            return False
        return a == b

    def call(self, callee, args, line):
        if isinstance(callee, Builtin):
            self.check_arity(callee.name, callee.arity, len(args), line)
            try:
                return callee.fn(*args)
            except OriginError as e:
                raise OriginError(e.message, e.line or line)
        if isinstance(callee, Function):
            self.check_arity(callee.name, len(callee.params), len(args), line)
            call_env = Env(callee.env)
            for name, value in zip(callee.params, args):
                call_env.define(name, value)
            try:
                self.execute_block(callee.body, call_env)
            except _Return as r:
                return r.value
            return None
        raise OriginError(f"cannot call a {type_name(callee)}", line)

    @staticmethod
    def check_arity(name, arity, got, line):
        if isinstance(arity, tuple):
            lo, hi = arity
            if got < lo or (hi is not None and got > hi):
                want = f"{lo}-{hi}" if hi is not None else f"at least {lo}"
                raise OriginError(f"{name}() takes {want} arguments, got {got}", line)
        elif got != arity:
            raise OriginError(f"{name}() takes {arity} arguments, got {got}", line)


# ---------------------------------------------------------------- cli

def run_file(path):
    try:
        with open(path, "r", encoding="utf-8") as f:
            src = f.read()
    except OSError as e:
        print(f"origin: cannot read {path}: {e}", file=sys.stderr)
        return 1
    interp = Interpreter()
    try:
        interp.run(src)
    except OriginError as e:
        print(f"origin: {e}", file=sys.stderr)
        return 1
    except RecursionError:
        print("origin: too much recursion", file=sys.stderr)
        return 1
    except BrokenPipeError:
        # the program's output was piped into something that stopped reading
        sys.stderr.close()
        return 0
    return 0


def repl():
    interp = Interpreter()
    print("Origin REPL -- type an expression, or Ctrl-D to leave.")
    buffer = ""
    while True:
        try:
            line = input("... " if buffer else "ori> ")
        except (EOFError, KeyboardInterrupt):
            print()
            return 0
        buffer = f"{buffer}\n{line}" if buffer else line
        if buffer.count("{") > buffer.count("}"):
            continue
        try:
            value = interp.run(buffer)
            if value is not None:
                print(to_text(value))
        except OriginError as e:
            print(f"origin: {e}", file=sys.stderr)
        except RecursionError:
            print("origin: too much recursion", file=sys.stderr)
        buffer = ""


def main(argv):
    if len(argv) > 2:
        print("usage: origin.py [program.ori]", file=sys.stderr)
        return 2
    return run_file(argv[1]) if len(argv) == 2 else repl()


if __name__ == "__main__":
    sys.exit(main(sys.argv))
