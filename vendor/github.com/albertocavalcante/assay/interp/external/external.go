// Package external is the canopy-shaped consumer API on top of
// starlark-go-bazel's analysis pipeline. Analyze walks a module's
// .bzl files, drives every captured repository_rule with Permissive
// load resolution + (os, arch) fork, classifies the captured URLs by
// ecosystem (maven / pypi / github-release / etc.), and returns
// deduplicated Ref records suitable for canopy's external_refs
// SQLite store.
//
// Provenance (file:line for each URL's origin) is best-effort at this
// layer: File is the .bzl path; Line/Symbol fields are populated by a
// follow-up that pipes assay.bzlwalk RuleSpec data into the Invoke
// path.
package external

import (
	"context"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/albertocavalcante/starlark-go-bazel/bzl"
	bazelctx "github.com/albertocavalcante/starlark-go-bazel/ctx"
	"github.com/albertocavalcante/starlark-go-bazel/eval"
	"github.com/albertocavalcante/starlark-go-bazel/stub"
	"github.com/albertocavalcante/starlark-go-bazel/taint"
	"github.com/albertocavalcante/starlark-go-bazel/types"
	"github.com/albertocavalcante/starlark-go-bazel/version"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// Ref is one external network reference (URL) extracted from a
// Bazel module's .bzl files. Schema mirrors plan 11's ExternalRef;
// canopy's external_refs SQLite table is shaped to absorb this.
type Ref struct {
	URL        string // canonicalized
	Host       string // lowercased
	Class      string // see classify.go (bcr / maven / pypi-canonical / etc.)
	Mutability string // immutable / mutable-host / unknown
	SHA256     string
	Integrity  string
	APIName    string // ctx.download / ctx.download_and_extract
	RuleName   string
	Platform   string // os/arch or "any"
	Tainted    bool
	File       string // module-relative
}

// Options configures Analyze. Zero values default to VLatest +
// DefaultPlatforms.
type Options struct {
	BazelVersion version.Version
	Platforms    []taint.Platform
	// ConcurrentFiles caps the goroutines used to evaluate .bzl files
	// in parallel. ≤0 uses runtime.NumCPU(). Files are independent (no
	// cross-file load chain — LoadResolver replaces it with Permissive
	// stubs), so the parallelism is safe.
	ConcurrentFiles int
	// RelevantFiles, if non-empty, restricts evaluation to these
	// workspace-relative paths. Use this to skip test/example .bzl
	// files that don't carry any repository_rule worth driving — the
	// caller (e.g. canopy ingest) typically derives the list from
	// assay's bzlwalk output. Empty means "walk every .bzl in tree."
	RelevantFiles []string
}

// Result aggregates Analyze's output: deduplicated refs + per-file
// eval errors + per-fork rule errors.
type Result struct {
	Refs       []Ref
	FileErrors []FileError
	ForkErrors []taint.ForkError
}

// FileError pairs a path with the error from evaluating it. Non-fatal
// to the overall Analyze run: subsequent files still process.
type FileError struct {
	File string
	Err  error
}

// Analyze walks workspaceRoot for .bzl files, evaluates each in
// analysis mode, drives every captured repository_rule with default
// attrs, classifies the URLs, and returns deduplicated Refs.
//
// Per-file eval failures + per-fork rule errors are collected in the
// Result; the overall call returns nil error unless workspaceRoot
// itself can't be walked.
func Analyze(ctx context.Context, workspaceRoot string, opts Options) (*Result, error) {
	if opts.BazelVersion == 0 {
		opts.BazelVersion = version.Latest()
	}

	var bzlFiles []string
	if len(opts.RelevantFiles) > 0 {
		// Caller pre-filtered — skip walking the tree.
		bzlFiles = make([]string, 0, len(opts.RelevantFiles))
		for _, rel := range opts.RelevantFiles {
			bzlFiles = append(bzlFiles, filepath.Join(workspaceRoot, rel))
		}
	} else {
		var err error
		bzlFiles, err = findBzlFiles(workspaceRoot)
		if err != nil {
			return nil, fmt.Errorf("walk %s: %w", workspaceRoot, err)
		}
	}

	// Read all files upfront so per-file work in the parallel loop is
	// pure CPU (no IO). Files are independent — no cross-file load
	// chain reaches across (LoadResolver replaces it with Permissive
	// stubs), so there's no eval-cache sharing benefit to fight against
	// concurrent execution.
	type fileEntry struct {
		rel, abs string
		src      []byte
	}
	entries := make([]fileEntry, 0, len(bzlFiles))
	result := &Result{}
	for _, bzlPath := range bzlFiles {
		src, err := os.ReadFile(bzlPath) //nolint:gosec // G304: bzlPath comes from the package's own directory walk.
		if err != nil {
			result.FileErrors = append(result.FileErrors, FileError{File: bzlPath, Err: err})
			continue
		}
		rel, relErr := filepath.Rel(workspaceRoot, bzlPath)
		if relErr != nil {
			rel = bzlPath
		}
		entries = append(entries, fileEntry{rel: rel, abs: bzlPath, src: src})
	}

	if len(entries) == 0 {
		return result, nil
	}

	concurrency := opts.ConcurrentFiles
	if concurrency <= 0 {
		concurrency = runtime.NumCPU()
	}
	if concurrency > len(entries) {
		concurrency = len(entries)
	}

	type fileResult struct {
		rel    string
		refs   []Ref
		ferrs  []taint.ForkError
		evalEr error
	}
	results := make([]fileResult, len(entries))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i := range entries {
		e := entries[i]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			refs, ferrs, err := analyzeFile(ctx, e.rel, e.abs, e.src, opts)
			results[i] = fileResult{rel: e.rel, refs: refs, ferrs: ferrs, evalEr: err}
		}()
	}
	wg.Wait()

	for _, r := range results {
		if r.evalEr != nil {
			result.FileErrors = append(result.FileErrors, FileError{File: r.rel, Err: r.evalEr})
			continue
		}
		result.Refs = append(result.Refs, r.refs...)
		result.ForkErrors = append(result.ForkErrors, r.ferrs...)
	}

	// Layer 1: MODULE.bazel.lock — deterministic, version-aware URL
	// extraction. When present, the lockfile's resolved repository
	// specs give us ground-truth URLs that didn't need static eval.
	// Tainted=false on every captured Ref; complements (doesn't
	// replace) the interpreted-eval output.
	if lock, err := readModuleLockfile(workspaceRoot); err == nil && lock != nil {
		result.Refs = append(result.Refs, lock.Refs...)
		if lock.Warning != "" {
			result.FileErrors = append(result.FileErrors, FileError{
				File: "MODULE.bazel.lock", Err: fmt.Errorf("%s", lock.Warning),
			})
		}
	} else if err != nil {
		result.FileErrors = append(result.FileErrors, FileError{
			File: "MODULE.bazel.lock", Err: err,
		})
	}

	result.Refs = dedupeRefs(result.Refs)
	return result, nil
}

// evalBzlSource parses + evaluates a single .bzl file with the
// canopy-shaped predeclared universe (json stubbed via Permissive)
// and Permissive load resolution. Single parse: passes the
// *syntax.File through EvalFromAST instead of letting ExecFile
// re-parse from source bytes. Shared by analyzeFile and
// DriveExtensionFromSource.
func evalBzlSource(filename string, src []byte, opts Options) (*bzl.Result, error) {
	parsed, err := syntax.LegacyFileOptions().Parse(filename, src, 0)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	symbols := stub.ScanLoads(parsed)
	resolver := stub.LoaderFor(symbols, nil)
	interp := bzl.New(bzl.Options{
		LoadResolver: resolver,
		Version:      opts.BazelVersion,
		PredeclaredBzl: starlark.StringDict{
			// json isn't in the default predeclared universe; sdk.bzl
			// and other rules reference it for parsing version lists.
			"json": stub.Shared,
		},
	})
	res, err := interp.EvalFromAST(filename, parsed)
	if err != nil {
		return nil, fmt.Errorf("eval: %w", err)
	}
	return res, nil
}

func analyzeFile(ctx context.Context, rel, abs string, src []byte, opts Options) ([]Ref, []taint.ForkError, error) {
	res, err := evalBzlSource(abs, src, opts)
	if err != nil {
		return nil, nil, err
	}

	var refs []Ref
	var ferrs []taint.ForkError

	for _, val := range res.Globals {
		switch v := val.(type) {
		case *types.RepositoryRuleClass:
			inv, err := eval.InvokeRepositoryRule(ctx, v, defaultsForRule(v), eval.InvokeOptions{
				Version:   opts.BazelVersion,
				Platforms: opts.Platforms,
			})
			if err != nil {
				continue
			}
			for _, u := range inv.URLs {
				refs = append(refs, makeRef(u, rel))
			}
			ferrs = append(ferrs, inv.ForkErrors...)

		case *types.ModuleExtensionClass:
			// Drive with a synthetic root ModuleSpec containing one
			// default tag instance per declared tag_class. Tag attrs
			// flow through the same Default-extraction path as repo
			// rule attrs. This is the conservative "first slice" —
			// for rulesets whose tag_classes don't pin defaults the
			// extension still runs but may produce empty URLs;
			// extending to consumer-corpus aggregation is a follow-up.
			specs := defaultModuleSpecsForExtension(v)
			inv, err := eval.InvokeModuleExtension(ctx, v, specs, eval.InvokeOptions{
				Version:   opts.BazelVersion,
				Platforms: opts.Platforms,
			})
			if err != nil {
				continue
			}
			for _, u := range inv.URLs {
				refs = append(refs, makeRef(u, rel))
			}
			ferrs = append(ferrs, inv.ForkErrors...)
		}
	}

	return refs, ferrs, nil
}

// defaultModuleSpecsForExtension synthesizes a single-element
// []ModuleSpec containing one default tag instance per declared
// tag_class. Used by Analyze to drive module_extension impls when
// the consumer's MODULE.bazel isn't available (the ingest layer is
// looking at the producer ruleset, not a consumer workspace).
//
// Each synthesized tag instance carries the tag_class's attr defaults
// (mirroring defaultsForRule). Tag classes with no attr defaults
// produce an empty-attr instance, which still triggers the extension's
// impl iteration over module_ctx.modules — sometimes enough to surface
// a hardcoded URL inside the impl that doesn't depend on tag content.
func defaultModuleSpecsForExtension(ext *types.ModuleExtensionClass) []bazelctx.ModuleSpec {
	if ext == nil {
		return nil
	}
	tagClasses := ext.TagClasses()
	tags := make(map[string][]bazelctx.TagInstance, len(tagClasses))
	for name, tc := range tagClasses {
		tags[name] = []bazelctx.TagInstance{{Attrs: defaultsForTagClass(tc)}}
	}
	return []bazelctx.ModuleSpec{{
		Name:    "_synthetic_root",
		Version: "0.0.0",
		IsRoot:  true,
		Tags:    tags,
	}}
}

// defaultsForTagClass mirrors defaultsForRule but for tag_class attr
// schemas. Tag classes that declare no attrs return an empty map; the
// extension impl still runs but `tag.X` accesses fall through to
// Permissive (taint-tracked).
func defaultsForTagClass(tc *types.TagClass) map[string]starlark.Value {
	if tc == nil {
		return nil
	}
	attrs := tc.Attrs()
	if len(attrs) == 0 {
		return nil
	}
	out := make(map[string]starlark.Value, len(attrs))
	for name, raw := range attrs {
		holder, ok := raw.(types.AttrDescriptorHolder)
		if !ok {
			continue
		}
		d := holder.Descriptor()
		if d == nil || d.Default == nil || d.Default == starlark.None {
			continue
		}
		out[name] = d.Default
	}
	return out
}

// defaultsForRule extracts each attr's Default value from the rule's
// captured attr schema, suitable for passing as the initial attrs map
// to eval.InvokeRepositoryRule. Without this, default-invocation runs
// with empty strings for every attr — and rules like
// `url = "https://x/{}".format(ctx.attr.version)` produce a useless
// "https://x/" capture instead of the real default URL.
//
// Attrs whose Default is nil (mandatory attrs the rule expects the
// caller to supply) are omitted; the synthetic ctx will return an
// empty string for them, matching the prior behavior.
func defaultsForRule(rule *types.RepositoryRuleClass) map[string]starlark.Value {
	if rule == nil || len(rule.Attrs()) == 0 {
		return nil
	}
	out := make(map[string]starlark.Value, len(rule.Attrs()))
	for name, raw := range rule.Attrs() {
		holder, ok := raw.(types.AttrDescriptorHolder)
		if !ok {
			continue
		}
		d := holder.Descriptor()
		if d == nil || d.Default == nil || d.Default == starlark.None {
			continue
		}
		out[name] = d.Default
	}
	return out
}

func makeRef(u taint.CapturedURL, file string) Ref {
	host := extractHost(u.URL)
	return Ref{
		URL:        u.URL,
		Host:       host,
		Class:      classifyHost(host, u.URL),
		Mutability: classifyMutability(u, host, u.URL),
		SHA256:     u.SHA256,
		Integrity:  u.Integrity,
		APIName:    u.APIName,
		RuleName:   u.RuleName,
		Platform:   u.Platform,
		Tainted:    u.Tainted,
		File:       file,
	}
}

func extractHost(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Host)
}

func findBzlFiles(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			// Skip hidden + Bazel scratch directories.
			if strings.HasPrefix(name, ".") || name == "bazel-out" ||
				name == "bazel-bin" || name == "bazel-testlogs" ||
				strings.HasPrefix(name, "bazel-") {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".bzl") {
			out = append(out, path)
		}
		return nil
	})
	return out, err
}

func dedupeRefs(refs []Ref) []Ref {
	type key struct{ url, platform, file string }
	seen := map[key]bool{}
	var out []Ref
	for _, r := range refs {
		k := key{r.URL, r.Platform, r.File}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, r)
	}
	return out
}
