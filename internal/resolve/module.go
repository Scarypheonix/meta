package resolve

import "github.com/scarypheonix/meta/internal/ast"

// Module is one node of the package's module tree.
//
// The filesystem is the module tree (spec/07-modules.md): a file's path under the source
// root, with separators replaced by `::`, is its module path. Nothing has to be
// registered anywhere for a file to be compiled.
type Module struct {
	// Name is the last path segment; the root module's name is empty.
	Name string
	// Parent is nil for the root.
	Parent *Module
	// Children are the submodules, by name.
	Children map[string]*Module
	// Files are the source files that make up this module.
	Files []*ast.File
	// Items are the names this module declares, whether public or not.
	Items map[string]Ref
	// Pub records which of Items are `pub`.
	Pub map[string]bool
	// Scope is where names resolve inside this module.
	Scope *scope
	// Prelude marks the module whose items go into the global scope.
	Prelude bool
}

func newModule(name string, parent *Module) *Module {
	return &Module{
		Name:     name,
		Parent:   parent,
		Children: map[string]*Module{},
		Items:    map[string]Ref{},
		Pub:      map[string]bool{},
	}
}

// Path returns the module's `::`-separated path from the root, which is "" for the root
// itself.
func (m *Module) Path() string {
	if m == nil || m.Parent == nil {
		return m.pathName()
	}
	parent := m.Parent.Path()
	if parent == "" {
		return m.Name
	}
	return parent + "::" + m.Name
}

func (m *Module) pathName() string {
	if m == nil {
		return ""
	}
	return m.Name
}

// Lookup finds a name declared in this module, and reports whether it is public.
func (m *Module) Lookup(name string) (Ref, bool, bool) {
	ref, ok := m.Items[name]
	return ref, m.Pub[name], ok
}

// Describe names the module for diagnostics.
func (m *Module) Describe() string {
	if p := m.Path(); p != "" {
		return "`" + p + "`"
	}
	return "the root module"
}
