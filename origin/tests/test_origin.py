"""Test suite for the Origin interpreter: python3 tests/test_origin.py"""

import io
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from origin import Interpreter, OriginError  # noqa: E402

EXAMPLES = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "examples")


def run(src):
    out = io.StringIO()
    Interpreter(out=out).run(src)
    return out.getvalue()


def value(expr):
    out = io.StringIO()
    return Interpreter(out=out).run(expr)


class TestExpressions(unittest.TestCase):
    def test_arithmetic(self):
        self.assertEqual(value("1 + 2 * 3"), 7)
        self.assertEqual(value("(1 + 2) * 3"), 9)
        self.assertEqual(value("7 % 3"), 1)
        self.assertEqual(value("7 / 2"), 3.5)
        self.assertEqual(value("6 / 3"), 2)
        self.assertEqual(value("-4 + 1"), -3)

    def test_comparison_and_logic(self):
        self.assertIs(value("1 < 2"), True)
        self.assertIs(value('"a" < "b"'), True)
        self.assertIs(value("1 == 1.0"), True)
        self.assertIs(value('1 == "1"'), False)
        self.assertIs(value("not nil"), True)
        self.assertEqual(value("nil or 5"), 5)
        self.assertEqual(value("0 and 9"), 9)  # only nil and false are falsy

    def test_strings_and_collections(self):
        self.assertEqual(value('"a" + "b"'), "ab")
        self.assertEqual(value("[1, 2] + [3]"), [1, 2, 3])
        self.assertEqual(value('"hello"[1]'), "e")
        self.assertEqual(value("[1, 2, 3][-1]"), 3)
        self.assertEqual(value('{"a": 1}.a'), 1)


class TestStatements(unittest.TestCase):
    def test_let_and_assign(self):
        self.assertEqual(run("let x = 1\nx = x + 4\nprint(x)"), "5\n")

    def test_if_else_chain(self):
        src = "let g = 75\nif g > 90 { print(1) } else if g > 70 { print(2) } else { print(3) }"
        self.assertEqual(run(src), "2\n")

    def test_while_with_break_and_continue(self):
        src = """
        let i = 0
        let seen = []
        while true {
            i = i + 1
            if i == 2 { continue }
            if i > 4 { break }
            push(seen, i)
        }
        print(seen)
        """
        self.assertEqual(run(src), "[1, 3, 4]\n")

    def test_for_over_list_string_and_map(self):
        self.assertEqual(run("for x in [1, 2] { print(x) }"), "1\n2\n")
        self.assertEqual(run('for c in "hi" { print(c) }'), "h\ni\n")
        self.assertEqual(run('for k in {"a": 1, "b": 2} { print(k) }'), "a\nb\n")

    def test_index_assignment(self):
        self.assertEqual(run("let l = [1, 2]\nl[0] = 9\nprint(l)"), "[9, 2]\n")
        self.assertEqual(run('let m = {}\nm.k = 1\nm["j"] = 2\nprint(m)'), "{k: 1, j: 2}\n")


class TestFunctions(unittest.TestCase):
    def test_declaration_and_call(self):
        self.assertEqual(run("fn add(a, b) { return a + b }\nprint(add(2, 3))"), "5\n")

    def test_implicit_nil_return(self):
        self.assertEqual(run("fn f() { let x = 1 }\nprint(f())"), "nil\n")

    def test_closure_captures_environment(self):
        src = """
        fn counter() {
            let n = 0
            return fn() { n = n + 1  return n }
        }
        let c = counter()
        c()
        print(c())
        """
        self.assertEqual(run(src), "2\n")

    def test_recursion(self):
        src = "fn fact(n) { if n <= 1 { return 1 } return n * fact(n - 1) }\nprint(fact(6))"
        self.assertEqual(run(src), "720\n")

    def test_functions_as_values(self):
        src = "fn apply(f, x) { return f(x) }\nprint(apply(fn(v) { return v * 3 }, 4))"
        self.assertEqual(run(src), "12\n")

    def test_shadowing_in_block_scope(self):
        self.assertEqual(run("let x = 1\nif true { let x = 2 }\nprint(x)"), "1\n")


class TestBuiltins(unittest.TestCase):
    def test_conversions_and_inspection(self):
        self.assertEqual(value('str(1) + str(true) + str(nil)'), "1truenil")
        self.assertEqual(value('num("42")'), 42)
        self.assertEqual(value('num("2.5")'), 2.5)
        self.assertEqual(value('type([])'), "list")

    def test_collections(self):
        self.assertEqual(value('len("abc")'), 3)
        self.assertEqual(value("range(3)"), [0, 1, 2])
        self.assertEqual(value("range(1, 6, 2)"), [1, 3, 5])
        self.assertEqual(value("range(3, 0, -1)"), [3, 2, 1])
        self.assertEqual(value('split("a,b", ",")'), ["a", "b"])
        self.assertEqual(value('join([1, 2], "+")'), "1+2")
        self.assertEqual(value('keys({"a": 1})'), ["a"])
        self.assertIs(value('has({"a": 1}, "b")'), False)


class TestErrors(unittest.TestCase):
    def assertFails(self, src, needle):
        with self.assertRaises(OriginError) as ctx:
            run(src)
        self.assertIn(needle, str(ctx.exception))

    def test_runtime_errors(self):
        self.assertFails("print(missing)", "undefined name 'missing'")
        self.assertFails("x = 1", "use 'let'")
        self.assertFails('print(1 + "a")', "cannot add a num and a str")
        self.assertFails("print(1 / 0)", "division by zero")
        self.assertFails("print([1][5])", "out of range")
        self.assertFails('print({"a": 1}.b)', "no key 'b'")
        self.assertFails("print(nil())", "cannot call a nil")
        self.assertFails("fn f(a) { }\nf()", "takes 1 arguments")
        self.assertFails("for x in 5 { }", "cannot loop over a num")

    def test_syntax_errors_report_a_line(self):
        with self.assertRaises(OriginError) as ctx:
            run("let a = 1\nlet = 2")
        self.assertEqual(ctx.exception.line, 2)
        self.assertFails("if true {", "unclosed block")
        self.assertFails('let s = "oops', "unterminated string")
        self.assertFails("1 + 2 = 3", "not assignable")


class TestExamples(unittest.TestCase):
    def test_every_example_runs(self):
        names = sorted(n for n in os.listdir(EXAMPLES) if n.endswith(".ori"))
        self.assertTrue(names)
        for name in names:
            with self.subTest(example=name):
                with open(os.path.join(EXAMPLES, name), encoding="utf-8") as f:
                    self.assertTrue(run(f.read()))


if __name__ == "__main__":
    unittest.main(verbosity=2)
