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
// cataloged in LIMITATIONS.md (in this package directory). Keep that
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
	"github.com/albertocavalcante/starlark-go-bazel/builtins"
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
		if ru.AttrsExtractionMethod != report.AttrsUnresolved {
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
		if rr.AttrsExtractionMethod != report.AttrsUnresolved {
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

	// Aspects flow through the same evalFile/global-lookup machinery
	// as rules. The aspect's local binding name (`my_aspect =
	// aspect(...)`) IS the global it's exported under, so
	// lookupAspectClass(file, name) is exactly parallel to
	// lookupRuleClass.
	//
	// Pre-M0 this loop was a no-op: starlark-go-bazel's aspect()
	// rejected the AttrDescriptor type that attr.* produced. M0
	// (plans 07 + 08 upstream) unified the type via the
	// AttrDescriptorHolder interface, so aspects now hydrate via
	// Tier-3 the same way rules do.
	for i := range rep.Aspects {
		a := &rep.Aspects[i]
		if a.AttrsExtractionMethod != report.AttrsUnresolved {
			continue
		}
		if a.Provenance.File == "" {
			continue
		}
		ac := cache.lookupAspectClass(a.Provenance.File, a.Name)
		if ac == nil {
			continue
		}
		a.Attrs = attrsFromAspectClass(ac)
		a.AttrsExtractionMethod = report.AttrsInterpreted
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

// lookupRuleClass returns the named global as a *types.RuleClass, or
// nil if the file failed to eval, the symbol is missing, or the value
// is a different type. Caller treats nil as "leave this rule alone."
func (c *evalCache) lookupRuleClass(relFile, symbol string) *types.RuleClass {
	return lookupAs[*types.RuleClass](c, relFile, symbol)
}

// lookupRepositoryRuleClass mirrors lookupRuleClass for the
// repository_rule path. starlark-go-bazel exposes
// types.RepositoryRuleClass as a distinct type from RuleClass; users
// who built their rules via the older rule()-shaped path fall through
// to lookupRuleClass at the call site.
func (c *evalCache) lookupRepositoryRuleClass(relFile, symbol string) *types.RepositoryRuleClass {
	return lookupAs[*types.RepositoryRuleClass](c, relFile, symbol)
}

// lookupAspectClass mirrors lookupRuleClass for the aspect() path.
// AspectClass lives in starlark-go-bazel's builtins package (not
// types) because aspect's surface is M0+ and still being aligned with
// the types-package canonical shape; its Attrs() returns
// map[string]*types.AttrDescriptor though, so attrsFromAspectClass
// can reuse the same descriptor-projection patterns as rules.
func (c *evalCache) lookupAspectClass(relFile, symbol string) *builtins.AspectClass {
	return lookupAs[*builtins.AspectClass](c, relFile, symbol)
}

// lookupAs is the shared evalFile + global-lookup + type-assert flow.
// Returns the zero value of T (typically nil for pointer types) on any
// step's failure. Avoids the four-line repetition that the two
// concrete lookups previously expanded out.
func lookupAs[T any](c *evalCache, relFile, symbol string) T {
	var zero T
	globals, ok := c.evalFile(relFile)
	if !ok {
		return zero
	}
	val, found := globals[symbol]
	if !found {
		return zero
	}
	t, ok := val.(T)
	if !ok {
		return zero
	}
	return t
}

// providersToGroups lifts the flat upstream `Providers []string` into
// the ProviderGroups disjunction-of-conjunctions shape. The
// AttrDescriptor from starlark-go-bazel doesn't preserve the AND/OR
// distinction (it only carries the flat conjunction form); we map it
// to a single conjunction wrapped in one outer alternative, matching
// the AST-level extractProviderGroups output for the simple form.
func providersToGroups(providers []string) [][]string {
	if len(providers) == 0 {
		return nil
	}
	return [][]string{append([]string(nil), providers...)}
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
	src, err := os.ReadFile(absPath) //nolint:gosec // G304: relFile comes from the assay walker's own enumeration of the workspace, not user input.
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

// attrsFromAspectClass mirrors attrsFromRuleClass for aspects. Aspect
// attrs are a strict subset (implicit must be label-typed with a
// default; explicit must be string/int/bool) but the projection
// shape into AttrSpec is identical — same descriptor fields, same
// implicit-attr filtering, same name-sort.
func attrsFromAspectClass(ac *builtins.AspectClass) []report.AttrSpec {
	descriptors := ac.Attrs()
	out := make([]report.AttrSpec, 0, len(descriptors))
	for name, ad := range descriptors {
		if implicitBazelAttrs[name] {
			continue
		}
		spec := report.AttrSpec{
			Name:           name,
			Type:           string(ad.Type),
			Doc:            ad.Doc,
			Mandatory:      ad.Mandatory,
			ProviderGroups: providersToGroups(ad.Providers),
		}
		if ad.Default != nil {
			spec.Default = starlarkDefaultText(ad.Default)
		}
		out = append(out, spec)
	}
	sortAttrsByName(out)
	return out
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
			Name:           name,
			Type:           string(ad.Type),
			Doc:            ad.Doc,
			Mandatory:      ad.Mandatory,
			ProviderGroups: providersToGroups(ad.Providers),
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
