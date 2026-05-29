package ctx

import (
	"fmt"

	"github.com/albertocavalcante/starlark-go-bazel/taint"
	"github.com/albertocavalcante/starlark-go-bazel/version"
	"go.starlark.net/starlark"
)

// RepositoryCtx is the synthetic ctx passed to a repository_rule's
// impl during analysis-mode evaluation. Mirrors Bazel's
// repository_ctx surface from StarlarkBaseExternalContext.java and
// StarlarkRepositoryContext.java.
//
// Side-effecting methods are non-faithful:
//   - download / download_and_extract record to Sinks if present.
//   - execute / read / which return opaque values + set per-fork
//     tainted flag (subsequent downloads inherit Tainted=true).
//   - file / template / symlink / delete / rename / patch / extract
//     return None (no fs touched).
//   - watch / watch_tree / report_progress are no-ops.
//   - getenv reads from RepositoryCtxOptions.OSEnv when known;
//     unknown lookups taint.
//
// Version-gated attributes (e.g., repo_metadata for Bazel 9+) hide
// when the configured Version doesn't expose them.
type RepositoryCtx struct {
	name          string
	originalName  string
	workspaceRoot string
	osName        string
	osArch        string
	osEnv         map[string]string
	attrs         map[string]starlark.Value
	ver           version.Version
	sinks         *taint.Sinks
	tainted       bool

	// Lazily-populated singletons for child values that real impls
	// access multiple times in one invocation (typical: ctx.os.name +
	// ctx.os.arch, ctx.attr.X repeatedly). Saves an allocation per
	// repeat access. We deliberately do NOT cache method-builtins —
	// most are called ≤1× per impl, so a map miss + insert costs more
	// than the NewBuiltin we'd save.
	cachedOs   *RepositoryOs
	cachedAttr *RepositoryAttr
}

// RepositoryCtxOptions configures a synthetic ctx. Sinks is optional;
// supply one when Mode=Analysis to capture downloads.
type RepositoryCtxOptions struct {
	Name          string
	OriginalName  string
	WorkspaceRoot string
	OSName        string
	OSArch        string
	OSEnv         map[string]string
	Attrs         map[string]starlark.Value
	Version       version.Version
	Sinks         *taint.Sinks
}

// NewRepositoryCtx constructs a RepositoryCtx with sensible defaults
// for unset fields. The returned ctx is mutable across method calls
// (tainted flag flips).
func NewRepositoryCtx(opts RepositoryCtxOptions) *RepositoryCtx {
	root := opts.WorkspaceRoot
	if root == "" {
		root = "/synthetic/workspace"
	}
	return &RepositoryCtx{
		name:          opts.Name,
		originalName:  opts.OriginalName,
		workspaceRoot: root,
		osName:        opts.OSName,
		osArch:        opts.OSArch,
		osEnv:         opts.OSEnv,
		attrs:         opts.Attrs,
		ver:           opts.Version.Resolved(),
		sinks:         opts.Sinks,
	}
}

// Tainted reports whether any opaque op has been called on this ctx
// during the current impl invocation.
func (c *RepositoryCtx) Tainted() bool { return c.tainted }

var (
	_ starlark.Value    = (*RepositoryCtx)(nil)
	_ starlark.HasAttrs = (*RepositoryCtx)(nil)
)

func (c *RepositoryCtx) String() string        { return "<repository_ctx>" }
func (c *RepositoryCtx) Type() string          { return "repository_ctx" }
func (c *RepositoryCtx) Freeze()               {}
func (c *RepositoryCtx) Truth() starlark.Bool  { return true }
func (c *RepositoryCtx) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable: repository_ctx") }

func (c *RepositoryCtx) Attr(name string) (starlark.Value, error) {
	switch name {
	// Repository-ctx-specific attributes / methods.
	case "name":
		return starlark.String(c.name), nil
	case "original_name":
		if c.originalName != "" {
			return starlark.String(c.originalName), nil
		}
		return starlark.String(c.name), nil
	case "workspace_root":
		return starlark.String(c.workspaceRoot), nil
	case "attr":
		if c.cachedAttr == nil {
			c.cachedAttr = &RepositoryAttr{values: c.attrs}
		}
		return c.cachedAttr, nil
	case "os":
		if c.cachedOs == nil {
			c.cachedOs = &RepositoryOs{name: c.osName, arch: c.osArch, env: c.osEnv}
		}
		return c.cachedOs, nil
	case "symlink", "template", "delete", "rename", "patch", "extract", "file":
		return starlark.NewBuiltin("ctx."+name, noopNone), nil
	case "watch_tree":
		return starlark.NewBuiltin("ctx.watch_tree", noopNone), nil
	case "repo_metadata":
		// Bazel 9+ only.
		if c.ver >= version.V9 || c.ver == version.VLatest {
			return starlark.NewBuiltin("ctx.repo_metadata", c.repoMetadataMethod), nil
		}
		return nil, nil
	// Shared base methods (StarlarkBaseExternalContext).
	case "download":
		return starlark.NewBuiltin("ctx.download", c.downloadMethod), nil
	case "download_and_extract":
		return starlark.NewBuiltin("ctx.download_and_extract", c.downloadAndExtractMethod), nil
	case "execute":
		return starlark.NewBuiltin("ctx.execute", c.executeMethod), nil
	case "read":
		return starlark.NewBuiltin("ctx.read", c.readMethod), nil
	case "which":
		return starlark.NewBuiltin("ctx.which", c.whichMethod), nil
	case "getenv":
		// Bazel 8+ method.
		if c.ver >= version.V8 || c.ver == version.VLatest {
			return starlark.NewBuiltin("ctx.getenv", c.getenvMethod), nil
		}
		return nil, nil
	case "watch":
		// Bazel 8+ method.
		if c.ver >= version.V8 || c.ver == version.VLatest {
			return starlark.NewBuiltin("ctx.watch", noopNone), nil
		}
		return nil, nil
	case "path":
		return starlark.NewBuiltin("ctx.path", passThroughFirst), nil
	case "report_progress":
		return starlark.NewBuiltin("ctx.report_progress", noopNone), nil
	}
	return nil, nil
}

func (c *RepositoryCtx) AttrNames() []string {
	names := []string{
		"attr", "os", "name", "original_name", "workspace_root",
		"download", "download_and_extract", "execute", "read",
		"which", "path", "report_progress",
		"symlink", "template", "delete", "rename", "patch", "extract", "file",
	}
	if c.ver >= version.V8 || c.ver == version.VLatest {
		names = append(names, "getenv", "watch")
	}
	if c.ver >= version.V8 || c.ver == version.VLatest {
		names = append(names, "watch_tree")
	}
	if c.ver >= version.V9 || c.ver == version.VLatest {
		names = append(names, "repo_metadata")
	}
	return names
}

func (c *RepositoryCtx) downloadMethod(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	return c.recordDownload("ctx.download", args, kwargs)
}

func (c *RepositoryCtx) downloadAndExtractMethod(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	return c.recordDownload("ctx.download_and_extract", args, kwargs)
}

func (c *RepositoryCtx) recordDownload(api string, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if c.sinks == nil {
		// No capture configured (Mode != Analysis); silently accept.
		return starlark.True, nil
	}
	var urlArg starlark.Value
	var sha256, integrity, stripPrefix string
	if len(args) > 0 {
		urlArg = args[0]
	}
	for _, kv := range kwargs {
		key, _ := starlark.AsString(kv[0])
		switch key {
		case "url", "urls", "url_or_urls":
			urlArg = kv[1]
		case "sha256":
			sha256, _ = starlark.AsString(kv[1])
		case "integrity":
			integrity, _ = starlark.AsString(kv[1])
		case "strip_prefix", "stripPrefix":
			stripPrefix, _ = starlark.AsString(kv[1])
		}
	}
	urls, perURLTainted := taint.FlattenURLs(urlArg)
	platform := (taint.Platform{OS: c.osName, Arch: c.osArch}).Label()
	for _, u := range urls {
		c.sinks.URLs = append(c.sinks.URLs, taint.CapturedURL{
			URL:         u,
			SHA256:      sha256,
			Integrity:   integrity,
			Platform:    platform,
			StripPrefix: stripPrefix,
			APIName:     api,
			Tainted:     perURLTainted || c.tainted,
		})
	}
	return starlark.True, nil
}

// executeMethod returns a sentinel exec_result struct and flips the
// per-fork tainted flag. M4 wires the stub.Permissive type here so
// chained .stdout / .stderr access propagates taint into URL args.
// For M3, return None — taint propagation tests live in M4.
func (c *RepositoryCtx) executeMethod(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	c.tainted = true
	return starlark.None, nil
}

func (c *RepositoryCtx) readMethod(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	c.tainted = true
	return starlark.String(""), nil
}

func (c *RepositoryCtx) whichMethod(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	c.tainted = true
	return starlark.None, nil
}

func (c *RepositoryCtx) getenvMethod(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) == 0 {
		return starlark.None, nil
	}
	name, ok := starlark.AsString(args[0])
	if !ok {
		return starlark.None, nil
	}
	if v, ok := c.osEnv[name]; ok {
		return starlark.String(v), nil
	}
	if len(args) > 1 {
		return args[1], nil
	}
	c.tainted = true
	return starlark.None, nil
}

func (c *RepositoryCtx) repoMetadataMethod(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	return starlark.None, nil
}

func noopNone(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	return starlark.None, nil
}

func passThroughFirst(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) > 0 {
		return args[0], nil
	}
	return starlark.String(""), nil
}

// RepositoryOs implements ctx.os.{name, arch, environ}.
type RepositoryOs struct {
	name string
	arch string
	env  map[string]string
}

var (
	_ starlark.Value    = (*RepositoryOs)(nil)
	_ starlark.HasAttrs = (*RepositoryOs)(nil)
)

func (o *RepositoryOs) String() string        { return fmt.Sprintf("<os %s/%s>", o.name, o.arch) }
func (o *RepositoryOs) Type() string          { return "repository_os" }
func (o *RepositoryOs) Freeze()               {}
func (o *RepositoryOs) Truth() starlark.Bool  { return true }
func (o *RepositoryOs) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable: repository_os") }

func (o *RepositoryOs) Attr(name string) (starlark.Value, error) {
	switch name {
	case "name":
		return starlark.String(o.name), nil
	case "arch":
		return starlark.String(o.arch), nil
	case "environ":
		d := starlark.NewDict(len(o.env))
		for k, v := range o.env {
			_ = d.SetKey(starlark.String(k), starlark.String(v))
		}
		return d, nil
	}
	return nil, nil
}

func (o *RepositoryOs) AttrNames() []string { return []string{"name", "arch", "environ"} }

// RepositoryAttr implements ctx.attr.<name>. Unknown attrs return
// empty string (conservative; consider tainting in a future pass).
type RepositoryAttr struct {
	values map[string]starlark.Value
}

var (
	_ starlark.Value    = (*RepositoryAttr)(nil)
	_ starlark.HasAttrs = (*RepositoryAttr)(nil)
)

func (a *RepositoryAttr) String() string        { return "<repository_attr>" }
func (a *RepositoryAttr) Type() string          { return "repository_attr" }
func (a *RepositoryAttr) Freeze()               {}
func (a *RepositoryAttr) Truth() starlark.Bool  { return true }
func (a *RepositoryAttr) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable: repository_attr") }
func (a *RepositoryAttr) Attr(name string) (starlark.Value, error) {
	if v, ok := a.values[name]; ok {
		return v, nil
	}
	return starlark.String(""), nil
}

func (a *RepositoryAttr) AttrNames() []string {
	names := make([]string, 0, len(a.values))
	for k := range a.values {
		names = append(names, k)
	}
	return names
}
