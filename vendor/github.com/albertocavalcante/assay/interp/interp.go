// Package interp is the Tier-3 attrs extractor: it runs the .bzl
// file in a sandboxed Bazel-Starlark interpreter and reads the
// resulting RuleClass globals directly. This is the authoritative
// path for rule attribute SET membership — what shows up here is
// exactly what Bazel would resolve at module load time.
//
// Cost is much higher than Tier 0/1 (full interpreter per file, plus
// any load() chains it pulls in), so canopy callers gate Hydrate
// behind an opt-in feature flag. The package itself depends only on
// starlark-go-bazel; assay/bzlwalk does NOT import interp, so projects
// that don't need the heavy path don't pay for the interpreter dep.
//
// Current scope, known limitations, and which-test-pins-which-gap are
// catalogued in LIMITATIONS.md (in this package directory). Keep that
// file authoritative; this comment summarizes the high points:
//
//   - rule() is supported — attr names are extracted, the rule's
//     executable/test flags propagate, and the rule's Doc is captured.
//   - repository_rule() and module_extension() are NOT yet supported by
//     starlark-go-bazel's builtins; Hydrate silently skips entries it
//     can't resolve so the "dynamic schema" UI fallback stays correct
//     for those.
//   - Per-attr Type / Default / Doc / Mandatory propagation through
//     types.RuleClass is currently stubbed upstream — interpreted
//     AttrSpec entries carry attribute names only, not the richer
//     descriptor fields Tier 0/1 capture from the literal AST.
//   - External loads (`load("@external//...", ...)`) are rewritten to
//     stub bindings via interp/stubload.go so the import doesn't
//     abort module-load eval. Rules that USE the stubbed symbols at
//     module-level (rare) will still fail.
//
// See LIMITATIONS.md for the full list, the pinning tests, and the
// rationale for each gap.
package interp

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"

	"github.com/albertocavalcante/assay/report"
	"github.com/albertocavalcante/starlark-go-bazel/bzl"
	"github.com/albertocavalcante/starlark-go-bazel/types"
	"go.starlark.net/starlark"
)

// Hydrate walks rep.Rules and rep.RepositoryRules; for every entry
// whose AttrsExtractionMethod is empty (= Tier 0+1 produced no attrs)
// it tries to evaluate the source .bzl file and look up the named
// global. If that global is a *types.RuleClass, its attrs are dropped
// into the spec and the method is set to AttrsInterpreted.
//
// On any failure — file won't parse, file won't eval (cycle, bad
// load), symbol absent from globals, symbol exists but isn't a
// RuleClass — Hydrate silently leaves the entry alone. The UI's
// existing "dynamic schema" path stays the truthful fallback.
//
// Per-file evaluations are cached so a file containing N un-resolved
// rules pays for one interpreter invocation, not N. Cache is
// invocation-scoped (Hydrate-local map); the starlark-go-bazel
// loader's own LRU handles cross-file load() reuse within an eval.
func Hydrate(_ context.Context, workspaceRoot string, rep *report.ModuleReport) {
	if rep == nil {
		return
	}
	cache := newEvalCache(workspaceRoot)

	for i := range rep.Rules {
		ru := &rep.Rules[i]
		if ru.AttrsExtractionMethod != "" {
			continue // earlier tier already resolved
		}
		if ru.Provenance.File == "" {
			continue
		}
		rc := cache.lookupRuleClass(ru.Provenance.File, ru.Name)
		if rc == nil {
			continue
		}
		ru.Attrs = attrsFromRuleClass(rc)
		ru.AttrsExtractionMethod = report.AttrsInterpreted
	}

	for i := range rep.RepositoryRules {
		rr := &rep.RepositoryRules[i]
		if rr.AttrsExtractionMethod != "" {
			continue
		}
		if rr.Provenance.File == "" {
			continue
		}
		if rrc := cache.lookupRepositoryRuleClass(rr.Provenance.File, rr.Name); rrc != nil {
			rr.Attrs = attrsFromRepositoryRuleClass(rrc)
			rr.AttrsExtractionMethod = report.AttrsInterpreted
			continue
		}
		// Some users still define repository rules via the older
		// rule()-shaped pattern; fall back to the build-time RuleClass.
		rc := cache.lookupRuleClass(rr.Provenance.File, rr.Name)
		if rc == nil {
			continue
		}
		rr.Attrs = attrsFromRuleClass(rc)
		rr.AttrsExtractionMethod = report.AttrsInterpreted
	}
}

// evalCache memoizes per-file evaluation results so Hydrate doesn't
// re-evaluate the same file once per un-resolved rule it contains.
// Failures cache too (as nil) so a single broken file doesn't get
// re-attempted for every rule that lived in it.
type evalCache struct {
	workspaceRoot string
	interp        *bzl.Interpreter
	results       map[string]starlark.StringDict
	failed        map[string]bool
}

func newEvalCache(workspaceRoot string) *evalCache {
	return &evalCache{
		workspaceRoot: workspaceRoot,
		// LenientLoad: even with the source-rewrite below, transitive
		// loads inside the (now-running) in-module .bzl files may
		// reach further externals we couldn't see ahead of time.
		// Lenient mode keeps those soft-failing too.
		interp: bzl.New(bzl.Options{
			WorkspaceRoot: workspaceRoot,
			LenientLoad:   true,
		}),
		results: map[string]starlark.StringDict{},
		failed:  map[string]bool{},
	}
}

// lookupRuleClass returns the named global from the evaluated .bzl
// file, but only if it's a *types.RuleClass. nil for any other case
// (eval failure, missing global, wrong type). Caller treats nil as
// "leave this rule untouched."
func (c *evalCache) lookupRuleClass(relFile, symbol string) *types.RuleClass {
	globals, ok := c.evalFile(relFile)
	if !ok {
		return nil
	}
	val, found := globals[symbol]
	if !found {
		return nil
	}
	rc, ok := val.(*types.RuleClass)
	if !ok {
		return nil
	}
	return rc
}

// lookupRepositoryRuleClass mirrors lookupRuleClass for the
// repository_rule path. The upstream type is types.RepositoryRuleClass
// (separate from types.RuleClass for analysis-time rule()), which
// became available when starlark-go-bazel landed M2 of the
// bazel-builtins-emulation plan.
func (c *evalCache) lookupRepositoryRuleClass(relFile, symbol string) *types.RepositoryRuleClass {
	globals, ok := c.evalFile(relFile)
	if !ok {
		return nil
	}
	val, found := globals[symbol]
	if !found {
		return nil
	}
	rc, ok := val.(*types.RepositoryRuleClass)
	if !ok {
		return nil
	}
	return rc
}

// attrsFromRepositoryRuleClass mirrors attrsFromRuleClass for
// repository_rule.Attrs (which is map[string]starlark.Value rather
// than the typed AttrDescriptor map of regular rules — production
// attr-descriptor extraction is a separate plan).
func attrsFromRepositoryRuleClass(rc *types.RepositoryRuleClass) []report.AttrSpec {
	attrs := rc.Attrs()
	out := make([]report.AttrSpec, 0, len(attrs))
	for name := range attrs {
		if implicitBazelAttrs[name] {
			continue
		}
		out = append(out, report.AttrSpec{Name: name})
	}
	sortAttrsByName(out)
	return out
}

func (c *evalCache) evalFile(relFile string) (starlark.StringDict, bool) {
	if c.failed[relFile] {
		return nil, false
	}
	if globals, ok := c.results[relFile]; ok {
		return globals, true
	}

	// Read + stub-rewrite + eval rather than calling EvalFile
	// directly. stubExternalLoads turns `load("@external//...", "x")`
	// into `x = None` so the interpreter doesn't choke on external
	// repos that aren't materialized in canopy's ingest tree. In-
	// module loads ("//...") stay untouched and use the lenient
	// FileSystem loader.
	absPath := filepath.Join(c.workspaceRoot, relFile)
	src, err := os.ReadFile(absPath)
	if err != nil {
		slog.Debug("interp: read failed", "file", relFile, "err", err)
		c.failed[relFile] = true
		return nil, false
	}
	rewritten, stubbed, err := stubExternalLoads(src, relFile)
	if err != nil {
		slog.Debug("interp: load-stub rewrite failed", "file", relFile, "err", err)
		c.failed[relFile] = true
		return nil, false
	}
	res, err := c.interp.Eval(absPath, rewritten)
	if err != nil {
		slog.Debug("interp: eval failed", "file", relFile, "stubbed_loads", stubbed, "err", err)
		c.failed[relFile] = true
		return nil, false
	}
	slog.Debug("interp: eval ok", "file", relFile, "stubbed_loads", stubbed, "globals", len(res.Globals))
	c.results[relFile] = res.Globals
	return res.Globals, true
}

// joinPath avoids a dependency on path/filepath in the hot path —
// the interp call wants a single absolute path string. Workspace
// roots are absolute, file paths are workspace-relative POSIX, and
// the OS filesystem the loader uses accepts both separators on
// macOS/Linux, so a simple `/` join is enough.
func joinPath(root, rel string) string {
	if root == "" {
		return rel
	}
	if rel == "" {
		return root
	}
	if root[len(root)-1] == '/' {
		return root + rel
	}
	return root + "/" + rel
}

// implicitBazelAttrs are the attribute names Bazel automatically adds
// to every rule's schema (visibility, tags, testonly, …). They aren't
// part of what the .bzl author wrote, so filtering them keeps the
// interpreted attr list comparable to what bzlwalk's literal/symbol-
// fold tiers produce. The set tracks what builtins/rule.go and
// types/rule_class.go inject; if upstream adds new implicit attrs,
// add them here too.
var implicitBazelAttrs = map[string]bool{
	"name":        true,
	"visibility":  true,
	"tags":        true,
	"testonly":    true,
	"deprecation": true,
	"features":    true,
}

// attrsFromRuleClass translates the interpreter's AttrDescriptor map
// into assay's flat AttrSpec slice. Map ordering in Go is randomized,
// so we sort by name to keep diffs deterministic across runs. Implicit
// Bazel attributes are filtered out so the interpreted result matches
// what the AST-level path would have reported for the same source.
func attrsFromRuleClass(rc *types.RuleClass) []report.AttrSpec {
	descriptors := rc.Attrs()
	out := make([]report.AttrSpec, 0, len(descriptors))
	for name, ad := range descriptors {
		if implicitBazelAttrs[name] {
			continue
		}
		spec := report.AttrSpec{
			Name:      name,
			Type:      string(ad.Type),
			Doc:       ad.Doc,
			Mandatory: ad.Mandatory,
			Providers: append([]string(nil), ad.Providers...),
		}
		if ad.Default != nil {
			spec.Default = starlarkDefaultText(ad.Default)
		}
		out = append(out, spec)
	}
	sortAttrsByName(out)
	return out
}

// starlarkDefaultText renders a starlark.Value as the kind of literal
// text the AST-level extractAttrs path emits — quoted strings, bare
// numbers, lowercase True/False, "None". Anything more exotic (list,
// dict, struct) is rendered via its String() method which is the
// human-readable Starlark form; downstream UI treats Default as
// opaque text.
func starlarkDefaultText(v starlark.Value) string {
	switch x := v.(type) {
	case starlark.String:
		return strconv.Quote(string(x))
	case starlark.Int:
		return x.String()
	case starlark.Bool:
		if x {
			return "True"
		}
		return "False"
	case starlark.NoneType:
		return "None"
	default:
		return x.String()
	}
}

// sortAttrsByName is a tiny in-place sort; keeping it inline rather
// than importing sort avoids a tiny stdlib import for one call site.
func sortAttrsByName(attrs []report.AttrSpec) {
	for i := 1; i < len(attrs); i++ {
		for j := i; j > 0 && attrs[j-1].Name > attrs[j].Name; j-- {
			attrs[j-1], attrs[j] = attrs[j], attrs[j-1]
		}
	}
	// Defensive: catch any nil-name entries that would sort weirdly.
	for _, a := range attrs {
		if a.Name == "" {
			// Should never happen because RuleClass.attrs map can't
			// have empty keys, but the panic here would surface
			// a real bug fast in CI.
			panic(fmt.Sprintf("interp: empty attr name in rule %q", a.Type))
		}
	}
}
