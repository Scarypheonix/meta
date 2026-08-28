# End-to-end suite

Each case is `NAME.origin` plus companion files holding the exact expected result:

| File | Required | Meaning |
|---|---|---|
| `NAME.origin` | yes | the program |
| `NAME.out` | yes | exact expected stdout (may be empty) |
| `NAME.exit` | yes | exact expected exit status, one line |
| `NAME.err` | no | exact expected stderr; absent means stderr must be empty |

From Phase 4 on, every case runs at `-O0`, `-O1` and `-O2` and all three must produce
byte-identical output — that is the pass-verification harness, and it is the reason
spec §04 fully specifies evaluation order.

Cases are derived from `docs/spec/10-examples.md`, which is normative: if a case and the
spec disagree, one of them is a bug and the fix is never to edit the expected output to
match the implementation.
