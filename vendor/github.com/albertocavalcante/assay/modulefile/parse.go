// Package modulefile parses MODULE.bazel content into assay's report types.
// Wraps go-bzlmod-ast so callers don't need to know about its internal shape.
package modulefile

import (
	"fmt"
	"maps"
	"os"

	ast "github.com/albertocavalcante/go-bzlmod-ast"
	"github.com/albertocavalcante/go-bzlmod-ast/label"

	"github.com/albertocavalcante/assay/report"
)

// ParseFile reads and parses a MODULE.bazel file at the given path.
// Returns the module-level fields ready to merge into a ModuleReport.
//
// Filesystem errors are returned wrapped via [fs.PathError]'s natural
// formatting (e.g. "open /path/MODULE.bazel: no such file or
// directory"); callers that want errors.Is(err, fs.ErrNotExist)
// matching should rely on the wrapping chain rather than parsing
// strings.
func ParseFile(path string) (*report.ModuleReport, error) {
	b, err := os.ReadFile(path) //nolint:gosec // G304: ParseFile takes a path from the caller by design.
	if err != nil {
		return nil, err
	}
	return ParseContent(string(b))
}

// ParseContent parses MODULE.bazel content provided as a string.
// Wraps go-bzlmod-ast errors with a "parse MODULE.bazel:" prefix so
// the caller's chain doesn't drop the context that this WAS parse
// work.
//
// Built on go-bzlmod-ast's Handler pattern: one parse, one walk.
// Replaces the prior dual-parse (go-bzlmod's structured ModuleInfo
// + a separate go.starlark.net AST pass for the surface go-bzlmod
// didn't expose).
func ParseContent(content string) (*report.ModuleReport, error) {
	result, err := ast.ParseContent("MODULE.bazel", []byte(content))
	if err != nil {
		return nil, fmt.Errorf("parse MODULE.bazel: %w", err)
	}
	h := &reportHandler{r: &report.ModuleReport{}, extByVar: map[string]*report.ExtensionUse{}}
	if werr := ast.Walk(result.File, h); werr != nil {
		return nil, fmt.Errorf("walk MODULE.bazel: %w", werr)
	}
	return h.r, nil
}

// reportHandler is the assay-side Handler implementation that
// projects every MODULE.bazel statement into report.ModuleReport
// fields. Embeds BaseHandler so callbacks we don't need (most of
// them) stay as no-ops.
type reportHandler struct {
	ast.BaseHandler
	r *report.ModuleReport
	// extByVar links use_repo + tag-invocation callbacks back to the
	// ExtensionUse the use_extension created. Same role as
	// modulefile/surface.go's pre-Phase-0D extByLocal map; now driven
	// by the Handler's `variable` parameter rather than re-derived
	// from the AST.
	extByVar map[string]*report.ExtensionUse
}

func (h *reportHandler) Module(name label.Module, version label.Version, compatLevel int, _ label.ApparentRepo, bazelCompatibility []string) error {
	h.r.Name = name.String()
	if version != (label.Version{}) {
		h.r.Version = version.String()
	}
	h.r.CompatibilityLevel = compatLevel
	h.r.BazelCompatibility = append(h.r.BazelCompatibility, bazelCompatibility...)
	return nil
}

func (h *reportHandler) BazelDep(name label.Module, version label.Version, _ int, repoName label.ApparentRepo, devDep bool) error {
	h.r.BazelDeps = append(h.r.BazelDeps, report.ModuleKey{
		Name:          name.String(),
		Version:       version.String(),
		DevDependency: devDep,
		RepoName:      repoName.String(),
	})
	return nil
}

func (h *reportHandler) UseExtension(variable string, extFile label.ApparentLabel, extName label.StarlarkIdentifier, devDep, _ bool, tags []ast.ExtensionTag) error {
	use := report.ExtensionUse{
		LocalName:     variable,
		BzlFile:       extFile.String(),
		ExtensionName: extName.String(),
		DevDependency: devDep,
		Provenance:    spanToProvenance(report.Provenance{}, ast.Span{}), // overwritten below if we wanted span info; the Handler doesn't currently pass it
	}
	for _, tag := range tags {
		use.TagInvocations = append(use.TagInvocations, projectTagInvocation(tag))
	}
	h.r.UsedExtensions = append(h.r.UsedExtensions, use)
	h.extByVar[variable] = &h.r.UsedExtensions[len(h.r.UsedExtensions)-1]
	return nil
}

func (h *reportHandler) UseRepo(extensionVariable string, repos []string, renames map[string]string, _ bool) error {
	use, ok := h.extByVar[extensionVariable]
	if !ok {
		// No matching extension — orphan use_repo. Skip per the
		// existing surface.go silent-drop policy.
		return nil
	}
	use.ImportedRepos = append(use.ImportedRepos, repos...)
	if len(renames) > 0 {
		if use.RenamedRepos == nil {
			use.RenamedRepos = map[string]string{}
		}
		maps.Copy(use.RenamedRepos, renames)
	}
	return nil
}

func (h *reportHandler) SingleVersionOverride(moduleName label.Module, version label.Version, _ string, patches, patchCmds []string, patchStrip int) error {
	h.r.Overrides = append(h.r.Overrides, report.ModuleOverride{
		Kind:       "single_version",
		ModuleName: moduleName.String(),
		Version:    version.String(),
		Patches:    patches,
		PatchCmds:  patchCmds,
		PatchStrip: patchStrip,
	})
	return nil
}

func (h *reportHandler) MultipleVersionOverride(moduleName label.Module, versions []label.Version, _ string) error {
	vs := make([]string, 0, len(versions))
	for _, v := range versions {
		vs = append(vs, v.String())
	}
	h.r.Overrides = append(h.r.Overrides, report.ModuleOverride{
		Kind:       "multiple_version",
		ModuleName: moduleName.String(),
		Versions:   vs,
	})
	return nil
}

func (h *reportHandler) GitOverride(moduleName label.Module, remote, commit, _, _ string, patches, patchCmds []string, patchStrip int, _ bool, _ string) error {
	var urls []string
	if remote != "" {
		urls = []string{remote}
	}
	h.r.Overrides = append(h.r.Overrides, report.ModuleOverride{
		Kind:       "git",
		ModuleName: moduleName.String(),
		URLs:       urls,
		Commit:     commit,
		Patches:    patches,
		PatchCmds:  patchCmds,
		PatchStrip: patchStrip,
	})
	return nil
}

func (h *reportHandler) ArchiveOverride(moduleName label.Module, urls []string, integrity, _ string, patches, patchCmds []string, patchStrip int) error {
	h.r.Overrides = append(h.r.Overrides, report.ModuleOverride{
		Kind:       "archive",
		ModuleName: moduleName.String(),
		URLs:       urls,
		Integrity:  integrity,
		Patches:    patches,
		PatchCmds:  patchCmds,
		PatchStrip: patchStrip,
	})
	return nil
}

func (h *reportHandler) LocalPathOverride(moduleName label.Module, path string) error {
	h.r.Overrides = append(h.r.Overrides, report.ModuleOverride{
		Kind:       "local_path",
		ModuleName: moduleName.String(),
		Path:       path,
	})
	return nil
}

func (h *reportHandler) RegisterToolchains(patterns []string, _ bool) error {
	h.r.RegisteredToolchains = append(h.r.RegisteredToolchains, patterns...)
	return nil
}

func (h *reportHandler) RegisterExecutionPlatforms(patterns []string, _ bool) error {
	h.r.RegisteredExecutionPlatforms = append(h.r.RegisteredExecutionPlatforms, patterns...)
	return nil
}

func (h *reportHandler) Include(labelStr string, _ ast.Span) error {
	h.r.Includes = append(h.r.Includes, labelStr)
	return nil
}

// projectTagInvocation projects a single ast.ExtensionTag into the
// typed assay shape. attrs come through as map[string]any from the
// buildtools side; we type-switch into the per-Starlark-shape maps
// canopy renders without re-parsing strings.
func projectTagInvocation(tag ast.ExtensionTag) report.ExtensionTagInvocation {
	out := report.ExtensionTagInvocation{TagName: tag.Name}
	for k, v := range tag.Attributes {
		switch val := v.(type) {
		case string:
			if out.Kwargs == nil {
				out.Kwargs = map[string]string{}
			}
			out.Kwargs[k] = val
		case bool:
			if out.KwargBools == nil {
				out.KwargBools = map[string]bool{}
			}
			out.KwargBools[k] = val
		case int64:
			if out.KwargInts == nil {
				out.KwargInts = map[string]int64{}
			}
			out.KwargInts[k] = val
		case int:
			if out.KwargInts == nil {
				out.KwargInts = map[string]int64{}
			}
			out.KwargInts[k] = int64(val)
		case []any:
			items := make([]string, 0, len(val))
			for _, el := range val {
				if s, ok := el.(string); ok {
					items = append(items, s)
				}
			}
			if len(items) > 0 {
				if out.KwargLists == nil {
					out.KwargLists = map[string][]string{}
				}
				out.KwargLists[k] = items
			}
		}
	}
	return out
}

// spanToProvenance projects an ast.Span into assay's
// report.Provenance shape. Unused today (the Handler doesn't pass
// statement spans), retained to centralize the conversion for when
// it does.
func spanToProvenance(base report.Provenance, _ ast.Span) report.Provenance {
	return base
}
