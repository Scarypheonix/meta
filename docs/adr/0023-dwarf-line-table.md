# ADR-0023: A minimal DWARF4 line table, no type or variable information

**Status:** accepted · **Date:** 2026-08-30 · **Decided by:** implementer (user delegated)

## Context

spec/11-codegen.md's "Debug information" section is normative and, until this ADR,
unimplemented:

> The executable carries DWARF line-number information mapping each machine-code address
> to a source file, line and column. This is what makes `lldb breakpoint set --file
> hello.origin --line 4` resolve to an address and stop there. Full DWARF type and
> variable information is DEFERRED: the line table is what the phase's acceptance
> criterion needs.

`internal/obj/elf.go`'s own writer comment already anticipated this exactly: "There is no
section header table... A section table arrives with the DWARF line information, which is
the first thing that needs one." Nothing beyond that comment existed — no `.debug_line`,
no section header table, no symbol table, and `internal/backend` records a source
`diag.Span` per IR value (already used for trap messages) but nothing carries it to a
machine-code *address*.

This gap was found, not assigned: the ADR-0022 collector milestone made every other piece
of Phase 5's own exit criterion automatable, and re-reading spec/11-codegen.md's worked
examples to confirm that turned up `lldb, break on a source line | stops, and bt names
the Origin function` as a line this repository could not yet produce, on any machine. It
is not a target-machine confirmation step (ADR-0003's own, separate, genuinely
unautomatable checklist item) — it is a missing compiler feature, and `lldb` and
`llvm-dwarfdump` both being present in this container mean it can be built and verified
here, against the ELF path, before it ever needs the Mac.

## Decision

### Scope: a real, correct line table; no types, no variables, no call-frame info

DWARF is deliberately not implemented beyond what the spec commits to. `.debug_line` maps
every source-line boundary in the program to the machine-code address where it starts,
and one `DW_TAG_compile_unit` DIE (in `.debug_info`/`.debug_abbrev`) is enough to anchor
it — `DW_AT_stmt_list` is the only thing that tells `lldb` where to find the program for
a given file. No `DW_TAG_subprogram`, `DW_TAG_variable`, `DW_TAG_base_type`, or any other
DIE exists yet; emitting them with nothing (no name mangling scheme, no type system
mapping) to build them against would be exactly the "speculative" work the spec's own
text already ruled out for full type/variable info, and the same reasoning extends to
everything past a line table.

**"`bt` names the Origin function" comes from an ELF symbol table, not DWARF.** A
`DW_TAG_subprogram` DIE is the "proper" DWARF way to name a function, but it is real
additional surface (a DIE per function, a children-bearing abbrev, address-range
attributes) for a fact a plain `STT_FUNC` `.symtab` entry already states in the simplest
form ELF itself defines. `lldb` and `gdb` both resolve a backtrace frame's name from
`.symtab` when no richer DWARF subprogram info exists, and Origin's native calling
convention already gives every function a standard `push rbp; mov rbp, rsp` prologue, so
frame-pointer-based unwinding — which is what a symbol-table-only binary gets, with no
`.debug_frame`/`.eh_frame` at all — works without anything further.

### DWARF 4, 32-bit format, no special opcodes

DWARF 4 over DWARF 5: DWARF 5 restructured the line-program header's file/directory
tables (a format-descriptor scheme replacing the fixed layout) for cross-referencing
multiple compilation units' file tables efficiently — a problem Origin does not have (one
source file compiled to one executable, Phase 5's own scope). DWARF 4's simpler, fixed
header shape is correct, is what `llvm-dwarfdump`/`lldb` in this container parse without
any version-specific caveats, and is not a decision that costs anything to revisit later.

32-bit DWARF (a 4-byte `unit_length`) over 64-bit DWARF (the `0xffffffff` escape plus an
8-byte length): the whole program is nowhere near 4 GiB of debug info.

The line-number program uses only the standard and extended opcodes
(`DW_LNS_advance_line`, `DW_LNS_set_column`, `DW_LNS_copy`, `DW_LNE_set_address`,
`DW_LNE_end_sequence`) and never a *special* opcode — DWARF's single-byte combined
address+line-advance encoding, an optional compactness optimization the spec explicitly
permits skipping. Every row costs a few more bytes than the compact form would, which is
irrelevant at this program's size and avoids the special-opcode table's own tuning
parameters (`line_base`, `line_range`) ever needing to matter for correctness.

### Built entirely from text addresses; never from read-only or writable data

This is what makes the line table free to build identically on both of `backend.Build`'s
passes, with no new pass-length check needed alongside `Build`'s existing text/roData
one. `internal/obj.Plan`'s `TextAddr` is `t.Base + align(headerSize(t), 16)` — a function
of the *target* alone, unaffected by the sizes `Plan` is given — so it is identical on
both passes, and every instruction's encoded length is already pass-invariant by
construction (ADR-0017's own two-pass determinism guarantee: an instruction's length
never depends on an operand's value). A `(address, span)` table built purely from
addresses inside `.text` is therefore byte-identical on both passes automatically, with
nothing new to check. The line table's one string, the source file's own recorded name
(`source.File.Name`, used verbatim — spec/11-codegen.md's Determinism clause forbids
resolving it to an absolute path that would vary across checkouts), is embedded directly
in `.debug_info`/`.debug_line` via `DW_FORM_string`, never through a `.debug_str`
indirection — one fewer section, and one fewer thing that could accidentally reference a
roData offset.

### A new `internal/dwarf` package owns the byte-level encoding

Process rule 5: the line-program state machine, the abbrev/DIE encoding, and the ELF
symbol-table shape are a *representation* two things must agree on — `internal/backend`,
which knows the (address, span, function) facts, and `internal/obj`, which embeds the
resulting bytes into a container format — so, like `internal/x86`'s instruction encoder
and `internal/layout`'s object layout, they get one home with their own tests rather than
being inlined into either caller. `internal/backend` collects `(address, span)` pairs
during `function()`'s existing instruction-lowering loop (one entry whenever a value's
span differs from the previous one — DWARF only needs entries at line boundaries, not
per instruction) and each function's own `(name, address, size)`, and hands both to
`internal/dwarf`'s builders; `internal/obj` receives finished byte slices
(`DebugAbbrev`, `DebugInfo`, `DebugLine`, symbol data) on `Image` and is responsible only
for where they sit in the file and how the container's own section/symbol tables
describe them.

### ELF gets a real section header table; Mach-O gets a `__DWARF` segment

Both go at the end of the file, after the two loaded segments, exactly where
`elf.go`'s own anticipating comment placed them — `headerSize` (and therefore every
loaded segment's address) is unaffected, so nothing about existing addressing changes.
ELF needs `.text`, `.rodata`, `.data` "shadow" sections (describing the already-loaded
bytes so a section-based tool can name them) alongside `.debug_abbrev`, `.debug_info`,
`.debug_line`, `.symtab`, `.strtab`, and `.shstrtab` (which names all the others,
itself included). Mach-O embeds the same three debug byte slices directly in
`__DWARF,__debug_abbrev` / `__debug_info` / `__debug_line` sections (Mach-O's own
native convention for embedded DWARF, what `lldb` on macOS already expects with no
separate `dSYM` bundle) and a plain Mach-O symbol table (`LC_SYMTAB`) for function
names.

## Consequences

- **Verified in this container, against `lldb` itself, not just structurally.** ELF's
  path is the one this container can run end to end: build a program, launch it under
  `lldb -b` (batch mode), `breakpoint set --file <name> --line <N>`, `run`, confirm it
  stops there and `bt` names the enclosing Origin function. This is the actual acceptance
  criterion from spec/11-codegen.md's worked-examples table, executed for real rather
  than only claimed. `llvm-dwarfdump --debug-line` and Go's own `debug/elf`+`debug/dwarf`
  (parsing the emitted bytes back, the same pattern `internal/obj`'s existing
  `TestELFParses`/`TestMachOParses` already use) verify the encoded structure directly.
- **Mach-O's own correctness is structural-only here**, same limit every other native
  Mach-O behavior has had since ADR-0003: `debug/macho` can parse the emitted segment
  and confirm the DWARF bytes are byte-identical to what ELF embeds, but `lldb` actually
  breaking on a line in a *Mach-O* binary is still the target machine's own confirmation,
  unautomatable here — no different from "the binary runs" already being on that list.

  **Amended after ADR-0024.** That limit was drawn too pessimistically. `lldb` and
  `llvm-dwarfdump` both read Mach-O *cross-platform*, and resolving a breakpoint by
  file and line is a static DWARF lookup, not execution — so most of this is verifiable
  in the container after all. Against a Mach-O built by the real compiler here, `lldb`
  resolves `breakpoint set --file dbg2.origin --line 4` to `add + 22, address =
  0x100000fe6` and `image lookup -n add` names the function out of the symbol table;
  `llvm-dwarfdump --debug-line` prints the whole line program with `__TEXT` addresses.
  What genuinely needs the Mac is only the *live process* half: stopping there, and
  `bt` naming frames at run time. `internal/backend`'s own
  `TestMachOBuildCarriesValidDebugInfo` now covers the Mach-O path end to end through
  the compiler, which it never did while this ADR's Mach-O support was written.
- **A future call-frame table, subprogram DIEs, and type/variable info remain fully
  additive.** Nothing about the compile-unit-only, symtab-for-names shape chosen here
  needs revisiting to add them later: a `.debug_frame` section is a new, independent
  byte stream: `DW_TAG_subprogram` DIEs are new children of the one compile-unit DIE
  already anchoring the file; both slot in without changing the line table's own
  encoding or its address-only, pass-invariant construction.
