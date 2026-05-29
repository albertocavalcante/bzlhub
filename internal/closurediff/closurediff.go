// Package closurediff extends per-module diff into a recursive
// closure-wide diff: given a root (module, from, to), it walks the
// bazel_dep closure on each side using MVS (via gobzlmod.ResolveModule)
// and emits a structured report covering:
//
//   - dep-set shape changes (added / removed / version-changed deps)
//   - per-module modulediff for every dep whose version moved
//   - aggregated breaking-change rollup across the whole closure
//
// The breaking rollup is the killer field: a bump of X@A → X@B doesn't
// just change X — it pulls in different versions of its transitive
// deps, each with their own potentially-breaking surface. CI can now
// gate on the closure-wide count, not just the root.
package closurediff

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	gobzlmod "github.com/albertocavalcante/go-bzlmod"
	"github.com/albertocavalcante/assay/report"

	"github.com/albertocavalcante/canopy/internal/modulediff"
)

// Options configures a closure diff.
type Options struct {
	Module      string // root module name
	FromVersion string
	ToVersion   string
	Upstream    string // BCR-shape registry URL; required (MVS needs a registry)
	Workers     int    // concurrent fetch + analyze; default 4

	// AnalyzeFunc fetches+analyzes one (module, version) and returns
	// the report. The caller supplies this so the package stays free of
	// store/mirror coupling. Errors are surfaced to the report's
	// per-module ErrorByModule map; one failure doesn't abort the walk.
	AnalyzeFunc func(ctx context.Context, module, version string) (*report.ModuleReport, error)
}

// Report is the wire shape: closure-level dep moves plus per-module
// diffs for modules whose version changed.
type Report struct {
	Module string `json:"module"`
	From   string `json:"from"`
	To     string `json:"to"`

	// FromClosureSize / ToClosureSize: total module count in each
	// closure (including root). Useful as a sanity-check number for the
	// reader ("rules_python 0.30→0.40 went from 28 to 33 modules").
	FromClosureSize int `json:"from_closure_size"`
	ToClosureSize   int `json:"to_closure_size"`

	// ClosureDeps describes how the dep SET (by module name) shifted.
	ClosureDeps ClosureDepsDiff `json:"closure_deps"`

	// ModuleDiffs holds a full modulediff.Report for every module
	// (including the root) whose VERSION changed between from and to.
	// Modules added/removed at the closure level are not here (no pair
	// to diff). Keyed by module name.
	ModuleDiffs map[string]*modulediff.Report `json:"module_diffs,omitempty"`

	// ErrorByModule records modules whose analysis failed during the
	// walk. The walk doesn't abort on these — they're surfaced for the
	// reviewer to chase.
	ErrorByModule map[string]string `json:"errors_by_module,omitempty"`

	// ClosureBreakingTotal is the SUM of all breaking findings across
	// every per-module diff. Headline number for CI / drift verdict.
	ClosureBreakingTotal int `json:"closure_breaking_total"`

	// ClosureBreakingByModule lets reviewers see which modules in the
	// closure contribute breakage. Only modules with >0 breaking
	// findings appear here. Sorted alphabetically when serialized as
	// JSON because Go maps marshal in random order — the CLI/UI sort
	// for display.
	ClosureBreakingByModule map[string]int `json:"closure_breaking_by_module,omitempty"`
}

// ClosureDepsDiff: which modules appeared / disappeared / moved versions
// between the from and to closures.
type ClosureDepsDiff struct {
	Added   []report.ModuleKey   `json:"added,omitempty"`
	Removed []report.ModuleKey   `json:"removed,omitempty"`
	Changed []ChangedClosureDep  `json:"changed,omitempty"`
}

// ChangedClosureDep is a module that's in both closures but at a
// different version.
type ChangedClosureDep struct {
	Name        string `json:"name"`
	FromVersion string `json:"from_version"`
	ToVersion   string `json:"to_version"`
}

// Compute is the entry point. Walks both closures, builds the shape
// diff, runs per-module diffs in parallel, and rolls up breaking
// findings. Returns a non-nil report unless closure resolution itself
// fails.
func Compute(ctx context.Context, opts Options) (*Report, error) {
	if opts.Module == "" || opts.FromVersion == "" || opts.ToVersion == "" {
		return nil, errors.New("closurediff: module, from, and to are required")
	}
	if opts.Upstream == "" {
		return nil, errors.New("closurediff: upstream registry URL is required (MVS needs one)")
	}
	if opts.AnalyzeFunc == nil {
		return nil, errors.New("closurediff: AnalyzeFunc is required")
	}
	workers := opts.Workers
	if workers <= 0 {
		workers = 4
	}

	fromClosure, err := walkClosure(ctx, opts.Module, opts.FromVersion, opts.Upstream)
	if err != nil {
		return nil, fmt.Errorf("walk from-closure %s@%s: %w", opts.Module, opts.FromVersion, err)
	}
	toClosure, err := walkClosure(ctx, opts.Module, opts.ToVersion, opts.Upstream)
	if err != nil {
		return nil, fmt.Errorf("walk to-closure %s@%s: %w", opts.Module, opts.ToVersion, err)
	}

	r := &Report{
		Module:                  opts.Module,
		From:                    opts.FromVersion,
		To:                      opts.ToVersion,
		FromClosureSize:         len(fromClosure),
		ToClosureSize:           len(toClosure),
		ClosureDeps:             shapeClosureDeps(fromClosure, toClosure),
		ModuleDiffs:             map[string]*modulediff.Report{},
		ErrorByModule:           map[string]string{},
		ClosureBreakingByModule: map[string]int{},
	}

	// For each module that exists in both closures at different
	// versions (including the root if from != to, which it always is),
	// run a modulediff. Fan out to `workers` goroutines.
	type job struct{ name, from, to string }
	var jobs []job
	for _, c := range r.ClosureDeps.Changed {
		jobs = append(jobs, job{c.Name, c.FromVersion, c.ToVersion})
	}

	var mu sync.Mutex
	jobsCh := make(chan job)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Go(func() {
			for j := range jobsCh {
				diff, err := perModuleDiff(ctx, j.name, j.from, j.to, opts.AnalyzeFunc)
				mu.Lock()
				if err != nil {
					r.ErrorByModule[j.name] = err.Error()
				} else {
					r.ModuleDiffs[j.name] = diff
					if n := len(diff.Breaking); n > 0 {
						r.ClosureBreakingByModule[j.name] = n
						r.ClosureBreakingTotal += n
					}
				}
				mu.Unlock()
			}
		})
	}
	for _, j := range jobs {
		select {
		case jobsCh <- j:
		case <-ctx.Done():
			close(jobsCh)
			wg.Wait()
			return nil, ctx.Err()
		}
	}
	close(jobsCh)
	wg.Wait()

	if len(r.ErrorByModule) == 0 {
		r.ErrorByModule = nil
	}
	if len(r.ClosureBreakingByModule) == 0 {
		r.ClosureBreakingByModule = nil
	}
	if len(r.ModuleDiffs) == 0 {
		r.ModuleDiffs = nil
	}
	return r, nil
}

// walkClosure runs MVS-based resolution against the given upstream and
// returns the set of selected (name → version). The root itself is
// included in the returned map.
func walkClosure(ctx context.Context, module, version, upstream string) (map[string]string, error) {
	res, err := gobzlmod.Resolve(ctx,
		gobzlmod.RegistrySource{Name: module, Version: version},
		gobzlmod.WithRegistries(upstream),
	)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(res.Modules))
	for _, m := range res.Modules {
		// gobzlmod.ResolveModule pre-prunes by-name in MVS, so we expect
		// at most one entry per name; collapse defensively anyway.
		if existing, ok := out[m.Name]; ok && existing != m.Version {
			// Take the higher one. Lex compare is good enough for the
			// detection case — gobzlmod has already done the real MVS.
			if m.Version > existing {
				out[m.Name] = m.Version
			}
			continue
		}
		out[m.Name] = m.Version
	}
	return out, nil
}

// shapeClosureDeps compares two closures by module name and produces the
// added/removed/changed buckets. The root module always appears in
// Changed (from != to), so the per-module-diff loop covers it too.
func shapeClosureDeps(from, to map[string]string) ClosureDepsDiff {
	var d ClosureDepsDiff
	for name, ver := range to {
		fv, ok := from[name]
		if !ok {
			d.Added = append(d.Added, report.ModuleKey{Name: name, Version: ver})
			continue
		}
		if fv != ver {
			d.Changed = append(d.Changed, ChangedClosureDep{Name: name, FromVersion: fv, ToVersion: ver})
		}
	}
	for name, ver := range from {
		if _, ok := to[name]; !ok {
			d.Removed = append(d.Removed, report.ModuleKey{Name: name, Version: ver})
		}
	}
	sort.Slice(d.Added, func(i, j int) bool { return d.Added[i].Name < d.Added[j].Name })
	sort.Slice(d.Removed, func(i, j int) bool { return d.Removed[i].Name < d.Removed[j].Name })
	sort.Slice(d.Changed, func(i, j int) bool { return d.Changed[i].Name < d.Changed[j].Name })
	return d
}

// perModuleDiff fetches+analyzes both sides via the caller-supplied
// AnalyzeFunc and runs modulediff.Compute. Returns nil + nil if either
// side can't be analyzed AND the caller would want to know about the
// gap (we surface gaps via ErrorByModule, never silent skips).
func perModuleDiff(ctx context.Context, name, from, to string, analyze func(context.Context, string, string) (*report.ModuleReport, error)) (*modulediff.Report, error) {
	fr, err := analyze(ctx, name, from)
	if err != nil {
		return nil, fmt.Errorf("analyze %s@%s: %w", name, from, err)
	}
	tr, err := analyze(ctx, name, to)
	if err != nil {
		return nil, fmt.Errorf("analyze %s@%s: %w", name, to, err)
	}
	return modulediff.Compute(fr, tr), nil
}
