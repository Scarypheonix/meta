package vm_test

import "github.com/scarypheonix/meta/internal/bytecode"

// bytecodeProgram wraps a compiled program so the test helpers can pass it around
// without every call site naming the bytecode package.
type bytecodeProgram struct{ prog *bytecode.Program }
