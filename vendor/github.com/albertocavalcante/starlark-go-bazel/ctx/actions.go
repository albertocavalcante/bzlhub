package ctx

import (
	"fmt"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
)

// ActionType represents the type of action.
// Source: StarlarkActionFactory.java categorizes actions as run, run_shell, write, etc.
type ActionType string

const (
	ActionTypeRun            ActionType = "run"
	ActionTypeRunShell       ActionType = "run_shell"
	ActionTypeWrite          ActionType = "write"
	ActionTypeSymlink        ActionType = "symlink"
	ActionTypeExpandTemplate ActionType = "expand_template"
	ActionTypeDoNothing      ActionType = "do_nothing"
)

// DeclaredAction represents a recorded action.
// Source: StarlarkActionFactory.java - actions are registered but in our mock
// implementation we just record them.
type DeclaredAction struct {
	Type                  ActionType
	Mnemonic              string
	ProgressMessage       string
	Outputs               []*File
	Inputs                []*File
	Tools                 []*File
	Executable            *File
	ExecutableString      string
	Arguments             []string
	Command               string // For run_shell
	Content               string // For write
	IsExecutable          bool
	Env                   map[string]string
	ExecutionRequirements map[string]string
	UseDefaultShellEnv    bool

	// For expand_template
	Template      *File
	Substitutions map[string]string

	// For symlink
	TargetFile *File
	TargetPath string
}

// Actions represents ctx.actions, the action factory.
// Source: StarlarkActionFactory.java
type Actions struct {
	ctx *Ctx

	// Declared actions are recorded here (mock implementation)
	declared []*DeclaredAction

	frozen bool
}

var (
	_ starlark.Value       = (*Actions)(nil)
	_ starlark.HasAttrs    = (*Actions)(nil)
	_ starlark.HasSetField = (*Actions)(nil)
)

// NewActions creates a new Actions factory.
func NewActions(ctx *Ctx) *Actions {
	return &Actions{
		ctx: ctx,
	}
}

// String returns the string representation.
func (a *Actions) String() string {
	return fmt.Sprintf("<actions for %s>", a.ctx.label.String())
}

// Type returns "actions".
func (a *Actions) Type() string { return "actions" }

// Freeze marks the actions as frozen.
func (a *Actions) Freeze() { a.frozen = true }

// Truth returns true.
func (a *Actions) Truth() starlark.Bool { return true }

// Hash returns an error.
func (a *Actions) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: actions")
}

// SetField is not supported but required for interface.
func (a *Actions) SetField(name string, val starlark.Value) error {
	return fmt.Errorf("cannot set field %q on actions", name)
}

// Attr returns an attribute (method) of actions.
// Source: StarlarkActionFactoryApi.java defines these methods
func (a *Actions) Attr(name string) (starlark.Value, error) {
	switch name {
	case "declare_file":
		return starlark.NewBuiltin("actions.declare_file", a.declareFile), nil
	case "declare_directory":
		return starlark.NewBuiltin("actions.declare_directory", a.declareDirectory), nil
	case "declare_symlink":
		return starlark.NewBuiltin("actions.declare_symlink", a.declareSymlink), nil
	case "do_nothing":
		return starlark.NewBuiltin("actions.do_nothing", a.doNothing), nil
	case "write":
		return starlark.NewBuiltin("actions.write", a.write), nil
	case "run":
		return starlark.NewBuiltin("actions.run", a.run), nil
	case "run_shell":
		return starlark.NewBuiltin("actions.run_shell", a.runShell), nil
	case "expand_template":
		return starlark.NewBuiltin("actions.expand_template", a.expandTemplate), nil
	case "symlink":
		return starlark.NewBuiltin("actions.symlink", a.symlink), nil
	case "args":
		return starlark.NewBuiltin("actions.args", a.args), nil
	case "template_dict":
		return starlark.NewBuiltin("actions.template_dict", a.templateDict), nil
	default:
		return nil, starlark.NoSuchAttrError(fmt.Sprintf("actions has no attribute %q", name))
	}
}

// AttrNames returns the list of attribute names.
func (a *Actions) AttrNames() []string {
	return []string{
		"args",
		"declare_directory",
		"declare_file",
		"declare_symlink",
		"do_nothing",
		"expand_template",
		"run",
		"run_shell",
		"symlink",
		"template_dict",
		"write",
	}
}

// DeclaredActions returns the list of declared actions.
func (a *Actions) DeclaredActions() []*DeclaredAction {
	return a.declared
}

// declareFile implements actions.declare_file(filename, sibling=None).
// Source: StarlarkActionFactory.declareFile()
func (a *Actions) declareFile(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var filename string
	var sibling starlark.Value = starlark.None

	if err := starlark.UnpackArgs("declare_file", args, kwargs,
		"filename", &filename,
		"sibling?", &sibling,
	); err != nil {
		return nil, err
	}

	var path string
	if sibling == starlark.None {
		// Path relative to package directory
		path = a.ctx.label.Pkg() + "/" + filename
	} else {
		// Sibling - use same directory as sibling
		siblingFile, ok := sibling.(*File)
		if !ok {
			return nil, fmt.Errorf("sibling must be a File, got %s", sibling.Type())
		}
		dir := siblingFile.path[:len(siblingFile.path)-len(siblingFile.Basename())]
		path = dir + filename
	}

	return NewDeclaredFile(path, a.ctx.binDir), nil
}

// declareDirectory implements actions.declare_directory(filename, sibling=None).
// Source: StarlarkActionFactory.declareDirectory()
func (a *Actions) declareDirectory(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var filename string
	var sibling starlark.Value = starlark.None

	if err := starlark.UnpackArgs("declare_directory", args, kwargs,
		"filename", &filename,
		"sibling?", &sibling,
	); err != nil {
		return nil, err
	}

	var path string
	if sibling == starlark.None {
		path = a.ctx.label.Pkg() + "/" + filename
	} else {
		siblingFile, ok := sibling.(*File)
		if !ok {
			return nil, fmt.Errorf("sibling must be a File, got %s", sibling.Type())
		}
		dir := siblingFile.path[:len(siblingFile.path)-len(siblingFile.Basename())]
		path = dir + filename
	}

	return NewDirectory(path, a.ctx.binDir), nil
}

// declareSymlink implements actions.declare_symlink(filename, sibling=None).
// Source: StarlarkActionFactory.declareSymlink()
func (a *Actions) declareSymlink(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var filename string
	var sibling starlark.Value = starlark.None

	if err := starlark.UnpackArgs("declare_symlink", args, kwargs,
		"filename", &filename,
		"sibling?", &sibling,
	); err != nil {
		return nil, err
	}

	var path string
	if sibling == starlark.None {
		path = a.ctx.label.Pkg() + "/" + filename
	} else {
		siblingFile, ok := sibling.(*File)
		if !ok {
			return nil, fmt.Errorf("sibling must be a File, got %s", sibling.Type())
		}
		dir := siblingFile.path[:len(siblingFile.path)-len(siblingFile.Basename())]
		path = dir + filename
	}

	return NewSymlink(path, a.ctx.binDir), nil
}

// doNothing implements actions.do_nothing(mnemonic, inputs).
// Source: StarlarkActionFactory.doNothing()
func (a *Actions) doNothing(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var mnemonic string
	var inputs starlark.Value = starlark.NewList(nil)

	if err := starlark.UnpackArgs("do_nothing", args, kwargs,
		"mnemonic", &mnemonic,
		"inputs?", &inputs,
	); err != nil {
		return nil, err
	}

	action := &DeclaredAction{
		Type:     ActionTypeDoNothing,
		Mnemonic: mnemonic,
		Inputs:   extractFiles(inputs),
	}
	a.declared = append(a.declared, action)

	return starlark.None, nil
}

// write implements actions.write(output, content, is_executable=False).
// Source: StarlarkActionFactory.write()
func (a *Actions) write(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var output starlark.Value
	var content starlark.Value
	var isExecutable bool
	var mnemonic starlark.Value = starlark.None

	if err := starlark.UnpackArgs("write", args, kwargs,
		"output", &output,
		"content", &content,
		"is_executable?", &isExecutable,
		"mnemonic?", &mnemonic,
	); err != nil {
		return nil, err
	}

	outputFile, ok := output.(*File)
	if !ok {
		return nil, fmt.Errorf("output must be a File, got %s", output.Type())
	}

	var contentStr string
	switch c := content.(type) {
	case starlark.String:
		contentStr = string(c)
	case *Args:
		// Convert Args to string representation
		contentStr = c.String()
	default:
		return nil, fmt.Errorf("content must be a string or Args, got %s", content.Type())
	}

	action := &DeclaredAction{
		Type:         ActionTypeWrite,
		Mnemonic:     "FileWrite",
		Outputs:      []*File{outputFile},
		Content:      contentStr,
		IsExecutable: isExecutable,
	}
	if mnemonic != starlark.None {
		action.Mnemonic = string(mnemonic.(starlark.String))
	}
	a.declared = append(a.declared, action)

	return starlark.None, nil
}

// run implements actions.run(...).
// Source: StarlarkActionFactory.run()
func (a *Actions) run(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var outputs starlark.Value
	var inputs starlark.Value = starlark.NewList(nil)
	var executable starlark.Value
	var tools starlark.Value = starlark.None
	var arguments starlark.Value = starlark.NewList(nil)
	var mnemonic starlark.Value = starlark.None
	var progressMessage starlark.Value = starlark.None
	var useDefaultShellEnv bool
	var env starlark.Value = starlark.None
	var executionRequirements starlark.Value = starlark.None

	if err := starlark.UnpackArgs("run", args, kwargs,
		"outputs", &outputs,
		"inputs?", &inputs,
		"executable", &executable,
		"tools?", &tools,
		"arguments?", &arguments,
		"mnemonic?", &mnemonic,
		"progress_message?", &progressMessage,
		"use_default_shell_env?", &useDefaultShellEnv,
		"env?", &env,
		"execution_requirements?", &executionRequirements,
	); err != nil {
		return nil, err
	}

	action := &DeclaredAction{
		Type:               ActionTypeRun,
		Mnemonic:           "Action",
		Outputs:            extractFiles(outputs),
		Inputs:             extractFiles(inputs),
		Tools:              extractFiles(tools),
		Arguments:          extractStrings(arguments),
		UseDefaultShellEnv: useDefaultShellEnv,
	}

	// Handle executable
	switch e := executable.(type) {
	case *File:
		action.Executable = e
	case starlark.String:
		action.ExecutableString = string(e)
	default:
		return nil, fmt.Errorf("executable must be a File or string, got %s", executable.Type())
	}

	if mnemonic != starlark.None {
		action.Mnemonic = string(mnemonic.(starlark.String))
	}
	if progressMessage != starlark.None {
		action.ProgressMessage = string(progressMessage.(starlark.String))
	}
	if env != starlark.None {
		action.Env = extractStringDict(env)
	}
	if executionRequirements != starlark.None {
		action.ExecutionRequirements = extractStringDict(executionRequirements)
	}

	a.declared = append(a.declared, action)
	return starlark.None, nil
}

// runShell implements actions.run_shell(...).
// Source: StarlarkActionFactory.runShell()
func (a *Actions) runShell(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var outputs starlark.Value
	var inputs starlark.Value = starlark.NewList(nil)
	var tools starlark.Value = starlark.None
	var arguments starlark.Value = starlark.NewList(nil)
	var mnemonic starlark.Value = starlark.None
	var command starlark.Value
	var progressMessage starlark.Value = starlark.None
	var useDefaultShellEnv bool
	var env starlark.Value = starlark.None
	var executionRequirements starlark.Value = starlark.None

	if err := starlark.UnpackArgs("run_shell", args, kwargs,
		"outputs", &outputs,
		"inputs?", &inputs,
		"tools?", &tools,
		"arguments?", &arguments,
		"mnemonic?", &mnemonic,
		"command", &command,
		"progress_message?", &progressMessage,
		"use_default_shell_env?", &useDefaultShellEnv,
		"env?", &env,
		"execution_requirements?", &executionRequirements,
	); err != nil {
		return nil, err
	}

	action := &DeclaredAction{
		Type:               ActionTypeRunShell,
		Mnemonic:           "Action",
		Outputs:            extractFiles(outputs),
		Inputs:             extractFiles(inputs),
		Tools:              extractFiles(tools),
		Arguments:          extractStrings(arguments),
		UseDefaultShellEnv: useDefaultShellEnv,
	}

	// Handle command
	switch c := command.(type) {
	case starlark.String:
		action.Command = string(c)
	case *starlark.List:
		// Deprecated: command as list of strings
		for i := range c.Len() {
			if s, ok := c.Index(i).(starlark.String); ok {
				action.Arguments = append(action.Arguments, string(s))
			}
		}
	default:
		return nil, fmt.Errorf("command must be a string, got %s", command.Type())
	}

	if mnemonic != starlark.None {
		action.Mnemonic = string(mnemonic.(starlark.String))
	}
	if progressMessage != starlark.None {
		action.ProgressMessage = string(progressMessage.(starlark.String))
	}
	if env != starlark.None {
		action.Env = extractStringDict(env)
	}
	if executionRequirements != starlark.None {
		action.ExecutionRequirements = extractStringDict(executionRequirements)
	}

	a.declared = append(a.declared, action)
	return starlark.None, nil
}

// expandTemplate implements actions.expand_template(...).
// Source: StarlarkActionFactory.expandTemplate()
func (a *Actions) expandTemplate(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var template starlark.Value
	var output starlark.Value
	var substitutions starlark.Value = starlark.NewDict(0)
	var isExecutable bool

	if err := starlark.UnpackArgs("expand_template", args, kwargs,
		"template", &template,
		"output", &output,
		"substitutions?", &substitutions,
		"is_executable?", &isExecutable,
	); err != nil {
		return nil, err
	}

	templateFile, ok := template.(*File)
	if !ok {
		return nil, fmt.Errorf("template must be a File, got %s", template.Type())
	}

	outputFile, ok := output.(*File)
	if !ok {
		return nil, fmt.Errorf("output must be a File, got %s", output.Type())
	}

	action := &DeclaredAction{
		Type:          ActionTypeExpandTemplate,
		Mnemonic:      "TemplateExpansion",
		Outputs:       []*File{outputFile},
		Inputs:        []*File{templateFile},
		Template:      templateFile,
		Substitutions: extractStringDict(substitutions),
		IsExecutable:  isExecutable,
	}
	a.declared = append(a.declared, action)

	return starlark.None, nil
}

// symlink implements actions.symlink(...).
// Source: StarlarkActionFactory.symlink()
func (a *Actions) symlink(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var output starlark.Value
	var targetFile starlark.Value = starlark.None
	var targetPath starlark.Value = starlark.None
	var isExecutable bool
	var progressMessage starlark.Value = starlark.None

	if err := starlark.UnpackArgs("symlink", args, kwargs,
		"output", &output,
		"target_file?", &targetFile,
		"target_path?", &targetPath,
		"is_executable?", &isExecutable,
		"progress_message?", &progressMessage,
	); err != nil {
		return nil, err
	}

	outputFile, ok := output.(*File)
	if !ok {
		return nil, fmt.Errorf("output must be a File, got %s", output.Type())
	}

	// Exactly one of target_file or target_path must be specified
	if (targetFile == starlark.None) == (targetPath == starlark.None) {
		return nil, fmt.Errorf("exactly one of target_file or target_path is required")
	}

	action := &DeclaredAction{
		Type:            ActionTypeSymlink,
		Mnemonic:        "Symlink",
		Outputs:         []*File{outputFile},
		IsExecutable:    isExecutable,
		ProgressMessage: "Creating symlink %{output}",
	}

	if targetFile != starlark.None {
		tf, ok := targetFile.(*File)
		if !ok {
			return nil, fmt.Errorf("target_file must be a File, got %s", targetFile.Type())
		}
		action.TargetFile = tf
		action.Inputs = []*File{tf}
	}
	if targetPath != starlark.None {
		action.TargetPath = string(targetPath.(starlark.String))
	}
	if progressMessage != starlark.None {
		action.ProgressMessage = string(progressMessage.(starlark.String))
	}

	a.declared = append(a.declared, action)
	return starlark.None, nil
}

// args implements actions.args().
// Source: StarlarkActionFactory.args()
func (a *Actions) args(_ *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
	return NewArgs(), nil
}

// templateDict implements actions.template_dict().
// Source: StarlarkActionFactory.templateDict()
func (a *Actions) templateDict(_ *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
	return NewTemplateDict(), nil
}

// Args represents an Args object for building command lines.
// Source: Args.java in Starlark
type Args struct {
	values      []string
	frozen      bool
	mapEach     starlark.Callable
	beforeEach  string
	joinWith    string
	formatEach  string
	uniquify    bool
	expandDirs  bool
	omitIfEmpty bool
}

var (
	_ starlark.Value    = (*Args)(nil)
	_ starlark.HasAttrs = (*Args)(nil)
)

// NewArgs creates a new Args object.
func NewArgs() *Args {
	return &Args{}
}

// String returns the string representation.
func (a *Args) String() string {
	return fmt.Sprintf("<Args: %d values>", len(a.values))
}

// Type returns "Args".
func (a *Args) Type() string { return "Args" }

// Freeze marks the args as frozen.
func (a *Args) Freeze() { a.frozen = true }

// Truth returns true.
func (a *Args) Truth() starlark.Bool { return true }

// Hash returns an error.
func (a *Args) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: Args")
}

// Attr returns an attribute of Args.
func (a *Args) Attr(name string) (starlark.Value, error) {
	switch name {
	case "add":
		return starlark.NewBuiltin("Args.add", a.add), nil
	case "add_all":
		return starlark.NewBuiltin("Args.add_all", a.addAll), nil
	case "add_joined":
		return starlark.NewBuiltin("Args.add_joined", a.addJoined), nil
	case "set_param_file_format":
		return starlark.NewBuiltin("Args.set_param_file_format", a.setParamFileFormat), nil
	case "use_param_file":
		return starlark.NewBuiltin("Args.use_param_file", a.useParamFile), nil
	default:
		return nil, starlark.NoSuchAttrError(fmt.Sprintf("Args has no attribute %q", name))
	}
}

// AttrNames returns the list of attribute names.
func (a *Args) AttrNames() []string {
	return []string{"add", "add_all", "add_joined", "set_param_file_format", "use_param_file"}
}

// Values returns the accumulated values.
func (a *Args) Values() []string {
	return a.values
}

// add implements Args.add().
func (a *Args) add(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	for _, arg := range args {
		switch v := arg.(type) {
		case starlark.String:
			a.values = append(a.values, string(v))
		case *File:
			a.values = append(a.values, v.Path())
		default:
			a.values = append(a.values, v.String())
		}
	}
	return a, nil
}

// addAll implements Args.add_all().
func (a *Args) addAll(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	for _, arg := range args {
		switch v := arg.(type) {
		case *starlark.List:
			for i := range v.Len() {
				elem := v.Index(i)
				switch e := elem.(type) {
				case starlark.String:
					a.values = append(a.values, string(e))
				case *File:
					a.values = append(a.values, e.Path())
				default:
					a.values = append(a.values, e.String())
				}
			}
		case starlark.String:
			// First positional can be a flag prefix
			a.values = append(a.values, string(v))
		}
	}
	return a, nil
}

// addJoined implements Args.add_joined().
func (a *Args) addJoined(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var joinWith string = ","
	for _, kv := range kwargs {
		key := string(kv[0].(starlark.String))
		if key == "join_with" {
			joinWith = string(kv[1].(starlark.String))
		}
	}
	_ = joinWith // TODO: implement joining
	return a.addAll(nil, nil, args, kwargs)
}

// setParamFileFormat implements Args.set_param_file_format().
func (a *Args) setParamFileFormat(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	// Mock implementation
	return a, nil
}

// useParamFile implements Args.use_param_file().
func (a *Args) useParamFile(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	// Mock implementation
	return a, nil
}

// TemplateDict represents a TemplateDict for expand_template.
// Source: TemplateDict in starlarkbuildapi
type TemplateDict struct {
	entries map[string]string
	frozen  bool
}

var (
	_ starlark.Value    = (*TemplateDict)(nil)
	_ starlark.HasAttrs = (*TemplateDict)(nil)
)

// NewTemplateDict creates a new TemplateDict.
func NewTemplateDict() *TemplateDict {
	return &TemplateDict{entries: make(map[string]string)}
}

// String returns the string representation.
func (t *TemplateDict) String() string {
	return fmt.Sprintf("<TemplateDict: %d entries>", len(t.entries))
}

// Type returns "TemplateDict".
func (t *TemplateDict) Type() string { return "TemplateDict" }

// Freeze marks the dict as frozen.
func (t *TemplateDict) Freeze() { t.frozen = true }

// Truth returns true.
func (t *TemplateDict) Truth() starlark.Bool { return true }

// Hash returns an error.
func (t *TemplateDict) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: TemplateDict")
}

// Attr returns an attribute.
func (t *TemplateDict) Attr(name string) (starlark.Value, error) {
	switch name {
	case "add":
		return starlark.NewBuiltin("TemplateDict.add", t.add), nil
	case "add_joined":
		return starlark.NewBuiltin("TemplateDict.add_joined", t.addJoined), nil
	default:
		return nil, starlark.NoSuchAttrError(fmt.Sprintf("TemplateDict has no attribute %q", name))
	}
}

// AttrNames returns the list of attribute names.
func (t *TemplateDict) AttrNames() []string {
	return []string{"add", "add_joined"}
}

// Entries returns the entries.
func (t *TemplateDict) Entries() map[string]string {
	return t.entries
}

// add implements TemplateDict.add().
func (t *TemplateDict) add(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var key, value string
	if err := starlark.UnpackArgs("add", args, kwargs, "key", &key, "value", &value); err != nil {
		return nil, err
	}
	t.entries[key] = value
	return t, nil
}

// addJoined implements TemplateDict.add_joined().
func (t *TemplateDict) addJoined(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	// Mock implementation
	return t, nil
}

// Helper functions

func extractFiles(v starlark.Value) []*File {
	var files []*File
	switch val := v.(type) {
	case *starlark.List:
		for i := range val.Len() {
			if f, ok := val.Index(i).(*File); ok {
				files = append(files, f)
			}
		}
	case *File:
		files = append(files, val)
	case *starlarkstruct.Struct:
		// Could be a depset or other struct
	}
	return files
}

func extractStrings(v starlark.Value) []string {
	var strings []string
	switch val := v.(type) {
	case *starlark.List:
		for i := range val.Len() {
			elem := val.Index(i)
			switch e := elem.(type) {
			case starlark.String:
				strings = append(strings, string(e))
			case *Args:
				strings = append(strings, e.Values()...)
			}
		}
	case starlark.String:
		strings = append(strings, string(val))
	}
	return strings
}

func extractStringDict(v starlark.Value) map[string]string {
	result := make(map[string]string)
	if d, ok := v.(*starlark.Dict); ok {
		for _, item := range d.Items() {
			if k, ok := item[0].(starlark.String); ok {
				if val, ok := item[1].(starlark.String); ok {
					result[string(k)] = string(val)
				}
			}
		}
	}
	return result
}
