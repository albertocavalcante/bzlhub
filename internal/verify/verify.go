// Package verify performs integrity + consistency checks over a local
// canopy mirror.
//
// While `bzlhub drift` answers "is my mirror in sync with upstream?",
// `bzlhub verify` answers "is my mirror's own state self-consistent?".
// A corrupted tarball, an out-of-sync SQLite index, an orphan blob —
// these are all things an operator wants to know about *before* a Bazel
// build trips over them at the worst possible moment.
//
// The package exposes a single entry point — Verify(ctx, opts) — which
// builds a state aggregate once (one filesystem walk + one DB dump) and
// runs each enabled check against that shared snapshot. Each check is a
// pure function (state) []Finding so they're trivially testable in
// isolation and ordering-independent.
package verify

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/albertocavalcante/bzlhub/internal/store"
)

// Severity tiers the findings: Errors block, Warnings need eyeballs,
// Info is "FYI, here's an oddity worth knowing about".
type Severity string

const (
	SevError   Severity = "error"
	SevWarning Severity = "warning"
	SevInfo    Severity = "info"
)

// Kind identifies which check produced a finding. Used by consumers
// (CI gates, MCP advice, UI filters) to route on category without
// pattern-matching free-text messages.
type Kind string

const (
	KindBlobIntegrity        Kind = "blob_integrity"
	KindBlobMissing          Kind = "blob_missing"
	KindSourceJSONSchema     Kind = "source_json_schema"
	KindModuleBazelPresent   Kind = "module_bazel_present"
	KindIndexMirrorAgreement Kind = "index_mirror_agreement"
	KindOrphanBlobs          Kind = "orphan_blobs"
	KindDeepReportMismatch   Kind = "deep_report_mismatch"
	KindScipMissing          Kind = "scip_missing"
)

// Finding is one issue surfaced by a check. Fields are wire-shaped
// (JSON-clean tags, omitempty discipline) so the same struct serves
// programmatic consumers and the human-readable text renderer.
type Finding struct {
	Kind     Kind           `json:"kind"`
	Severity Severity       `json:"severity"`
	Module   string         `json:"module,omitempty"`
	Version  string         `json:"version,omitempty"`
	Path     string         `json:"path,omitempty"`
	Message  string         `json:"message"`
	Fix      string         `json:"fix,omitempty"`
	Details  map[string]any `json:"details,omitempty"`
}

// Report is the structured result of a verify run.
type Report struct {
	MirrorRoot      string    `json:"mirror_root"`
	DBPath          string    `json:"db_path"`
	ModulesExamined int       `json:"modules_examined"`
	BlobsExamined   int       `json:"blobs_examined"`
	Findings        []Finding `json:"findings"`
	Errors          int       `json:"errors"`
	Warnings        int       `json:"warnings"`
	Info            int       `json:"info"`
	Elapsed         string    `json:"elapsed"`
}

// Options configures a verify run. Zero-valued Checks means "run all".
type Options struct {
	MirrorRoot string
	DBPath     string
	Deep       bool
	Checks     []Kind // if non-empty, only run these check kinds
}

// allChecks is the canonical ordering of the standard (non-deep) checks.
// Deep is conditionally appended when Options.Deep is set.
var allChecks = []Kind{
	KindBlobIntegrity,
	KindSourceJSONSchema,
	KindModuleBazelPresent,
	KindIndexMirrorAgreement,
	KindOrphanBlobs,
	KindScipMissing,
}

// Verify is the public entry point. Returns a populated Report on
// success; an error only when the tool itself can't do its job (e.g.,
// missing root, can't open DB). Per-finding problems live inside the
// Report — they don't surface as errors.
func Verify(ctx context.Context, opts Options) (*Report, error) {
	if opts.MirrorRoot == "" {
		return nil, errors.New("verify: MirrorRoot is required")
	}
	start := time.Now()

	// Optional DB: a mirror without an index is still verifiable for the
	// integrity/schema/module_bazel/orphan checks; only index_mirror
	// agreement degrades. Keeping the DB optional lets `bzlhub verify`
	// be useful against a freshly-rsync'd mirror that doesn't carry the
	// index file along with it.
	var st *store.Store
	if opts.DBPath != "" {
		s, err := store.Open(ctx, opts.DBPath)
		if err != nil {
			return nil, fmt.Errorf("verify: open db %s: %w", opts.DBPath, err)
		}
		defer s.Close()
		st = s
	}

	state, err := buildState(ctx, opts.MirrorRoot, st)
	if err != nil {
		return nil, fmt.Errorf("verify: build state: %w", err)
	}

	enabled := enabledChecks(opts.Checks)
	var findings []Finding
	for _, k := range allChecks {
		if !enabled[k] {
			continue
		}
		findings = append(findings, runCheck(k, state)...)
	}
	if opts.Deep {
		findings = append(findings, checkDeep(ctx, state)...)
	}

	r := &Report{
		MirrorRoot:      opts.MirrorRoot,
		DBPath:          opts.DBPath,
		ModulesExamined: len(state.modules),
		BlobsExamined:   len(state.blobs),
		Findings:        sortFindings(findings),
		Elapsed:         time.Since(start).Round(time.Millisecond).String(),
	}
	for _, f := range r.Findings {
		switch f.Severity {
		case SevError:
			r.Errors++
		case SevWarning:
			r.Warnings++
		case SevInfo:
			r.Info++
		}
	}
	return r, nil
}

// runCheck dispatches by Kind. Kept as a switch (rather than a map of
// funcs) so additions stay grep-able and the compiler enforces the
// kind→check pairing.
func runCheck(k Kind, st *state) []Finding {
	switch k {
	case KindBlobIntegrity:
		return checkBlobIntegrity(st)
	case KindSourceJSONSchema:
		return checkSourceJSONSchema(st)
	case KindModuleBazelPresent:
		return checkModuleBazelPresent(st)
	case KindIndexMirrorAgreement:
		return checkIndexMirrorAgreement(st)
	case KindOrphanBlobs:
		return checkOrphanBlobs(st)
	case KindScipMissing:
		return checkScipPresent(st)
	}
	return nil
}

// enabledChecks resolves the "if Checks is empty, run all" rule into a
// concrete set for fast lookup.
func enabledChecks(req []Kind) map[Kind]bool {
	out := map[Kind]bool{}
	if len(req) == 0 {
		for _, k := range allChecks {
			out[k] = true
		}
		return out
	}
	for _, k := range req {
		out[k] = true
	}
	return out
}

// sortFindings stabilizes the wire order: severity (error → warning →
// info), then kind, then module, then version, then path. Deterministic
// output makes test assertions easier and lets the text renderer group
// related findings together without an extra pass.
func sortFindings(fs []Finding) []Finding {
	sevRank := func(s Severity) int {
		switch s {
		case SevError:
			return 0
		case SevWarning:
			return 1
		case SevInfo:
			return 2
		}
		return 3
	}
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if ra, rb := sevRank(a.Severity), sevRank(b.Severity); ra != rb {
			return ra < rb
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Module != b.Module {
			return a.Module < b.Module
		}
		if a.Version != b.Version {
			return a.Version < b.Version
		}
		return a.Path < b.Path
	})
	return fs
}
