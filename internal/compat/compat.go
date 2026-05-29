// Package compat is canopy's compatibility analyzer (A1 — the
// killer feature that distinguishes canopy from peer registries).
//
// Input: a MODULE.bazel text blob from the caller.
// Output: a per-dep migration report telling the caller "if you bump
// each dep to the latest indexed version, here's what breaks and how
// to fix it."
//
// The analyzer is pure: it reads from a ReportSource (which the
// service layer plugs as canopy.Service) and never reaches out to
// the network. The MODULE.bazel input is parsed in-process via
// go-bzlmod; no Starlark evaluation, no URL fetches, no shelling out.
//
// Security knobs are enforced at the caller boundary (size cap, rate
// limit) — this package trusts that the body it gets is bounded.
package compat

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/albertocavalcante/assay/report"
	bzlmod "github.com/albertocavalcante/go-bzlmod"

	"github.com/albertocavalcante/canopy/internal/modulediff"
)

// ReportSource is the read-only view of canopy's index the analyzer
// needs. *canopy.Service satisfies it via its existing methods,
// keeping this package decoupled from the broader service surface.
type ReportSource interface {
	// LatestVersion returns the highest indexed version for a module,
	// or "" when the module isn't in the corpus. Stub versions
	// ("", "0", "0.0.0") are skipped — they're placeholder ingests
	// without a real release behind them.
	LatestVersion(ctx context.Context, name string) (string, error)
	// GetReport returns the structured report for one (name, version).
	// (nil, nil) when the pair isn't indexed; callers degrade
	// gracefully rather than erroring.
	GetReport(ctx context.Context, name, version string) (*report.ModuleReport, error)
}

// Options gates the analyzer. Today there are no knobs beyond the
// implicit "use the indexed latest" target; future shapes (pin to a
// specific target version, ignore dev_dependency, etc.) land here.
type Options struct {
	// IncludeDevDependencies controls whether bazel_dep(dev_dependency = True)
	// entries appear in the report. Default false matches the typical
	// "is my prod build going to break?" question.
	IncludeDevDependencies bool
}

// Result is the analyzer's output. Per-dep entries are sorted by
// breaking-count DESC then name ASC so the worst offenders surface
// at the top — same default ordering the UI applies.
type Result struct {
	// Self is the analyzed MODULE.bazel's own (name, version) when
	// the input declared a `module(name = "...", version = "...")`.
	// Empty fields when the block is absent or malformed.
	Self SelfInfo `json:"self"`
	Deps []DepEntry `json:"deps"`
	// Summary aggregates per-dep counts; cheap for the UI to render
	// without re-walking the report.
	Summary Summary `json:"summary"`
	// PlanMarkdown is a ready-to-paste migration plan. The frontend
	// can render it directly; agents can stuff it into a PR.
	PlanMarkdown string `json:"plan_markdown"`
	// PlanShell is a ready-to-pipe `migrate.sh` bash script that
	// applies every Buildozer codemod the analyzer was able to
	// derive from the BreakingFindings. Empty when no findings carry
	// a codemod (rare — typically discovery commands are still
	// emitted). The script runs in --dry-run mode by default; see
	// docs/plans/06-buildozer-codemods.md for safety messaging.
	PlanShell string `json:"plan_shell,omitempty"`
}

// SelfInfo identifies the analyzed module itself, when the input
// declared one. Optional — many MODULE.bazel inputs to the analyzer
// will be from consumer projects that don't publish their own module.
type SelfInfo struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

// DepEntry is one row of the report: a single bazel_dep and what
// happens when it gets bumped.
type DepEntry struct {
	Name        string `json:"name"`
	FromVersion string `json:"from_version"`         // pinned in input
	ToVersion   string `json:"to_version,omitempty"` // latest in corpus; empty when InCorpus is false
	// InCorpus reports whether canopy's index has the module at all.
	// False entries can't be diffed but still appear in the result so
	// the UI can prompt the operator to ingest them.
	InCorpus      bool                       `json:"in_corpus"`
	// SameVersion reports whether FromVersion == ToVersion (already on
	// latest). UI hides the dep from the "needs attention" view but
	// keeps it in the full list for completeness.
	SameVersion   bool                       `json:"same_version,omitempty"`
	FromIndexed   bool                       `json:"from_indexed"`            // pinned version present in index
	BreakingCount int                        `json:"breaking_count"`          // 0 when from→to is clean
	Report        *modulediff.Report         `json:"report,omitempty"`        // nil when InCorpus=false or no change
	// Findings is a stable-sorted copy of Report.Breaking. Pulled up
	// to the entry level so consumers can render summaries without
	// dereferencing nested fields.
	Findings      []modulediff.BreakingFinding `json:"findings,omitempty"`
}

// Summary is the at-a-glance counter the UI's banner reads.
type Summary struct {
	TotalDeps         int `json:"total_deps"`
	BreakingDeps      int `json:"breaking_deps"`       // deps with >= 1 breaking finding
	MissingFromCorpus int `json:"missing_from_corpus"` // deps not indexed in canopy
	AlreadyLatest     int `json:"already_latest"`      // deps pinned to the latest indexed version
}

// ErrEmptyInput is returned when the body parses to zero bazel_deps
// (typically an empty file or an MODULE.bazel that's purely a
// `module(...)` declaration with no deps). The caller renders a
// friendly "nothing to analyze" UI instead of an error toast.
var ErrEmptyInput = errors.New("compat: no bazel_dep declarations found in input")

// Analyze parses the MODULE.bazel content, looks each declared
// bazel_dep up in the index, and computes a structural diff against
// the latest indexed version. Returns ErrEmptyInput when the input
// has no bazel_deps. All other "missing data" cases (dep not in
// corpus, no newer version) produce entries in the result rather
// than errors — the report is still useful when partial.
func Analyze(ctx context.Context, src ReportSource, body string, opts Options) (*Result, error) {
	if src == nil {
		return nil, errors.New("compat: nil ReportSource")
	}
	info, err := bzlmod.ParseModuleContent(body)
	if err != nil {
		return nil, fmt.Errorf("parse MODULE.bazel: %w", err)
	}
	if info == nil {
		return nil, errors.New("compat: parse returned nil ModuleInfo")
	}

	// Filter dev deps unless explicitly requested. Default matches
	// "is my prod build going to break?" — dev deps are
	// build-tooling adjacencies whose drift the consumer can address
	// later.
	var deps []bzlmod.Dependency
	for _, d := range info.Dependencies {
		if d.Name == "" || d.Version == "" {
			continue
		}
		if d.DevDependency && !opts.IncludeDevDependencies {
			continue
		}
		deps = append(deps, d)
	}
	if len(deps) == 0 {
		return nil, ErrEmptyInput
	}

	out := &Result{
		Self: SelfInfo{Name: info.Name, Version: info.Version},
		Deps: make([]DepEntry, 0, len(deps)),
	}

	for _, d := range deps {
		entry := DepEntry{
			Name:        d.Name,
			FromVersion: d.Version,
		}

		latest, err := src.LatestVersion(ctx, d.Name)
		if err != nil || latest == "" {
			// Not in corpus (or transient store error treated the
			// same — the user sees "ingest me first"). Record and
			// move on.
			out.Deps = append(out.Deps, entry)
			continue
		}
		entry.InCorpus = true
		entry.ToVersion = latest
		entry.SameVersion = latest == d.Version
		if entry.SameVersion {
			// No diff to compute. Still record so the consumer sees
			// "rules_go is already on latest" rather than wondering
			// if we skipped it.
			out.Deps = append(out.Deps, entry)
			continue
		}

		fromReport, _ := src.GetReport(ctx, d.Name, d.Version)
		entry.FromIndexed = fromReport != nil

		toReport, _ := src.GetReport(ctx, d.Name, latest)
		if fromReport == nil || toReport == nil {
			// One side missing — common when consumers pin a version
			// canopy hasn't ingested yet. Surface the bump
			// recommendation without breaking-finding detail.
			out.Deps = append(out.Deps, entry)
			continue
		}

		rep := modulediff.Compute(fromReport, toReport)
		rep.Module = d.Name
		rep.From = d.Version
		rep.To = latest
		entry.Report = rep
		entry.Findings = append(entry.Findings, rep.Breaking...)
		entry.BreakingCount = len(rep.Breaking)
		out.Deps = append(out.Deps, entry)
	}

	// Stable ordering: breaking-count DESC, then alphabetical. The
	// UI's "show breaking only" filter and the markdown plan both
	// rely on this; recomputing sort downstream would risk drift.
	sort.SliceStable(out.Deps, func(i, j int) bool {
		if out.Deps[i].BreakingCount != out.Deps[j].BreakingCount {
			return out.Deps[i].BreakingCount > out.Deps[j].BreakingCount
		}
		return out.Deps[i].Name < out.Deps[j].Name
	})

	for _, e := range out.Deps {
		out.Summary.TotalDeps++
		switch {
		case !e.InCorpus:
			out.Summary.MissingFromCorpus++
		case e.SameVersion:
			out.Summary.AlreadyLatest++
		}
		if e.BreakingCount > 0 {
			out.Summary.BreakingDeps++
		}
	}

	out.PlanMarkdown = renderPlan(out)
	out.PlanShell = renderShell(out)
	return out, nil
}

// renderPlan formats the analyzer result as a paste-ready markdown
// migration plan. Pure rendering: no I/O, deterministic output for
// stable diffs across re-analyses.
func renderPlan(r *Result) string {
	var b strings.Builder
	b.WriteString("# Compatibility report\n\n")
	if r.Self.Name != "" {
		fmt.Fprintf(&b, "Analyzed: `%s", r.Self.Name)
		if r.Self.Version != "" {
			fmt.Fprintf(&b, "@%s", r.Self.Version)
		}
		b.WriteString("`\n\n")
	}

	fmt.Fprintf(&b, "- **%d** dep%s analyzed\n", r.Summary.TotalDeps, pluralS(r.Summary.TotalDeps))
	if r.Summary.BreakingDeps > 0 {
		fmt.Fprintf(&b, "- **%d** with breaking changes\n", r.Summary.BreakingDeps)
	}
	if r.Summary.MissingFromCorpus > 0 {
		fmt.Fprintf(&b, "- **%d** not yet ingested in canopy\n", r.Summary.MissingFromCorpus)
	}
	if r.Summary.AlreadyLatest > 0 {
		fmt.Fprintf(&b, "- **%d** already on latest\n", r.Summary.AlreadyLatest)
	}
	b.WriteString("\n")

	// Per-dep sections. Skip clean bumps from the markdown — they
	// add noise without action items. Same-version and
	// missing-from-corpus get a one-liner each in a bookkeeping
	// section at the end.
	wroteAny := false
	for _, e := range r.Deps {
		if e.BreakingCount == 0 {
			continue
		}
		wroteAny = true
		fmt.Fprintf(&b, "## `%s`: `%s` → `%s` (%d breaking)\n\n",
			e.Name, e.FromVersion, e.ToVersion, e.BreakingCount)
		for _, f := range e.Findings {
			label := string(f.Kind)
			if f.Symbol != "" {
				label = fmt.Sprintf("%s `%s`", f.Kind, f.Symbol)
				if f.Detail != "" {
					label = fmt.Sprintf("%s `%s` · `%s`", f.Kind, f.Symbol, f.Detail)
				}
			}
			fmt.Fprintf(&b, "- **%s** — %s\n", label, f.Reason)
			if f.Hint != "" {
				fmt.Fprintf(&b, "  - %s\n", f.Hint)
			}
		}
		b.WriteString("\n")
	}

	var notIngested []string
	for _, e := range r.Deps {
		if !e.InCorpus {
			notIngested = append(notIngested, e.Name+"@"+e.FromVersion)
		}
	}
	if len(notIngested) > 0 {
		b.WriteString("## Not in canopy index\n\n")
		b.WriteString("These deps aren't ingested yet — analyzer can't compare against a known-good version.\n\n")
		for _, x := range notIngested {
			fmt.Fprintf(&b, "- `%s`\n", x)
		}
		b.WriteString("\n")
	}

	if !wroteAny && len(notIngested) == 0 {
		b.WriteString("No breaking changes detected — every dep is on the latest indexed version or its bump is clean.\n")
	}
	return b.String()
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
