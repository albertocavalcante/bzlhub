// Package hermetic classifies a module's hermeticity profile by walking its
// .bzl AST and pattern-matching against dialect-defined symbol sets.
//
// # Epistemic status
//
// Each finding carries a [report.Confidence] value (Definitive or
// Heuristic). Consumers (canopy registry page, audit scripts) MUST
// respect that field — don't render a Heuristic finding with the same
// visual weight as a Definitive one. The per-class breakdown:
//
//   - [report.NetworkFetchPinned] — Definitive (literal sha256 observed)
//   - [report.PrebuiltBinariesPinned] — Definitive (literal sha256 + executable=True)
//   - [report.NetworkFetchUnpinned] — Heuristic (conservative fallback when sha256 isn't literal)
//   - [report.RequiresSystemTools] — Heuristic (curated binary list, not exhaustive)
//   - [report.RepositoryRuleArbitraryCode] — Heuristic (fallback when execute arg shape is opaque)
//   - [report.BuildFromSource] — Heuristic (curated rule list + path filter + self-publish demotion)
//   - [report.PureStarlark] — synthesized when no findings exist
//
// # Conservatism principle
//
// When the classifier can't determine something statically — URLs
// built from interpolated variables, opaque ctx.execute args, etc. —
// it records a Heuristic finding rather than silently passing. False
// positives are preferred over false negatives: a module flagged as
// needing review just gets reviewed.
//
// # Heuristic sources
//
// Read these before changing detection logic; the union of their
// approximations is what makes findings Heuristic rather than
// Definitive:
//
//   - [dialect.Dialect].IsCompilationRuleSymbol — curated list of `<lang>_binary` etc.
//   - [dialect.Dialect].IsNetworkFetchAPI — curated download API names
//   - [dialect.Dialect].IsSystemExecAPI — currently just "execute"
//   - classifyExec — well-known binary list (docker, git, python, …)
//   - hasIntegrityHash — literal sha256/integrity + same/cross-file dict-subscript
//   - [syntaxutil.BoolKeywordArg] — literal `executable = True` only
//   - looksLikeSelfPublishURL — string-literal substring match
//   - isTestOrExamplePath — curated directory-name list
//   - isReleaseToolingPath — `tools/`/`tooling/` segment match
//
// New detectors added here MUST declare their confidence at emit time.
package hermetic

import (
	"context"
	"path/filepath"
	"strings"

	"go.starlark.net/syntax"

	"github.com/albertocavalcante/assay/dialect"
	"github.com/albertocavalcante/assay/internal/syntaxutil"
	"github.com/albertocavalcante/assay/internal/walkparse"
	"github.com/albertocavalcante/assay/report"
)

// Classify walks rootDir's tree and records hermeticity findings.
//
// Standalone convenience wrapper — it invokes walkparse.Walk to do
// the single shared walk + parse, then delegates to ClassifyParsed.
// assay.Analyze calls ClassifyParsed directly with a file slice
// shared between bzlwalk and hermetic so each file is parsed once.
//
// ctx bounds the walk: cancellation is checked at each filesystem entry.
func Classify(ctx context.Context, rootDir string, d dialect.Dialect, r *report.ModuleReport) error {
	files, err := walkparse.Walk(ctx, rootDir)
	if err != nil {
		return err
	}
	return ClassifyParsed(ctx, d, files, r)
}

// ClassifyParsed runs hermeticity classification over a pre-parsed
// file slice (typically produced by walkparse.Walk). Does not touch
// the filesystem and does not re-parse anything — both the per-file
// pre-pass (literal-dict bindings + loads) and the main scan iterate
// the same in-memory ASTs.
func ClassifyParsed(ctx context.Context, d dialect.Dialect, files []walkparse.File, r *report.ModuleReport) error {
	idx := buildHermeticIndexFromFiles(files)
	c := &classifier{dialect: d, moduleName: r.Name, index: idx}
	for _, f := range files {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if f.AST == nil {
			continue
		}
		isBuild := f.Kind == "build"
		isBzl := f.Kind == "bzl"
		if !isBuild && !isBzl {
			continue
		}
		c.scan(f.AST, f.Path, isBuild)
	}
	r.Hermeticity = c.finalize()
	return nil
}

type classifier struct {
	dialect  dialect.Dialect
	findings []report.HermeticityFinding

	// moduleName is the module's declared name from MODULE.bazel
	// (e.g., "bazel_lib"). Used by the self-publish detector to
	// recognize when a rctx.download URL points at the module's own
	// GitHub releases — the bazel-lib pattern where consumers
	// download prebuilt binaries even though source exists in the
	// repo for the release pipeline.
	moduleName string

	// selfPublishesBinaries is set the first time we see a string
	// literal that contains both "/releases/download/" and a path
	// segment matching the normalized moduleName. Triggers BFS
	// demotion when all BFS findings live under tools/ paths.
	selfPublishesBinaries bool

	// index is the module-wide pre-pass cache that lets the
	// integrity-hash detector follow load()-imported dict names
	// into their source files. Nil disables cross-file lookup
	// (every external resolution returns ok=false).
	index *hermeticIndex
}

// scan walks an entire file looking for calls to network-fetch APIs,
// system-exec APIs, and (in BUILD files) source-compilation rules.
// Detection is by callee name only — we don't track receiver types,
// since most module .bzl files use them via well-known patterns
// (e.g., `repository_ctx.download_and_extract(...)` shows up as a
// DotExpr with selector "download_and_extract").
//
// isBuild signals whether the file is a BUILD/BUILD.bazel (so the
// compilation-rule detection runs). For .bzl files those rules might
// merely be referenced inside macro definitions, which isn't the same
// thing as the module's BUILD files invoking them at the consumer
// build edge.
func (c *classifier) scan(f *syntax.File, relPath string, isBuild bool) {
	isTestPath := isTestOrExamplePath(relPath)
	// Per-file pre-pass: collect module-level `IDENT = literal_dict`
	// bindings so the integrity-hash detector can resolve
	// `sha256 = INTEGRITY[platform]` patterns (the bazel-lib release
	// shape) against a known table. File-local: cross-file resolution
	// isn't done here — sha256 dicts in a separate file would slip
	// past, which is acceptable since the pattern is overwhelmingly
	// same-file in real modules.
	pinnedDicts := collectPinnedDicts(f)
	syntax.Walk(f, func(n syntax.Node) bool {
		// Self-publish URL detection: scan every string literal in
		// the file. A literal containing both "/releases/download/"
		// and the (normalized) module name is the signature of a
		// module shipping its own binaries via GitHub releases. We
		// don't require the literal to be the immediate `url` arg of
		// a download() — the bazel-lib pattern binds the URL to a
		// local variable first and `.format()`s a version into it.
		if lit, ok := n.(*syntax.Literal); ok && !c.selfPublishesBinaries {
			if s, ok := lit.Value.(string); ok && looksLikeSelfPublishURL(s, c.moduleName) {
				c.selfPublishesBinaries = true
			}
		}
		call, ok := n.(*syntax.CallExpr)
		if !ok {
			return true
		}
		name := syntaxutil.IdentName(call.Fn)
		if name == "" {
			return true
		}
		if isBuild && !isTestPath && c.dialect.IsCompilationRuleSymbol(name) {
			// Heuristic: rule-name match against a curated list +
			// path-based test/example filtering + self-publish
			// demotion. All three layers are best-effort.
			c.findings = append(c.findings, report.HermeticityFinding{
				Class:      report.BuildFromSource,
				Symbol:     name,
				Reason:     "BUILD file invokes " + name + " — compiles source at consumer build time",
				Confidence: report.ConfidenceHeuristic,
				Provenance: syntaxutil.ProvenanceFrom(relPath, call),
			})
		}
		if c.dialect.IsNetworkFetchAPI(name) {
			pinned := hasIntegrityHashWithIndex(call, pinnedDicts, c.index, relPath)
			class := report.NetworkFetchPinned
			reason := "calls " + name + " with integrity hash pinned"
			// Definitive when sha256 was a literal we directly read;
			// heuristic when we conservatively assumed unpinned for a
			// non-literal expression (could be effectively pinned via
			// dict-subscript / lookup table — bazel-lib's pattern).
			confidence := report.ConfidenceDefinitive
			if !pinned {
				class = report.NetworkFetchUnpinned
				reason = "calls " + name + " without literal integrity hash (may still be effectively pinned)"
				confidence = report.ConfidenceHeuristic
			}
			c.findings = append(c.findings, report.HermeticityFinding{
				Class:      class,
				Symbol:     name,
				Reason:     reason,
				Confidence: confidence,
				Provenance: syntaxutil.ProvenanceFrom(relPath, call),
			})
			if pinned && syntaxutil.BoolKeywordArg(call, "executable") {
				// Definitive: both signals are literal AST shapes.
				c.findings = append(c.findings, report.HermeticityFinding{
					Class:      report.PrebuiltBinariesPinned,
					Symbol:     name,
					Reason:     "calls " + name + " with executable=True and pinned integrity hash",
					Confidence: report.ConfidenceDefinitive,
					Provenance: syntaxutil.ProvenanceFrom(relPath, call),
				})
			}
		}
		if c.dialect.IsSystemExecAPI(name) {
			class := c.classifyExec(call)
			// RequiresSystemTools: matched against curated binary list,
			// not exhaustive. RepositoryRuleArbitraryCode: fallback
			// when the executable argument shape isn't statically
			// resolvable. Both are heuristic by nature.
			c.findings = append(c.findings, report.HermeticityFinding{
				Class:      class,
				Symbol:     name,
				Reason:     "calls " + name + " — runs arbitrary commands",
				Confidence: report.ConfidenceHeuristic,
				Provenance: syntaxutil.ProvenanceFrom(relPath, call),
			})
		}
		return true
	})
}

// classifyExec returns RequiresSystemTools if the command appears to invoke
// a well-known system binary (docker, git, python, etc.), otherwise
// RepositoryRuleArbitraryCode.
func (c *classifier) classifyExec(call *syntax.CallExpr) report.HermeticityClass {
	// Heuristic: first positional argument is a list literal whose first
	// element is a string literal like "docker", "git", "python".
	if len(call.Args) == 0 {
		return report.RepositoryRuleArbitraryCode
	}
	list, ok := call.Args[0].(*syntax.ListExpr)
	if !ok || len(list.List) == 0 {
		return report.RepositoryRuleArbitraryCode
	}
	lit, ok := list.List[0].(*syntax.Literal)
	if !ok {
		return report.RepositoryRuleArbitraryCode
	}
	cmd, ok := lit.Value.(string)
	if !ok {
		return report.RepositoryRuleArbitraryCode
	}
	switch cmd {
	case "docker", "git", "python", "python3", "node", "npm", "yarn", "go", "cargo", "make", "cmake", "bash", "sh":
		return report.RequiresSystemTools
	}
	return report.RepositoryRuleArbitraryCode
}

// finalize collapses findings into the final HermeticityProfile,
// applying the self-publish BFS demotion if applicable.
//
// Demotion rule: when the module self-publishes binaries (URL match
// during scan) AND every BuildFromSource finding lives under a
// tools/-style release-tooling directory, drop those BFS findings
// entirely. That's the bazel-lib pattern — the source compiles
// release binaries the maintainer ships, NOT what consumers build.
//
// If even one BFS finding lives outside release-tooling paths
// (rules_lint sarif, rules_go gobuilder + runfiles, etc.), demotion
// is skipped and BFS keeps firing — the module ships consumer-
// facing source AND prebuilt binaries.
func (c *classifier) finalize() report.HermeticityProfile {
	findings := c.maybeDemoteBFS()
	if len(findings) == 0 {
		return report.HermeticityProfile{
			Classes: []report.HermeticityClass{report.PureStarlark},
		}
	}
	seen := map[report.HermeticityClass]bool{}
	var classes []report.HermeticityClass
	for _, f := range findings {
		if !seen[f.Class] {
			seen[f.Class] = true
			classes = append(classes, f.Class)
		}
	}
	return report.HermeticityProfile{
		Classes:  classes,
		Findings: findings,
	}
}

// maybeDemoteBFS returns findings with BFS entries dropped when the
// self-publish demotion rule fires.
func (c *classifier) maybeDemoteBFS() []report.HermeticityFinding {
	if !c.selfPublishesBinaries {
		return c.findings
	}
	hasBFS := false
	allBFSAreReleaseTooling := true
	for _, f := range c.findings {
		if f.Class != report.BuildFromSource {
			continue
		}
		hasBFS = true
		if !isReleaseToolingPath(f.Provenance.File) {
			allBFSAreReleaseTooling = false
			break
		}
	}
	if !hasBFS || !allBFSAreReleaseTooling {
		return c.findings
	}
	out := make([]report.HermeticityFinding, 0, len(c.findings))
	for _, f := range c.findings {
		if f.Class == report.BuildFromSource {
			continue
		}
		out = append(out, f)
	}
	return out
}

// looksLikeSelfPublishURL reports whether s is a string literal that
// resembles a GitHub-releases download URL pointing at the module's
// own releases. Heuristic: must contain "/releases/download/" AND a
// path segment matching the normalized module name (underscore <->
// hyphen accepted). Empty moduleName matches nothing — caller
// shouldn't call without one but be defensive.
//
// False positives are possible (e.g., a docstring URL pointing at
// some other repo whose name happens to share a substring), but the
// only consequence is demoting BFS findings on a module that's not
// actually self-publishing — which means the user sees "buildFromSource"
// missing for a module that does compile, a softer failure mode than
// over-firing on bazel-lib.
func looksLikeSelfPublishURL(s, moduleName string) bool {
	if moduleName == "" {
		return false
	}
	if !strings.Contains(s, "/releases/download/") {
		return false
	}
	// Normalize both directions so my_mod matches my-mod and vice versa.
	candidates := []string{moduleName}
	if strings.Contains(moduleName, "_") {
		candidates = append(candidates, strings.ReplaceAll(moduleName, "_", "-"))
	}
	if strings.Contains(moduleName, "-") {
		candidates = append(candidates, strings.ReplaceAll(moduleName, "-", "_"))
	}
	// Match as a path segment, not a substring — protects against
	// "mymod" being a substring of "notmymod-thing".
	for _, c := range candidates {
		if strings.Contains(s, "/"+c+"/") {
			return true
		}
	}
	return false
}

// isReleaseToolingPath reports whether path looks like maintainer
// release-pipeline tooling — currently meaning "under a tools/ or
// tooling/ directory at any depth." Used together with the
// selfPublishesBinaries signal to decide BFS demotion.
func isReleaseToolingPath(p string) bool {
	for seg := range strings.SplitSeq(filepath.ToSlash(p), "/") {
		if seg == "tools" || seg == "tooling" {
			return true
		}
	}
	return false
}

// isTestOrExamplePath is the local alias for the shared
// syntaxutil.IsTestOrExamplePath. Kept as a method-style call to
// avoid touching every callsite during the extraction; eventually
// inline to the syntaxutil form directly.
func isTestOrExamplePath(p string) bool {
	return syntaxutil.IsTestOrExamplePath(p)
}

// pinnedDictSet is the set of module-level identifier names that bind
// to literal dicts whose every value is a non-empty literal string.
// Subscript expressions whose base is one of these idents always
// resolve to a literal sha256, so they count as pinned just like a
// bare literal would.
type pinnedDictSet map[string]bool

// collectPinnedDicts projects [syntaxutil.CollectTopLevelDictBindings]
// through the all-non-empty-string filter. Anything more dynamic
// (function call, conditional, partial-literal dict) is rejected at
// either layer — pinning requires the WHOLE dict's value range to be
// literal strings.
func collectPinnedDicts(f *syntax.File) pinnedDictSet {
	dicts := syntaxutil.CollectTopLevelDictBindings(f)
	if dicts == nil {
		return nil
	}
	out := pinnedDictSet{}
	for name, dict := range dicts {
		if isAllNonEmptyStringDict(dict) {
			out[name] = true
		}
	}
	return out
}

// isAllNonEmptyStringDict returns true iff every entry in dict has a
// value that's a non-empty string literal. Empty entries don't count
// (they aren't pins); non-literal values disqualify the whole dict.
func isAllNonEmptyStringDict(dict *syntax.DictExpr) bool {
	if len(dict.List) == 0 {
		return false
	}
	for _, e := range dict.List {
		entry, ok := e.(*syntax.DictEntry)
		if !ok {
			return false
		}
		lit, ok := entry.Value.(*syntax.Literal)
		if !ok {
			return false
		}
		s, ok := lit.Value.(string)
		if !ok || s == "" {
			return false
		}
	}
	return true
}

// hasIntegrityHashWithIndex reports whether the call passes an
// integrity/sha256 keyword argument that statically resolves to a
// non-empty literal. Five accepted shapes (all definitive when matched):
//
//  1. `sha256 = "literal"` — the original case.
//  2. `sha256 = DICT_NAME[<anything>]` — subscript of a same-file
//     dict whose every value is a non-empty literal.
//  3. `sha256 = DICT_NAME.get(<anything>)` — same as (2) via `.get()`.
//  4. `sha256 = LOADED_DICT[<anything>]` — cross-file: LOADED_DICT
//     was load()-imported from another file in the module, and that
//     file's binding is an all-literal dict.
//  5. `sha256 = LOADED_DICT.get(<anything>)` — cross-file via .get().
//
// idx may be nil (no module-wide index). In that case only same-file
// resolution runs.
func hasIntegrityHashWithIndex(call *syntax.CallExpr, sameFile pinnedDictSet, idx *hermeticIndex, fromFile string) bool {
	for _, arg := range call.Args {
		bin, ok := arg.(*syntax.BinaryExpr)
		if !ok || bin.Op != syntax.EQ {
			continue
		}
		key, ok := bin.X.(*syntax.Ident)
		if !ok {
			continue
		}
		switch key.Name {
		case "integrity", "sha256":
			if integrityValueIsPinned(bin.Y, sameFile, idx, fromFile) {
				return true
			}
		}
	}
	return false
}

// integrityValueIsPinned reports whether an expression statically
// resolves to a non-empty string literal — directly, via same-file
// subscript, or via cross-file subscript through a load() chain.
func integrityValueIsPinned(expr syntax.Expr, sameFile pinnedDictSet, idx *hermeticIndex, fromFile string) bool {
	switch n := expr.(type) {
	case *syntax.Literal:
		s, ok := n.Value.(string)
		return ok && s != ""
	case *syntax.IndexExpr:
		base, ok := n.X.(*syntax.Ident)
		if !ok {
			return false
		}
		return identResolvesToPinnedDict(base.Name, sameFile, idx, fromFile)
	case *syntax.CallExpr:
		// DICT.get(key) — `.get()` shape only.
		dot, ok := n.Fn.(*syntax.DotExpr)
		if !ok || dot.Name.Name != "get" {
			return false
		}
		base, ok := dot.X.(*syntax.Ident)
		if !ok {
			return false
		}
		return identResolvesToPinnedDict(base.Name, sameFile, idx, fromFile)
	}
	return false
}

// identResolvesToPinnedDict reports whether name binds to an
// all-literal dict reachable from fromFile — either in the current
// file's bindings or via a load() chain into another file in the
// module. External loads (`@external//...`) bail cleanly.
func identResolvesToPinnedDict(name string, sameFile pinnedDictSet, idx *hermeticIndex, fromFile string) bool {
	if sameFile[name] {
		return true
	}
	if idx == nil {
		return false
	}
	file, ok := idx.perFile[fromFile]
	if !ok {
		return false
	}
	imp, ok := file.loads[name]
	if !ok {
		return false
	}
	target, ok := syntaxutil.ResolveLoadedFile(fromFile, imp.ModulePath)
	if !ok {
		return false
	}
	targetIdx, ok := idx.perFile[target]
	if !ok {
		return false
	}
	return targetIdx.pinnedDicts[imp.OriginalName]
}
