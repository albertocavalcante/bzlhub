package ctx

import (
	"fmt"
	"sort"

	"go.starlark.net/starlark"

	"github.com/albertocavalcante/starlark-go-bazel/types"
)

// AttrProxy provides access to ctx.attr - the attribute values.
// Source: StarlarkRuleContext.getAttr() returns a StructImpl containing
// the attribute values. StarlarkAttributesCollection builds this.
type AttrProxy struct {
	values map[string]starlark.Value
	frozen bool
}

var (
	_ starlark.Value    = (*AttrProxy)(nil)
	_ starlark.HasAttrs = (*AttrProxy)(nil)
)

// NewAttrProxy creates a new AttrProxy.
func NewAttrProxy() *AttrProxy {
	return &AttrProxy{
		values: make(map[string]starlark.Value),
	}
}

// String returns the string representation.
func (a *AttrProxy) String() string {
	return "<ctx.attr>"
}

// Type returns "struct" (like Bazel's ctx.attr).
func (a *AttrProxy) Type() string { return "struct" }

// Freeze marks the proxy as frozen.
func (a *AttrProxy) Freeze() {
	if a.frozen {
		return
	}
	a.frozen = true
	for _, v := range a.values {
		v.Freeze()
	}
}

// Truth returns true.
func (a *AttrProxy) Truth() starlark.Bool { return true }

// Hash returns an error.
func (a *AttrProxy) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: struct")
}

// Attr returns an attribute value.
func (a *AttrProxy) Attr(name string) (starlark.Value, error) {
	if v, ok := a.values[name]; ok {
		return v, nil
	}
	return nil, starlark.NoSuchAttrError(fmt.Sprintf("struct has no attribute %q", name))
}

// AttrNames returns the list of attribute names.
func (a *AttrProxy) AttrNames() []string {
	names := make([]string, 0, len(a.values))
	for k := range a.values {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// Set sets an attribute value.
func (a *AttrProxy) Set(name string, value starlark.Value) {
	a.values[name] = value
}

// Get gets an attribute value.
func (a *AttrProxy) Get(name string) (starlark.Value, bool) {
	v, ok := a.values[name]
	return v, ok
}

// FilesProxy provides access to ctx.files - files from label attributes.
// Source: StarlarkRuleContext.getFiles() returns a StructImpl where
// each attribute name maps to a list of File objects.
type FilesProxy struct {
	values map[string][]*File
	frozen bool
}

var (
	_ starlark.Value    = (*FilesProxy)(nil)
	_ starlark.HasAttrs = (*FilesProxy)(nil)
)

// NewFilesProxy creates a new FilesProxy.
func NewFilesProxy() *FilesProxy {
	return &FilesProxy{
		values: make(map[string][]*File),
	}
}

// String returns the string representation.
func (f *FilesProxy) String() string {
	return "<ctx.files>"
}

// Type returns "struct".
func (f *FilesProxy) Type() string { return "struct" }

// Freeze marks the proxy as frozen.
func (f *FilesProxy) Freeze() { f.frozen = true }

// Truth returns true.
func (f *FilesProxy) Truth() starlark.Bool { return true }

// Hash returns an error.
func (f *FilesProxy) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: struct")
}

// Attr returns the files for an attribute.
func (f *FilesProxy) Attr(name string) (starlark.Value, error) {
	if files, ok := f.values[name]; ok {
		items := make([]starlark.Value, len(files))
		for i, file := range files {
			items[i] = file
		}
		return starlark.NewList(items), nil
	}
	return nil, starlark.NoSuchAttrError(fmt.Sprintf("struct has no attribute %q", name))
}

// AttrNames returns the list of attribute names.
func (f *FilesProxy) AttrNames() []string {
	names := make([]string, 0, len(f.values))
	for k := range f.values {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// Set sets files for an attribute.
func (f *FilesProxy) Set(name string, files []*File) {
	f.values[name] = files
}

// FileProxy provides access to ctx.file - single file from label attributes.
// Source: StarlarkRuleContext.getFile() returns a StructImpl where
// each attribute marked allow_single_file maps to a single File or None.
type FileProxy struct {
	values map[string]*File
	frozen bool
}

var (
	_ starlark.Value    = (*FileProxy)(nil)
	_ starlark.HasAttrs = (*FileProxy)(nil)
)

// NewFileProxy creates a new FileProxy.
func NewFileProxy() *FileProxy {
	return &FileProxy{
		values: make(map[string]*File),
	}
}

// String returns the string representation.
func (f *FileProxy) String() string {
	return "<ctx.file>"
}

// Type returns "struct".
func (f *FileProxy) Type() string { return "struct" }

// Freeze marks the proxy as frozen.
func (f *FileProxy) Freeze() { f.frozen = true }

// Truth returns true.
func (f *FileProxy) Truth() starlark.Bool { return true }

// Hash returns an error.
func (f *FileProxy) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: struct")
}

// Attr returns the file for an attribute.
func (f *FileProxy) Attr(name string) (starlark.Value, error) {
	if file, ok := f.values[name]; ok {
		if file == nil {
			return starlark.None, nil
		}
		return file, nil
	}
	return nil, starlark.NoSuchAttrError(fmt.Sprintf("struct has no attribute %q", name))
}

// AttrNames returns the list of attribute names.
func (f *FileProxy) AttrNames() []string {
	names := make([]string, 0, len(f.values))
	for k := range f.values {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// Set sets file for an attribute.
func (f *FileProxy) Set(name string, file *File) {
	f.values[name] = file
}

// ExecutableProxy provides access to ctx.executable - executable files.
// Source: StarlarkRuleContext.getExecutable() returns executables
// from attributes marked executable=True.
type ExecutableProxy struct {
	values map[string]*File
	frozen bool
}

var (
	_ starlark.Value    = (*ExecutableProxy)(nil)
	_ starlark.HasAttrs = (*ExecutableProxy)(nil)
)

// NewExecutableProxy creates a new ExecutableProxy.
func NewExecutableProxy() *ExecutableProxy {
	return &ExecutableProxy{
		values: make(map[string]*File),
	}
}

// String returns the string representation.
func (e *ExecutableProxy) String() string {
	return "<ctx.executable>"
}

// Type returns "struct".
func (e *ExecutableProxy) Type() string { return "struct" }

// Freeze marks the proxy as frozen.
func (e *ExecutableProxy) Freeze() { e.frozen = true }

// Truth returns true.
func (e *ExecutableProxy) Truth() starlark.Bool { return true }

// Hash returns an error.
func (e *ExecutableProxy) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: struct")
}

// Attr returns the executable for an attribute.
func (e *ExecutableProxy) Attr(name string) (starlark.Value, error) {
	if file, ok := e.values[name]; ok {
		if file == nil {
			return starlark.None, nil
		}
		return file, nil
	}
	return nil, starlark.NoSuchAttrError(fmt.Sprintf("struct has no attribute %q", name))
}

// AttrNames returns the list of attribute names.
func (e *ExecutableProxy) AttrNames() []string {
	names := make([]string, 0, len(e.values))
	for k := range e.values {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// Set sets executable for an attribute.
func (e *ExecutableProxy) Set(name string, file *File) {
	e.values[name] = file
}

// OutputsProxy provides access to ctx.outputs - predeclared output files.
// Source: StarlarkRuleContext.outputs() returns the Outputs struct containing
// predeclared outputs from output attributes and the outputs dict.
type OutputsProxy struct {
	values       map[string]starlark.Value
	executable   *File // For ctx.outputs.executable (deprecated)
	isExecutable bool  // Whether the rule is executable
	frozen       bool
}

var (
	_ starlark.Value    = (*OutputsProxy)(nil)
	_ starlark.HasAttrs = (*OutputsProxy)(nil)
)

// NewOutputsProxy creates a new OutputsProxy.
func NewOutputsProxy(isExecutable bool) *OutputsProxy {
	return &OutputsProxy{
		values:       make(map[string]starlark.Value),
		isExecutable: isExecutable,
	}
}

// String returns the string representation.
func (o *OutputsProxy) String() string {
	return "<ctx.outputs>"
}

// Type returns "ctx.outputs".
func (o *OutputsProxy) Type() string { return "ctx.outputs" }

// Freeze marks the proxy as frozen.
func (o *OutputsProxy) Freeze() { o.frozen = true }

// Truth returns true.
func (o *OutputsProxy) Truth() starlark.Bool { return true }

// Hash returns an error.
func (o *OutputsProxy) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: ctx.outputs")
}

// Attr returns an output.
func (o *OutputsProxy) Attr(name string) (starlark.Value, error) {
	// Special case: executable output for executable/test rules
	if name == "executable" && o.isExecutable {
		if o.executable == nil {
			return starlark.None, nil
		}
		return o.executable, nil
	}

	if v, ok := o.values[name]; ok {
		return v, nil
	}
	return nil, starlark.NoSuchAttrError(
		fmt.Sprintf("No attribute '%s' in outputs. Make sure you declared a rule output with this name.", name))
}

// AttrNames returns the list of output names.
func (o *OutputsProxy) AttrNames() []string {
	names := make([]string, 0, len(o.values))
	for k := range o.values {
		names = append(names, k)
	}
	if o.isExecutable && o.executable != nil {
		names = append(names, "executable")
	}
	sort.Strings(names)
	return names
}

// Set sets an output.
func (o *OutputsProxy) Set(name string, value starlark.Value) {
	o.values[name] = value
}

// SetExecutable sets the executable output.
func (o *OutputsProxy) SetExecutable(file *File) {
	o.executable = file
}

// TargetProxy wraps a target for ctx.attr dependencies.
// Source: TransitiveInfoCollection provides access to target data.
type TargetProxy struct {
	label     *types.Label
	files     []*File
	providers map[*types.Provider]*types.ProviderInstance
	frozen    bool
}

var (
	_ starlark.Value     = (*TargetProxy)(nil)
	_ starlark.HasAttrs  = (*TargetProxy)(nil)
	_ starlark.Indexable = (*TargetProxy)(nil)
)

// NewTargetProxy creates a new TargetProxy.
func NewTargetProxy(label *types.Label) *TargetProxy {
	return &TargetProxy{
		label:     label,
		providers: make(map[*types.Provider]*types.ProviderInstance),
	}
}

// String returns the string representation.
func (t *TargetProxy) String() string {
	return fmt.Sprintf("<target %s>", t.label.String())
}

// Type returns "Target".
func (t *TargetProxy) Type() string { return "Target" }

// Freeze marks the proxy as frozen.
func (t *TargetProxy) Freeze() { t.frozen = true }

// Truth returns true.
func (t *TargetProxy) Truth() starlark.Bool { return true }

// Hash returns an error.
func (t *TargetProxy) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: Target")
}

// Attr returns an attribute of the target.
func (t *TargetProxy) Attr(name string) (starlark.Value, error) {
	switch name {
	case "label":
		return t.label, nil
	case "files":
		// Return a depset of files
		items := make([]starlark.Value, len(t.files))
		for i, f := range t.files {
			items[i] = f
		}
		return starlark.NewList(items), nil
	default:
		return nil, starlark.NoSuchAttrError(fmt.Sprintf("Target has no attribute %q", name))
	}
}

// AttrNames returns the list of attribute names.
func (t *TargetProxy) AttrNames() []string {
	return []string{"files", "label"}
}

// Index implements indexing target[provider].
func (t *TargetProxy) Index(i int) starlark.Value {
	// Not used for indexing by integer
	return starlark.None
}

// Len returns 0 (not a sequence).
func (t *TargetProxy) Len() int {
	return 0
}

// SetFiles sets the files for this target.
func (t *TargetProxy) SetFiles(files []*File) {
	t.files = files
}

// AddProvider adds a provider to this target.
func (t *TargetProxy) AddProvider(provider *types.Provider, instance *types.ProviderInstance) {
	t.providers[provider] = instance
}

// GetProvider gets a provider from this target.
func (t *TargetProxy) GetProvider(provider *types.Provider) (*types.ProviderInstance, bool) {
	pi, ok := t.providers[provider]
	return pi, ok
}

// Label returns the target's label.
func (t *TargetProxy) Label() *types.Label {
	return t.label
}

// Files returns the target's files.
func (t *TargetProxy) Files() []*File {
	return t.files
}
