package ast

import (
	"fmt"
	"os"

	"github.com/albertocavalcante/go-bzlmod-ast/buildutil"
	"github.com/albertocavalcante/go-bzlmod-ast/label"
	"github.com/albertocavalcante/go-bzlmod-ast/third_party/buildtools/build"
)

// ParseError represents a parsing error with position information.
type ParseError struct {
	Pos     Position
	Message string
	Wrapped error
}

func (e *ParseError) Error() string {
	if e.Pos.Line > 0 {
		return fmt.Sprintf("%s:%d:%d: %s", e.Pos.Filename, e.Pos.Line, e.Pos.Column, e.Message)
	}
	return e.Message
}

func (e *ParseError) Unwrap() error {
	return e.Wrapped
}

// ParseResult contains the parsed file and any diagnostics.
type ParseResult struct {
	File     *ModuleFile
	Errors   []*ParseError
	Warnings []*ParseError
}

// HasErrors returns true if there were parse errors.
func (r *ParseResult) HasErrors() bool {
	return len(r.Errors) > 0
}

// Parser parses MODULE.bazel files into AST.
type Parser struct {
	filename string
	errors   []*ParseError
	warnings []*ParseError
}

// ParseFile reads and parses a MODULE.bazel file from disk.
func ParseFile(filename string) (*ParseResult, error) {
	data, err := os.ReadFile(filename) // #nosec G304 -- intentional file read by caller-provided path
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", filename, err)
	}
	return ParseContent(filename, data)
}

// ParseContent parses MODULE.bazel content from bytes.
func ParseContent(filename string, content []byte) (*ParseResult, error) {
	p := &Parser{filename: filename}
	return p.parse(content)
}

func (p *Parser) parse(content []byte) (*ParseResult, error) {
	raw, err := build.ParseModule(p.filename, content)
	if err != nil {
		return nil, &ParseError{
			Pos:     Position{Filename: p.filename},
			Message: fmt.Sprintf("syntax error: %v", err),
			Wrapped: err,
		}
	}

	file := &ModuleFile{
		Path:       p.filename,
		Statements: make([]Statement, 0, len(raw.Stmt)),
		raw:        raw,
	}

	for _, stmt := range raw.Stmt {
		if s := p.parseStatement(stmt); s != nil {
			file.Statements = append(file.Statements, s)
		}
	}

	// Attach each ExtensionTagCall to its parent UseExtension.Tags
	// and drop it from the top-level statement list.
	file.Statements = linkExtensionTags(file.Statements)

	return &ParseResult{
		File:     file,
		Errors:   p.errors,
		Warnings: p.warnings,
	}, nil
}

func (p *Parser) parseStatement(expr build.Expr) Statement {
	// Handle assignment expressions like: go = use_extension(...) or http_archive = use_repo_rule(...)
	if assign, ok := expr.(*build.AssignExpr); ok {
		if call, ok := assign.RHS.(*build.CallExpr); ok {
			if ident, ok := call.X.(*build.Ident); ok {
				pos := p.span(call)
				switch ident.Name {
				case "use_extension":
					ue := p.parseUseExtension(call, pos)
					if ue != nil {
						// Capture LHS variable so downstream
						// ExtensionTagCall.Extension references can
						// resolve back to this UseExtension.
						if lhs, ok := assign.LHS.(*build.Ident); ok {
							ue.Variable = lhs.Name
						}
					}
					return ue
				case "use_repo_rule":
					return p.parseUseRepoRule(call, pos)
				}
			}
		}
		return nil
	}

	call, ok := expr.(*build.CallExpr)
	if !ok {
		return nil
	}

	pos := p.span(call)

	// Handle method calls like go_sdk.from_file(...) - extension tag calls
	if dotExpr, isDot := call.X.(*build.DotExpr); isDot {
		return p.parseExtensionTagCall(call, dotExpr, pos)
	}

	ident, isIdent := call.X.(*build.Ident)
	if !isIdent {
		return nil
	}

	switch ident.Name {
	case "module":
		return p.parseModule(call, pos)
	case "bazel_dep":
		return p.parseBazelDep(call, pos)
	case "use_extension":
		return p.parseUseExtension(call, pos)
	case "use_repo":
		return p.parseUseRepo(call, pos)
	case "single_version_override":
		return p.parseSingleVersionOverride(call, pos)
	case "multiple_version_override":
		return p.parseMultipleVersionOverride(call, pos)
	case "git_override":
		return p.parseGitOverride(call, pos)
	case "archive_override":
		return p.parseArchiveOverride(call, pos)
	case "local_path_override":
		return p.parseLocalPathOverride(call, pos)
	case "register_toolchains":
		return p.parseRegisterToolchains(call, pos)
	case "register_execution_platforms":
		return p.parseRegisterExecutionPlatforms(call, pos)
	case "include":
		return p.parseInclude(call, pos)
	case "use_repo_rule":
		return p.parseUseRepoRule(call, pos)
	case "inject_repo":
		return p.parseInjectRepo(call, pos)
	case "override_repo":
		return p.parseOverrideRepo(call, pos)
	case "flag_alias":
		return p.parseFlagAlias(call, pos)
	default:
		return &UnknownStatement{
			Pos:      pos,
			FuncName: ident.Name,
			Raw:      expr,
		}
	}
}

func (p *Parser) parseInclude(call *build.CallExpr, pos Span) *Include {
	inc := &Include{Pos: pos}

	// include() takes a single positional string argument (label)
	if len(call.List) > 0 {
		if str, ok := call.List[0].(*build.StringExpr); ok {
			inc.Label = str.Value
		}
	}

	// Also check for named "label" parameter
	if labelStr := buildutil.String(call, "label"); labelStr != "" {
		inc.Label = labelStr
	}

	if inc.Label == "" {
		p.addErrorf(pos.Start, "include: missing required label argument")
	}

	return inc
}

func (p *Parser) parseExtensionTagCall(call *build.CallExpr, dotExpr *build.DotExpr, pos Span) *ExtensionTagCall {
	tag := &ExtensionTagCall{
		Pos:        pos,
		Attributes: make(map[string]any),
		Raw:        call,
	}

	// Extract extension name (e.g., "go_sdk" from go_sdk.from_file)
	if ident, ok := dotExpr.X.(*build.Ident); ok {
		tag.Extension = ident.Name
	}

	// Extract tag/method name (e.g., "from_file")
	tag.TagName = dotExpr.Name

	// Extract all named attributes
	for _, arg := range call.List {
		if assign, ok := arg.(*build.AssignExpr); ok {
			if lhs, ok := assign.LHS.(*build.Ident); ok {
				tag.Attributes[lhs.Name] = buildutil.ExtractValue(assign.RHS)
			}
		}
	}

	return tag
}

func (p *Parser) parseModule(call *build.CallExpr, pos Span) *ModuleDecl {
	decl := &ModuleDecl{Pos: pos}

	if name := buildutil.String(call, "name"); name != "" {
		m, err := label.NewModule(name)
		if err != nil {
			p.addErrorf(pos.Start, "invalid module name: %v", err)
		} else {
			decl.Name = m
		}
	}

	if version := buildutil.String(call, "version"); version != "" {
		v, err := label.NewVersion(version)
		if err != nil {
			p.addErrorf(pos.Start, "invalid module version: %v", err)
		} else {
			decl.Version = v
		}
	}

	decl.CompatibilityLevel = buildutil.Int(call, "compatibility_level")

	if repoName := buildutil.String(call, "repo_name"); repoName != "" {
		r, err := label.NewApparentRepo(repoName)
		if err != nil {
			p.addErrorf(pos.Start, "invalid repo_name: %v", err)
		} else {
			decl.RepoName = r
		}
	}

	decl.BazelCompatibility = buildutil.StringList(call, "bazel_compatibility")

	return decl
}

func (p *Parser) parseBazelDep(call *build.CallExpr, pos Span) *BazelDep {
	dep := &BazelDep{Pos: pos}

	name := buildutil.String(call, "name")
	if name == "" {
		p.addErrorf(pos.Start, "bazel_dep: missing required 'name' attribute")
		return nil
	}

	m, err := label.NewModule(name)
	if err != nil {
		p.addErrorf(pos.Start, "bazel_dep: invalid name: %v", err)
		return nil
	}
	dep.Name = m

	version := buildutil.String(call, "version")
	if version == "" {
		// Missing version is valid when using local_path_override or other overrides
		p.addWarningf(pos.Start, "bazel_dep: missing 'version' attribute for %s (valid if using override)", name)
	} else {
		v, err := label.NewVersion(version)
		if err != nil {
			p.addErrorf(pos.Start, "bazel_dep: invalid version for %s: %v", name, err)
			return nil
		}
		dep.Version = v
	}

	dep.MaxCompatibilityLevel = buildutil.Int(call, "max_compatibility_level")
	dep.DevDependency = buildutil.Bool(call, "dev_dependency")

	if repoName := buildutil.String(call, "repo_name"); repoName != "" {
		r, err := label.NewApparentRepo(repoName)
		if err != nil {
			p.addErrorf(pos.Start, "bazel_dep: invalid repo_name for %s: %v", name, err)
		} else {
			dep.RepoName = r
		}
	}

	return dep
}

func (p *Parser) parseUseExtension(call *build.CallExpr, pos Span) *UseExtension {
	ext := &UseExtension{Pos: pos}

	// First positional arg is the .bzl file
	if len(call.List) > 0 {
		if str, ok := call.List[0].(*build.StringExpr); ok {
			lbl, err := label.ParseApparentLabel(str.Value)
			if err != nil {
				p.addErrorf(pos.Start, "use_extension: invalid extension file: %v", err)
			} else {
				ext.ExtensionFile = lbl
			}
		}
	}

	// Second positional arg is the extension name
	if len(call.List) > 1 {
		if str, ok := call.List[1].(*build.StringExpr); ok {
			id, err := label.NewStarlarkIdentifier(str.Value)
			if err != nil {
				p.addErrorf(pos.Start, "use_extension: invalid extension name: %v", err)
			} else {
				ext.ExtensionName = id
			}
		}
	}

	ext.DevDependency = buildutil.Bool(call, "dev_dependency")
	ext.Isolate = buildutil.Bool(call, "isolate")

	return ext
}

func (p *Parser) parseUseRepo(call *build.CallExpr, pos Span) *UseRepo {
	repo := &UseRepo{Pos: pos, Repos: make([]string, 0)}

	// First positional arg is the extension proxy ident; capture
	// its name so Handler.UseRepo can link this call back to the
	// originating use_extension without holding a parallel map.
	if len(call.List) > 0 {
		if ident, ok := call.List[0].(*build.Ident); ok {
			repo.ExtensionVariable = ident.Name
		}
	}

	// Walk positional repos + kwarg renames after the proxy ident.
	for i := 1; i < len(call.List); i++ {
		arg := call.List[i]
		switch a := arg.(type) {
		case *build.StringExpr:
			// use_repo(ext, "repo_a", "repo_b") — positional names.
			repo.Repos = append(repo.Repos, a.Value)
		case *build.AssignExpr:
			// use_repo(ext, my_alias = "remote_repo") — rename kwarg.
			lhs, lhsOK := a.LHS.(*build.Ident)
			rhs, rhsOK := a.RHS.(*build.StringExpr)
			if !lhsOK || !rhsOK {
				continue
			}
			if repo.Renames == nil {
				repo.Renames = map[string]string{}
			}
			repo.Renames[lhs.Name] = rhs.Value
		}
	}

	return repo
}

func (p *Parser) parseSingleVersionOverride(call *build.CallExpr, pos Span) *SingleVersionOverride {
	m, ok := p.parseRequiredModuleName(call, pos, "single_version_override")
	if !ok {
		return nil
	}

	override := &SingleVersionOverride{Pos: pos, Module: m}

	if version := buildutil.String(call, "version"); version != "" {
		v, err := label.NewVersion(version)
		if err != nil {
			p.addErrorf(pos.Start, "single_version_override: invalid version: %v", err)
		} else {
			override.Version = v
		}
	}

	override.Registry = buildutil.String(call, "registry")
	override.Patches = buildutil.StringList(call, "patches")
	override.PatchCmds = buildutil.StringList(call, "patch_cmds")
	override.PatchStrip = buildutil.Int(call, "patch_strip")

	return override
}

func (p *Parser) parseMultipleVersionOverride(call *build.CallExpr, pos Span) *MultipleVersionOverride {
	m, ok := p.parseRequiredModuleName(call, pos, "multiple_version_override")
	if !ok {
		return nil
	}

	override := &MultipleVersionOverride{Pos: pos, Module: m}

	for _, vs := range buildutil.StringList(call, "versions") {
		v, err := label.NewVersion(vs)
		if err != nil {
			p.addErrorf(pos.Start, "multiple_version_override: invalid version %q: %v", vs, err)
		} else {
			override.Versions = append(override.Versions, v)
		}
	}

	override.Registry = buildutil.String(call, "registry")

	return override
}

func (p *Parser) parseGitOverride(call *build.CallExpr, pos Span) *GitOverride {
	m, ok := p.parseRequiredModuleName(call, pos, "git_override")
	if !ok {
		return nil
	}

	return &GitOverride{
		Pos:            pos,
		Module:         m,
		Remote:         buildutil.String(call, "remote"),
		Commit:         buildutil.String(call, "commit"),
		Tag:            buildutil.String(call, "tag"),
		Branch:         buildutil.String(call, "branch"),
		Patches:        buildutil.StringList(call, "patches"),
		PatchCmds:      buildutil.StringList(call, "patch_cmds"),
		PatchStrip:     buildutil.Int(call, "patch_strip"),
		InitSubmodules: buildutil.Bool(call, "init_submodules"),
		StripPrefix:    buildutil.String(call, "strip_prefix"),
	}
}

func (p *Parser) parseArchiveOverride(call *build.CallExpr, pos Span) *ArchiveOverride {
	m, ok := p.parseRequiredModuleName(call, pos, "archive_override")
	if !ok {
		return nil
	}

	return &ArchiveOverride{
		Pos:         pos,
		Module:      m,
		URLs:        buildutil.StringList(call, "urls"),
		Integrity:   buildutil.String(call, "integrity"),
		StripPrefix: buildutil.String(call, "strip_prefix"),
		Patches:     buildutil.StringList(call, "patches"),
		PatchCmds:   buildutil.StringList(call, "patch_cmds"),
		PatchStrip:  buildutil.Int(call, "patch_strip"),
	}
}

func (p *Parser) parseLocalPathOverride(call *build.CallExpr, pos Span) *LocalPathOverride {
	m, ok := p.parseRequiredModuleName(call, pos, "local_path_override")
	if !ok {
		return nil
	}

	path := buildutil.String(call, "path")
	if path == "" {
		p.addErrorf(pos.Start, "local_path_override: missing required 'path'")
	}

	return &LocalPathOverride{
		Pos:    pos,
		Module: m,
		Path:   path,
	}
}

func (p *Parser) parseRegisterToolchains(call *build.CallExpr, pos Span) *RegisterToolchains {
	reg := &RegisterToolchains{Pos: pos}

	// Positional args are the toolchain patterns
	for _, arg := range call.List {
		if str, ok := arg.(*build.StringExpr); ok {
			reg.Patterns = append(reg.Patterns, str.Value)
		}
	}

	reg.DevDependency = buildutil.Bool(call, "dev_dependency")
	return reg
}

func (p *Parser) parseRegisterExecutionPlatforms(call *build.CallExpr, pos Span) *RegisterExecutionPlatforms {
	reg := &RegisterExecutionPlatforms{Pos: pos}

	// Positional args are the platform patterns
	for _, arg := range call.List {
		if str, ok := arg.(*build.StringExpr); ok {
			reg.Patterns = append(reg.Patterns, str.Value)
		}
	}

	reg.DevDependency = buildutil.Bool(call, "dev_dependency")
	return reg
}

func (p *Parser) parseUseRepoRule(call *build.CallExpr, pos Span) *UseRepoRule {
	rule := &UseRepoRule{Pos: pos}

	// use_repo_rule takes two positional args: bzl_file and rule_name
	if len(call.List) >= 1 {
		if str, ok := call.List[0].(*build.StringExpr); ok {
			rule.RuleFile = str.Value
		}
	}
	if len(call.List) >= 2 {
		if str, ok := call.List[1].(*build.StringExpr); ok {
			rule.RuleName = str.Value
		}
	}

	// Also check named parameters
	if file := buildutil.String(call, "repo_rule_bzl_file"); file != "" {
		rule.RuleFile = file
	}
	if name := buildutil.String(call, "repo_rule_name"); name != "" {
		rule.RuleName = name
	}

	return rule
}

func (p *Parser) parseInjectRepo(call *build.CallExpr, pos Span) *InjectRepo {
	inject := &InjectRepo{
		Pos:   pos,
		Repos: make(map[string]string),
	}

	// First arg is the extension proxy
	if len(call.List) >= 1 {
		if ident, ok := call.List[0].(*build.Ident); ok {
			inject.Extension = ident.Name
		}
	}

	// Named kwargs are the repo mappings
	for _, arg := range call.List {
		if assign, ok := arg.(*build.AssignExpr); ok {
			if lhs, ok := assign.LHS.(*build.Ident); ok {
				if str, ok := assign.RHS.(*build.StringExpr); ok {
					inject.Repos[lhs.Name] = str.Value
				}
			}
		}
	}

	return inject
}

func (p *Parser) parseOverrideRepo(call *build.CallExpr, pos Span) *OverrideRepo {
	override := &OverrideRepo{
		Pos:   pos,
		Repos: make(map[string]string),
	}

	// First arg is the extension proxy
	if len(call.List) >= 1 {
		if ident, ok := call.List[0].(*build.Ident); ok {
			override.Extension = ident.Name
		}
	}

	// Named kwargs are the repo mappings
	for _, arg := range call.List {
		if assign, ok := arg.(*build.AssignExpr); ok {
			if lhs, ok := assign.LHS.(*build.Ident); ok {
				if str, ok := assign.RHS.(*build.StringExpr); ok {
					override.Repos[lhs.Name] = str.Value
				}
			}
		}
	}

	return override
}

func (p *Parser) parseFlagAlias(call *build.CallExpr, pos Span) *FlagAlias {
	alias := &FlagAlias{Pos: pos}

	alias.Name = buildutil.String(call, "name")
	alias.StarlarkFlag = buildutil.String(call, "starlark_flag")

	if alias.Name == "" {
		p.addErrorf(pos.Start, "flag_alias: missing required 'name' attribute")
	}
	if alias.StarlarkFlag == "" {
		p.addErrorf(pos.Start, "flag_alias: missing required 'starlark_flag' attribute")
	}

	return alias
}

// Helper methods for extracting attributes

// span returns the half-open [start, end) source range of expr.
// Used to populate the Pos field of every typed statement so
// downstream tooling can render underlines and fold ranges.
func (p *Parser) span(expr build.Expr) Span {
	start, end := expr.Span()
	return Span{
		Start: Position{Filename: p.filename, Line: start.Line, Column: start.LineRune},
		End:   Position{Filename: p.filename, Line: end.Line, Column: end.LineRune},
	}
}

func (p *Parser) addErrorf(pos Position, format string, args ...any) {
	p.errors = append(p.errors, &ParseError{
		Pos:     pos,
		Message: fmt.Sprintf(format, args...),
	})
}

func (p *Parser) addWarningf(pos Position, format string, args ...any) {
	p.warnings = append(p.warnings, &ParseError{
		Pos:     pos,
		Message: fmt.Sprintf(format, args...),
	})
}

// parseRequiredModuleName extracts and validates the module_name attribute.
// Returns the module and true on success, or zero value and false on error.
// Errors are added to the parser's error list.
func (p *Parser) parseRequiredModuleName(call *build.CallExpr, pos Span, funcName string) (label.Module, bool) {
	moduleName := buildutil.String(call, "module_name")
	if moduleName == "" {
		p.addErrorf(pos.Start, "%s: missing required 'module_name'", funcName)
		return label.Module{}, false
	}

	m, err := label.NewModule(moduleName)
	if err != nil {
		p.addErrorf(pos.Start, "%s: invalid module_name: %v", funcName, err)
		return label.Module{}, false
	}
	return m, true
}

// linkExtensionTags attaches each ExtensionTagCall to the
// UseExtension whose Variable matches and returns the statement
// slice with the consumed calls removed. Orphan calls (no matching
// use_extension) stay in place.
func linkExtensionTags(stmts []Statement) []Statement {
	extByVar := map[string]*UseExtension{}
	for _, s := range stmts {
		if ue, ok := s.(*UseExtension); ok {
			if _, exists := extByVar[ue.Variable]; !exists {
				extByVar[ue.Variable] = ue
			}
		}
	}
	out := stmts[:0]
	for _, s := range stmts {
		tc, isTagCall := s.(*ExtensionTagCall)
		if !isTagCall {
			out = append(out, s)
			continue
		}
		ue, ok := extByVar[tc.Extension]
		if !ok {
			out = append(out, s)
			continue
		}
		ue.Tags = append(ue.Tags, ExtensionTag{
			Pos:        tc.Pos,
			Name:       tc.TagName,
			Attributes: tc.Attributes,
		})
	}
	return out
}
