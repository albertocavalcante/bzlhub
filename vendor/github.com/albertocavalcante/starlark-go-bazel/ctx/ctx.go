// Package ctx provides the Starlark rule context (ctx) object.
//
// This implements the ctx object passed to Bazel rule implementation functions.
// The implementation is based on Bazel's StarlarkRuleContext.java.
//
// Reference:
//   - StarlarkRuleContext.java (main ctx implementation)
//   - StarlarkActionFactory.java (ctx.actions)
//   - StarlarkAttributesCollection.java (ctx.attr, ctx.file, ctx.files, ctx.executable)
//
// Main ctx attributes (from StarlarkRuleContextApi.java):
//   - label: Label of the current target
//   - attr: Access to attribute values
//   - files: Files from label attributes
//   - file: Single file from label attributes (allow_single_file)
//   - executable: Executable files from label attributes (executable=True)
//   - outputs: Predeclared output files
//   - actions: Action factory
//   - bin_dir: Output bin directory
//   - genfiles_dir: Output genfiles directory
//   - workspace_name: Workspace name
//   - build_file_path: Path to BUILD file
//   - configuration: Build configuration
//   - fragments: Configuration fragments
//   - var: Make variables
//   - features: Enabled features
//   - disabled_features: Disabled features
//   - info_file: Non-volatile workspace status
//   - version_file: Volatile workspace status
//   - toolchains: Toolchain context
//   - exec_groups: Execution groups
//
// Main ctx methods:
//   - runfiles(): Create runfiles object
//   - expand_location(): Expand $(location) in strings
//   - expand_make_variables(): Expand $(VAR) patterns
//   - resolve_command(): Resolve command for execution
//   - resolve_tools(): Resolve tools for execution
//   - tokenize(): Tokenize shell command
//   - package_relative_label(): Convert string to Label in package context
//   - coverage_instrumented(): Check coverage instrumentation
package ctx

import (
	"fmt"
	"strings"

	"go.starlark.net/starlark"

	"github.com/albertocavalcante/starlark-go-bazel/types"
)

// Ctx represents the rule context (ctx) passed to rule implementation functions.
// Source: StarlarkRuleContext.java
type Ctx struct {
	// Core identity
	label *types.Label // ctx.label - Label of the current target

	// Attribute access (Source: StarlarkAttributesCollection)
	attr       *AttrProxy       // ctx.attr - attribute values
	files      *FilesProxy      // ctx.files - files from label attributes
	file       *FileProxy       // ctx.file - single file from label attrs
	executable *ExecutableProxy // ctx.executable - executable from label attrs
	outputs    *OutputsProxy    // ctx.outputs - predeclared outputs

	// Action factory (Source: StarlarkActionFactory)
	actions *Actions // ctx.actions - action factory

	// Directory roots (Source: StarlarkRuleContext.getBinDirectory/getGenfilesDirectory)
	binDir      string // ctx.bin_dir.path
	genfilesDir string // ctx.genfiles_dir.path

	// Workspace info
	workspaceName string // ctx.workspace_name
	buildFilePath string // ctx.build_file_path

	// Configuration (Source: StarlarkRuleContext.getConfiguration)
	features         []string          // ctx.features
	disabledFeatures []string          // ctx.disabled_features
	makeVariables    map[string]string // ctx.var

	// Status files
	infoFile    *File // ctx.info_file (non-volatile)
	versionFile *File // ctx.version_file (volatile)

	// For location expansion
	labelMap map[string][]*File

	// Rule metadata
	isExecutable bool // Whether this is an executable rule
	isTest       bool // Whether this is a test rule
	isForAspect  bool // Whether this ctx is for an aspect

	frozen bool
}

var (
	_ starlark.Value       = (*Ctx)(nil)
	_ starlark.HasAttrs    = (*Ctx)(nil)
	_ starlark.HasSetField = (*Ctx)(nil)
)

// CtxConfig holds configuration for creating a new Ctx.
type CtxConfig struct {
	Label            *types.Label
	WorkspaceName    string
	BinDir           string
	GenfilesDir      string
	BuildFilePath    string
	IsExecutable     bool
	IsTest           bool
	IsForAspect      bool
	Features         []string
	DisabledFeatures []string
	MakeVariables    map[string]string
}

// NewCtx creates a new Ctx.
func NewCtx(cfg CtxConfig) *Ctx {
	ctx := &Ctx{
		label:            cfg.Label,
		workspaceName:    cfg.WorkspaceName,
		binDir:           cfg.BinDir,
		genfilesDir:      cfg.GenfilesDir,
		buildFilePath:    cfg.BuildFilePath,
		isExecutable:     cfg.IsExecutable,
		isTest:           cfg.IsTest,
		isForAspect:      cfg.IsForAspect,
		features:         cfg.Features,
		disabledFeatures: cfg.DisabledFeatures,
		makeVariables:    cfg.MakeVariables,
		labelMap:         make(map[string][]*File),
	}

	// Initialize proxies
	ctx.attr = NewAttrProxy()
	ctx.files = NewFilesProxy()
	ctx.file = NewFileProxy()
	ctx.executable = NewExecutableProxy()
	ctx.outputs = NewOutputsProxy(cfg.IsExecutable || cfg.IsTest)
	ctx.actions = NewActions(ctx)

	return ctx
}

// String returns the string representation.
func (c *Ctx) String() string {
	if c.isForAspect {
		return fmt.Sprintf("<aspect context for %s>", c.label.String())
	}
	return fmt.Sprintf("<rule context for %s>", c.label.String())
}

// Type returns "ctx".
func (c *Ctx) Type() string { return "ctx" }

// Freeze marks the ctx as frozen.
func (c *Ctx) Freeze() {
	if c.frozen {
		return
	}
	c.frozen = true
	c.attr.Freeze()
	c.files.Freeze()
	c.file.Freeze()
	c.executable.Freeze()
	c.outputs.Freeze()
	c.actions.Freeze()
}

// Truth returns true.
func (c *Ctx) Truth() starlark.Bool { return true }

// Hash returns an error.
func (c *Ctx) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: ctx")
}

// SetField is not supported.
func (c *Ctx) SetField(name string, val starlark.Value) error {
	return fmt.Errorf("cannot set field %q on ctx", name)
}

// Attr returns an attribute or method of ctx.
// Source: StarlarkRuleContextApi.java defines all ctx attributes and methods
func (c *Ctx) Attr(name string) (starlark.Value, error) {
	if c.frozen {
		// After analysis, most fields become inaccessible
		// Source: StarlarkRuleContext.checkMutable()
	}

	switch name {
	// Core identity (StarlarkRuleContextApi.getLabel)
	case "label":
		return c.label, nil

	// Attribute access (StarlarkRuleContextApi.getAttr, getFiles, getFile, getExecutable)
	case "attr":
		return c.attr, nil
	case "files":
		return c.files, nil
	case "file":
		return c.file, nil
	case "executable":
		return c.executable, nil

	// Outputs (StarlarkRuleContextApi.outputs)
	case "outputs":
		if c.isForAspect {
			return nil, fmt.Errorf("'outputs' is not defined for aspects")
		}
		return c.outputs, nil

	// Actions (StarlarkRuleContextApi.actions)
	case "actions":
		return c.actions, nil

	// Directory roots (StarlarkRuleContextApi.getBinDirectory, getGenfilesDirectory)
	case "bin_dir":
		return NewFileRoot(c.binDir), nil
	case "genfiles_dir":
		return NewFileRoot(c.genfilesDir), nil

	// Workspace info (StarlarkRuleContextApi.getWorkspaceName, getBuildFileRelativePath)
	case "workspace_name":
		return starlark.String(c.workspaceName), nil
	case "build_file_path":
		return starlark.String(c.buildFilePath), nil

	// Configuration (StarlarkRuleContextApi.getFeatures, getDisabledFeatures)
	case "features":
		items := make([]starlark.Value, len(c.features))
		for i, f := range c.features {
			items[i] = starlark.String(f)
		}
		return starlark.NewList(items), nil
	case "disabled_features":
		items := make([]starlark.Value, len(c.disabledFeatures))
		for i, f := range c.disabledFeatures {
			items[i] = starlark.String(f)
		}
		return starlark.NewList(items), nil

	// Make variables (StarlarkRuleContextApi.var)
	case "var":
		d := starlark.NewDict(len(c.makeVariables))
		for k, v := range c.makeVariables {
			_ = d.SetKey(starlark.String(k), starlark.String(v))
		}
		return d, nil

	// Status files (StarlarkRuleContextApi.getStableWorkspaceStatus, getVolatileWorkspaceStatus)
	case "info_file":
		if c.infoFile != nil {
			return c.infoFile, nil
		}
		return starlark.None, nil
	case "version_file":
		if c.versionFile != nil {
			return c.versionFile, nil
		}
		return starlark.None, nil

	// Configuration object (simplified)
	case "configuration":
		return &Configuration{coverageEnabled: false}, nil

	// Fragments (simplified - returns empty struct)
	case "fragments":
		return &FragmentCollection{}, nil

	// Toolchains (simplified - returns empty struct)
	case "toolchains":
		return &ToolchainContext{}, nil

	// Exec groups (simplified)
	case "exec_groups":
		return &ExecGroupCollection{}, nil

	// Methods
	case "runfiles":
		return starlark.NewBuiltin("ctx.runfiles", c.runfilesMethod), nil
	case "expand_location":
		return starlark.NewBuiltin("ctx.expand_location", c.expandLocationMethod), nil
	case "expand_make_variables":
		return starlark.NewBuiltin("ctx.expand_make_variables", c.expandMakeVariablesMethod), nil
	case "resolve_command":
		return starlark.NewBuiltin("ctx.resolve_command", c.resolveCommandMethod), nil
	case "resolve_tools":
		return starlark.NewBuiltin("ctx.resolve_tools", c.resolveToolsMethod), nil
	case "tokenize":
		return starlark.NewBuiltin("ctx.tokenize", c.tokenizeMethod), nil
	case "package_relative_label":
		return starlark.NewBuiltin("ctx.package_relative_label", c.packageRelativeLabelMethod), nil
	case "coverage_instrumented":
		return starlark.NewBuiltin("ctx.coverage_instrumented", c.coverageInstrumentedMethod), nil

	// Aspect-only attributes
	case "rule":
		if !c.isForAspect {
			return nil, fmt.Errorf("'rule' is only available in aspect implementations")
		}
		return c.attr, nil // Simplified: return same attr
	case "aspect_ids":
		if !c.isForAspect {
			return nil, fmt.Errorf("'aspect_ids' is only available in aspect implementations")
		}
		return starlark.NewList(nil), nil

	// Experimental
	case "created_actions":
		// Returns None unless rule has _skylark_testable = True
		return starlark.None, nil
	case "build_setting_value":
		return nil, fmt.Errorf("'build_setting_value' is only available for build setting rules")

	default:
		return nil, starlark.NoSuchAttrError(fmt.Sprintf("ctx has no attribute %q", name))
	}
}

// AttrNames returns the list of attribute names.
func (c *Ctx) AttrNames() []string {
	names := []string{
		"actions",
		"attr",
		"bin_dir",
		"build_file_path",
		"configuration",
		"coverage_instrumented",
		"created_actions",
		"disabled_features",
		"exec_groups",
		"executable",
		"expand_location",
		"expand_make_variables",
		"features",
		"file",
		"files",
		"fragments",
		"genfiles_dir",
		"info_file",
		"label",
		"outputs",
		"package_relative_label",
		"resolve_command",
		"resolve_tools",
		"runfiles",
		"tokenize",
		"toolchains",
		"var",
		"version_file",
		"workspace_name",
	}
	if c.isForAspect {
		names = append(names, "aspect_ids", "rule")
	}
	return names
}

// runfilesMethod implements ctx.runfiles().
// Source: StarlarkRuleContext.runfiles()
func (c *Ctx) runfilesMethod(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var files starlark.Value = starlark.NewList(nil)
	var transitiveFiles starlark.Value = starlark.None
	var collectData bool
	var collectDefault bool
	var symlinks starlark.Value = starlark.NewDict(0)
	var rootSymlinks starlark.Value = starlark.NewDict(0)

	if err := starlark.UnpackArgs("runfiles", args, kwargs,
		"files?", &files,
		"transitive_files?", &transitiveFiles,
		"collect_data?", &collectData,
		"collect_default?", &collectDefault,
		"symlinks?", &symlinks,
		"root_symlinks?", &rootSymlinks,
	); err != nil {
		return nil, err
	}

	rf := NewRunfiles()

	// Add files
	if list, ok := files.(*starlark.List); ok {
		for i := range list.Len() {
			if f, ok := list.Index(i).(*File); ok {
				rf.AddFile(f)
			}
		}
	}

	// Add transitive files (simplified - just add directly)
	if transitiveFiles != starlark.None {
		// In real Bazel this would be a depset
		if list, ok := transitiveFiles.(*starlark.List); ok {
			for i := range list.Len() {
				if f, ok := list.Index(i).(*File); ok {
					rf.AddTransitiveFile(f)
				}
			}
		}
	}

	// Add symlinks
	if d, ok := symlinks.(*starlark.Dict); ok {
		for _, item := range d.Items() {
			if path, ok := item[0].(starlark.String); ok {
				if f, ok := item[1].(*File); ok {
					rf.AddSymlink(string(path), f)
				}
			}
		}
	}

	// Add root symlinks
	if d, ok := rootSymlinks.(*starlark.Dict); ok {
		for _, item := range d.Items() {
			if path, ok := item[0].(starlark.String); ok {
				if f, ok := item[1].(*File); ok {
					rf.AddRootSymlink(string(path), f)
				}
			}
		}
	}

	return rf, nil
}

// expandLocationMethod implements ctx.expand_location().
// Source: StarlarkRuleContext.expandLocation()
func (c *Ctx) expandLocationMethod(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var input string
	var targets starlark.Value = starlark.NewList(nil)

	if err := starlark.UnpackArgs("expand_location", args, kwargs,
		"input", &input,
		"targets?", &targets,
	); err != nil {
		return nil, err
	}

	// Build label map from targets
	labelMap := make(map[string][]*File)
	if list, ok := targets.(*starlark.List); ok {
		for i := range list.Len() {
			if t, ok := list.Index(i).(*TargetProxy); ok {
				labelMap[t.Label().String()] = t.Files()
			}
		}
	}

	// Merge with ctx's label map
	for k, v := range c.labelMap {
		if _, ok := labelMap[k]; !ok {
			labelMap[k] = v
		}
	}

	result, err := expandLocation(input, labelMap)
	if err != nil {
		return nil, err
	}
	return starlark.String(result), nil
}

// expandMakeVariablesMethod implements ctx.expand_make_variables().
// Source: StarlarkRuleContext.expandMakeVariables()
func (c *Ctx) expandMakeVariablesMethod(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var attrName string
	var command string
	var additionalSubstitutions starlark.Value

	if err := starlark.UnpackArgs("expand_make_variables", args, kwargs,
		"attribute_name", &attrName,
		"command", &command,
		"additional_substitutions", &additionalSubstitutions,
	); err != nil {
		return nil, err
	}

	result := command

	// First apply additional substitutions
	if d, ok := additionalSubstitutions.(*starlark.Dict); ok {
		for _, item := range d.Items() {
			if k, ok := item[0].(starlark.String); ok {
				if v, ok := item[1].(starlark.String); ok {
					pattern := "$(" + string(k) + ")"
					result = strings.ReplaceAll(result, pattern, string(v))
				}
			}
		}
	}

	// Then apply ctx.var
	for k, v := range c.makeVariables {
		pattern := "$(" + k + ")"
		result = strings.ReplaceAll(result, pattern, v)
	}

	// Handle $$ -> $
	result = strings.ReplaceAll(result, "$$", "$")

	return starlark.String(result), nil
}

// resolveCommandMethod implements ctx.resolve_command().
// Source: StarlarkRuleContext.resolveCommand()
func (c *Ctx) resolveCommandMethod(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var command string

	if err := starlark.UnpackArgs("resolve_command", args, kwargs,
		"command?", &command,
	); err != nil {
		return nil, err
	}

	// Returns tuple (inputs, argv, empty list)
	return starlark.Tuple{
		starlark.NewList(nil),
		starlark.NewList([]starlark.Value{starlark.String("/bin/bash"), starlark.String("-c"), starlark.String(command)}),
		starlark.NewList(nil),
	}, nil
}

// resolveToolsMethod implements ctx.resolve_tools().
// Source: StarlarkRuleContext.resolveTools()
func (c *Ctx) resolveToolsMethod(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	// Returns tuple (inputs depset, empty list)
	return starlark.Tuple{
		starlark.NewList(nil), // Simplified: should be a depset
		starlark.NewList(nil),
	}, nil
}

// tokenizeMethod implements ctx.tokenize().
// Source: StarlarkRuleContext.tokenize()
func (c *Ctx) tokenizeMethod(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var optionString string
	if err := starlark.UnpackArgs("tokenize", args, kwargs, "option", &optionString); err != nil {
		return nil, err
	}

	// Simple shell tokenization (not as sophisticated as Bazel's)
	tokens := tokenizeShell(optionString)
	items := make([]starlark.Value, len(tokens))
	for i, t := range tokens {
		items[i] = starlark.String(t)
	}
	return starlark.NewList(items), nil
}

// packageRelativeLabelMethod implements ctx.package_relative_label().
// Source: StarlarkRuleContext.packageRelativeLabel()
func (c *Ctx) packageRelativeLabelMethod(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var input starlark.Value
	if err := starlark.UnpackArgs("package_relative_label", args, kwargs, "input", &input); err != nil {
		return nil, err
	}

	// If already a Label, return it
	if l, ok := input.(*types.Label); ok {
		return l, nil
	}

	// Parse string as label in package context
	s, ok := input.(starlark.String)
	if !ok {
		return nil, fmt.Errorf("expected string or Label, got %s", input.Type())
	}

	labelStr := string(s)

	// Handle relative labels
	if !strings.HasPrefix(labelStr, "//") && !strings.HasPrefix(labelStr, "@") {
		if strings.HasPrefix(labelStr, ":") {
			labelStr = "//" + c.label.Pkg() + labelStr
		} else {
			labelStr = "//" + c.label.Pkg() + ":" + labelStr
		}
	}

	return types.ParseLabel(labelStr)
}

// coverageInstrumentedMethod implements ctx.coverage_instrumented().
// Source: StarlarkRuleContext.instrumentCoverage()
func (c *Ctx) coverageInstrumentedMethod(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	// Simplified: always return false
	return starlark.False, nil
}

// Accessors

// Label returns the ctx's label.
func (c *Ctx) Label() *types.Label { return c.label }

// Actions returns the ctx's actions factory.
func (c *Ctx) Actions() *Actions { return c.actions }

// AttrProxy returns the ctx's attr proxy.
func (c *Ctx) AttrProxy() *AttrProxy { return c.attr }

// FilesProxy returns the ctx's files proxy.
func (c *Ctx) FilesProxy() *FilesProxy { return c.files }

// FileProxy returns the ctx's file proxy.
func (c *Ctx) FileProxy() *FileProxy { return c.file }

// ExecutableProxy returns the ctx's executable proxy.
func (c *Ctx) ExecutableProxy() *ExecutableProxy { return c.executable }

// OutputsProxy returns the ctx's outputs proxy.
func (c *Ctx) OutputsProxy() *OutputsProxy { return c.outputs }

// SetLabelMap sets the label map for location expansion.
func (c *Ctx) SetLabelMap(m map[string][]*File) {
	c.labelMap = m
}

// SetInfoFile sets the info file.
func (c *Ctx) SetInfoFile(f *File) { c.infoFile = f }

// SetVersionFile sets the version file.
func (c *Ctx) SetVersionFile(f *File) { c.versionFile = f }

// Helper: simple shell tokenizer
func tokenizeShell(s string) []string {
	var tokens []string
	var current strings.Builder
	inQuote := false
	quoteChar := rune(0)

	for _, r := range s {
		switch {
		case r == '"' || r == '\'':
			if inQuote && r == quoteChar {
				inQuote = false
				quoteChar = 0
			} else if !inQuote {
				inQuote = true
				quoteChar = r
			} else {
				current.WriteRune(r)
			}
		case r == ' ' || r == '\t':
			if inQuote {
				current.WriteRune(r)
			} else if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}

	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}

	return tokens
}

// Configuration represents ctx.configuration (simplified).
// Source: BuildConfigurationValue
type Configuration struct {
	coverageEnabled bool
	frozen          bool
}

var _ starlark.Value = (*Configuration)(nil)
var _ starlark.HasAttrs = (*Configuration)(nil)

func (c *Configuration) String() string        { return "<configuration>" }
func (c *Configuration) Type() string          { return "configuration" }
func (c *Configuration) Freeze()               { c.frozen = true }
func (c *Configuration) Truth() starlark.Bool  { return true }
func (c *Configuration) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable: configuration") }

func (c *Configuration) Attr(name string) (starlark.Value, error) {
	switch name {
	case "coverage_enabled":
		return starlark.Bool(c.coverageEnabled), nil
	default:
		return nil, starlark.NoSuchAttrError(fmt.Sprintf("configuration has no attribute %q", name))
	}
}

func (c *Configuration) AttrNames() []string {
	return []string{"coverage_enabled"}
}

// FragmentCollection represents ctx.fragments (simplified).
// Source: FragmentCollection.java
type FragmentCollection struct {
	frozen bool
}

var _ starlark.Value = (*FragmentCollection)(nil)
var _ starlark.HasAttrs = (*FragmentCollection)(nil)

func (f *FragmentCollection) String() string        { return "<fragments>" }
func (f *FragmentCollection) Type() string          { return "fragments" }
func (f *FragmentCollection) Freeze()               { f.frozen = true }
func (f *FragmentCollection) Truth() starlark.Bool  { return true }
func (f *FragmentCollection) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable: fragments") }

func (f *FragmentCollection) Attr(name string) (starlark.Value, error) {
	// Return empty struct for any fragment
	return &emptyStruct{}, nil
}

func (f *FragmentCollection) AttrNames() []string {
	return []string{}
}

// ToolchainContext represents ctx.toolchains (simplified).
// Source: StarlarkToolchainContext
type ToolchainContext struct {
	frozen bool
}

var _ starlark.Value = (*ToolchainContext)(nil)
var _ starlark.HasAttrs = (*ToolchainContext)(nil)
var _ starlark.Mapping = (*ToolchainContext)(nil)

func (t *ToolchainContext) String() string        { return "<toolchains>" }
func (t *ToolchainContext) Type() string          { return "toolchains" }
func (t *ToolchainContext) Freeze()               { t.frozen = true }
func (t *ToolchainContext) Truth() starlark.Bool  { return true }
func (t *ToolchainContext) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable: toolchains") }

func (t *ToolchainContext) Attr(name string) (starlark.Value, error) {
	return nil, starlark.NoSuchAttrError(fmt.Sprintf("toolchains has no attribute %q", name))
}

func (t *ToolchainContext) AttrNames() []string {
	return []string{}
}

func (t *ToolchainContext) Get(key starlark.Value) (v starlark.Value, found bool, err error) {
	// Return None for any toolchain lookup
	return starlark.None, true, nil
}

// ExecGroupCollection represents ctx.exec_groups (simplified).
// Source: StarlarkExecGroupCollection
type ExecGroupCollection struct {
	frozen bool
}

var _ starlark.Value = (*ExecGroupCollection)(nil)
var _ starlark.HasAttrs = (*ExecGroupCollection)(nil)
var _ starlark.Mapping = (*ExecGroupCollection)(nil)

func (e *ExecGroupCollection) String() string        { return "<exec_groups>" }
func (e *ExecGroupCollection) Type() string          { return "exec_groups" }
func (e *ExecGroupCollection) Freeze()               { e.frozen = true }
func (e *ExecGroupCollection) Truth() starlark.Bool  { return true }
func (e *ExecGroupCollection) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable: exec_groups") }

func (e *ExecGroupCollection) Attr(name string) (starlark.Value, error) {
	return nil, starlark.NoSuchAttrError(fmt.Sprintf("exec_groups has no attribute %q", name))
}

func (e *ExecGroupCollection) AttrNames() []string {
	return []string{}
}

func (e *ExecGroupCollection) Get(key starlark.Value) (v starlark.Value, found bool, err error) {
	// Return empty exec group for any lookup
	return &emptyStruct{}, true, nil
}

// emptyStruct is a helper for empty struct values.
type emptyStruct struct {
	frozen bool
}

var _ starlark.Value = (*emptyStruct)(nil)
var _ starlark.HasAttrs = (*emptyStruct)(nil)

func (e *emptyStruct) String() string        { return "struct()" }
func (e *emptyStruct) Type() string          { return "struct" }
func (e *emptyStruct) Freeze()               { e.frozen = true }
func (e *emptyStruct) Truth() starlark.Bool  { return true }
func (e *emptyStruct) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable: struct") }

func (e *emptyStruct) Attr(name string) (starlark.Value, error) {
	return nil, starlark.NoSuchAttrError(fmt.Sprintf("struct has no attribute %q", name))
}

func (e *emptyStruct) AttrNames() []string {
	return []string{}
}
