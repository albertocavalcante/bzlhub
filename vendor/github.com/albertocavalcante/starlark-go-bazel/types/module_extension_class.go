package types

import (
	"fmt"

	"go.starlark.net/starlark"
)

// ModuleExtensionClass is the captured definition of a
// module_extension(). The implementation drives the bzlmod extension
// evaluation; tag_classes declare the named tag instances callers
// can declare in MODULE.bazel via use_extension(...).<tag>(...).
//
// Reference: bazel/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/ModuleExtension.java
type ModuleExtensionClass struct {
	name           string
	implementation starlark.Callable
	tagClasses     map[string]*TagClass
	doc            string
	environ        []string // Deprecated kwarg per Bazel docs.
	osDependent    bool     // Bazel 7+
	archDependent  bool     // Bazel 7+
	frozen         bool
}

var _ starlark.Value = (*ModuleExtensionClass)(nil)

func NewModuleExtensionClass(impl starlark.Callable, tagClasses map[string]*TagClass, opts ...ModuleExtensionOption) *ModuleExtensionClass {
	m := &ModuleExtensionClass{implementation: impl, tagClasses: tagClasses}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

type ModuleExtensionOption func(*ModuleExtensionClass)

func WithExtDoc(d string) ModuleExtensionOption    { return func(m *ModuleExtensionClass) { m.doc = d } }
func WithExtEnviron(e []string) ModuleExtensionOption { return func(m *ModuleExtensionClass) { m.environ = e } }
func WithOsDependent(v bool) ModuleExtensionOption { return func(m *ModuleExtensionClass) { m.osDependent = v } }
func WithArchDependent(v bool) ModuleExtensionOption { return func(m *ModuleExtensionClass) { m.archDependent = v } }

func (m *ModuleExtensionClass) String() string {
	if m.name != "" {
		return fmt.Sprintf("<module_extension %s>", m.name)
	}
	return "<module_extension>"
}

func (m *ModuleExtensionClass) Type() string             { return "module_extension" }
func (m *ModuleExtensionClass) Freeze()                  { m.frozen = true }
func (m *ModuleExtensionClass) Truth() starlark.Bool     { return true }
func (m *ModuleExtensionClass) Hash() (uint32, error)    { return 0, fmt.Errorf("unhashable: module_extension") }
func (m *ModuleExtensionClass) Name() string             { return m.name }
func (m *ModuleExtensionClass) SetName(name string)      { m.name = name }
func (m *ModuleExtensionClass) Implementation() starlark.Callable { return m.implementation }
func (m *ModuleExtensionClass) TagClasses() map[string]*TagClass  { return m.tagClasses }
func (m *ModuleExtensionClass) Doc() string              { return m.doc }
func (m *ModuleExtensionClass) Environ() []string        { return m.environ }
func (m *ModuleExtensionClass) OsDependent() bool        { return m.osDependent }
func (m *ModuleExtensionClass) ArchDependent() bool      { return m.archDependent }
