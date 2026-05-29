// Package ctx provides the Starlark rule context (ctx) object for Bazel rule implementations.
//
// This package implements a mock/recording version of Bazel's ctx object, suitable for
// static analysis and testing of Starlark rule implementations without executing a full build.
//
// # Bazel Source References
//
// All implementations are derived from the actual Bazel source code:
//
// ## ctx (StarlarkRuleContext)
// Source: com.google.devtools.build.lib.analysis.starlark.StarlarkRuleContext.java
// API:    com.google.devtools.build.lib.starlarkbuildapi.StarlarkRuleContextApi.java
//
// ## ctx.actions (StarlarkActionFactory)
// Source: com.google.devtools.build.lib.analysis.starlark.StarlarkActionFactory.java
// API:    com.google.devtools.build.lib.starlarkbuildapi.StarlarkActionFactoryApi.java
//
// ## ctx.attr, ctx.files, ctx.file, ctx.executable (StarlarkAttributesCollection)
// Source: com.google.devtools.build.lib.analysis.starlark.StarlarkAttributesCollection.java
//
// ## File (Artifact)
// Source: com.google.devtools.build.lib.actions.Artifact.java
// API:    com.google.devtools.build.lib.starlarkbuildapi.FileApi.java
//
// ## FileRoot (ArtifactRoot)
// Source: com.google.devtools.build.lib.actions.ArtifactRoot.java
// API:    com.google.devtools.build.lib.starlarkbuildapi.FileRootApi.java
//
// ## Runfiles
// Source: com.google.devtools.build.lib.analysis.Runfiles.java
// API:    com.google.devtools.build.lib.starlarkbuildapi.RunfilesApi.java
//
// ## Args (command line arguments)
// Source: com.google.devtools.build.lib.analysis.starlark.Args.java
// API:    com.google.devtools.build.lib.starlarkbuildapi.CommandLineArgsApi.java
//
// ## TemplateDict
// Source: com.google.devtools.build.lib.analysis.starlark.TemplateDict.java
// API:    com.google.devtools.build.lib.starlarkbuildapi.TemplateDictApi.java
//
// # Implemented ctx Attributes
//
// The following ctx attributes are implemented (from StarlarkRuleContextApi.java):
//
//   - label: Label of the current target (getLabel)
//   - attr: Struct of attribute values (getAttr)
//   - files: Files from label/label_list attributes (getFiles)
//   - file: Single file from allow_single_file attributes (getFile)
//   - executable: Executable files from executable=True attributes (getExecutable)
//   - outputs: Predeclared output files (outputs)
//   - actions: Action factory for declaring actions (actions)
//   - bin_dir: Root for binary outputs (getBinDirectory)
//   - genfiles_dir: Root for genfiles outputs (getGenfilesDirectory)
//   - workspace_name: Name of the workspace (getWorkspaceName)
//   - build_file_path: Path to BUILD file (getBuildFileRelativePath)
//   - features: List of enabled features (getFeatures)
//   - disabled_features: List of disabled features (getDisabledFeatures)
//   - var: Make variable dictionary (var)
//   - configuration: Build configuration (getConfiguration)
//   - fragments: Configuration fragments (getFragments)
//   - toolchains: Toolchain context (toolchains)
//   - exec_groups: Execution groups (execGroups)
//   - info_file: Non-volatile workspace status file (getStableWorkspaceStatus)
//   - version_file: Volatile workspace status file (getVolatileWorkspaceStatus)
//   - created_actions: Actions created so far, for testing (createdActions)
//   - rule: Rule attributes for aspects (rule)
//   - aspect_ids: Aspect IDs for aspects (aspectIds)
//
// # Implemented ctx Methods
//
// The following ctx methods are implemented (from StarlarkRuleContextApi.java):
//
//   - runfiles(): Create a runfiles object (runfiles)
//   - expand_location(): Expand $(location ...) patterns (expandLocation)
//   - expand_make_variables(): Expand $(VAR) patterns (expandMakeVariables)
//   - resolve_command(): Resolve command for execution (resolveCommand)
//   - resolve_tools(): Resolve tools for execution (resolveTools)
//   - tokenize(): Tokenize shell command string (tokenize)
//   - package_relative_label(): Convert string to Label (packageRelativeLabel)
//   - coverage_instrumented(): Check coverage instrumentation (instrumentCoverage)
//
// # Implemented ctx.actions Methods
//
// The following action methods are implemented (from StarlarkActionFactoryApi.java):
//
//   - declare_file(): Declare an output file (declareFile)
//   - declare_directory(): Declare an output directory (declareDirectory)
//   - declare_symlink(): Declare an output symlink (declareSymlink)
//   - do_nothing(): Create a no-op action (doNothing)
//   - write(): Write content to a file (write)
//   - run(): Run an executable (run)
//   - run_shell(): Run a shell command (runShell)
//   - expand_template(): Expand a template file (expandTemplate)
//   - symlink(): Create a symlink action (symlink)
//   - args(): Create an Args object for command lines (args)
//   - template_dict(): Create a TemplateDict for expand_template (templateDict)
//
// # Mock/Recording Behavior
//
// This implementation is designed for static analysis, not actual build execution.
// Actions declared via ctx.actions are recorded in a list that can be inspected
// via Actions.DeclaredActions(). This allows tools to:
//
//   - Analyze what actions a rule implementation would create
//   - Verify action declarations are correct
//   - Extract dependency information from declared actions
//   - Test rule implementations without running a full build
//
// # Usage Example
//
//	label, _ := types.ParseLabel("//pkg:target")
//	ctx := ctx.NewCtx(ctx.CtxConfig{
//	    Label:         label,
//	    WorkspaceName: "my_workspace",
//	    BinDir:        "bazel-out/k8-fastbuild/bin",
//	})
//
//	// Set up attribute values
//	ctx.AttrProxy().Set("name", starlark.String("target"))
//	ctx.AttrProxy().Set("srcs", starlark.NewList(sourceFiles))
//
//	// Execute rule implementation function
//	thread := &starlark.Thread{}
//	_, err := starlark.Call(thread, implFunc, starlark.Tuple{ctx}, nil)
//
//	// Inspect declared actions
//	for _, action := range ctx.Actions().DeclaredActions() {
//	    fmt.Printf("Action: %s, Outputs: %v\n", action.Type, action.Outputs)
//	}
package ctx
