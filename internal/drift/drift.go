// Package drift compares a local canopy mirror against an upstream
// BCR-shape registry and surfaces divergence: new versions available,
// versions we have that were yanked upstream, modules we don't have at all,
// and "local-only" entries (we hold something upstream doesn't, possibly a
// canopy-published variant).
//
// The library only diffs modules already present in the mirror — it does
// NOT walk all of upstream BCR's catalog. That's a future feature; for now,
// drift answers "for the things I'm mirroring, am I behind?"
package drift

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/albertocavalcante/canopy/internal/fetch"
)

// Status is the high-level state of one module entry in the report.
type Status string

const (
	// InSync — local versions are a subset of upstream, latest local equals latest upstream,
	// none of ours were yanked upstream.
	InSync Status = "in-sync"

	// Behind — upstream's latest is newer than ours.
	Behind Status = "behind"

	// YankedUpstream — at least one version we hold was yanked upstream.
	YankedUpstream Status = "yanked-upstream"

	// LocalOnly — module not present upstream at all (private / canopy-published).
	LocalOnly Status = "local-only"

	// UpstreamError — couldn't fetch upstream metadata; reason in Error.
	UpstreamError Status = "upstream-error"
)

// ModuleDrift describes the divergence for one module.
type ModuleDrift struct {
	Name               string   `json:"name"`
	Status             Status   `json:"status"`
	LocalVersions      []string `json:"local_versions"`
	UpstreamVersions   []string `json:"upstream_versions,omitempty"`
	LocalLatest        string   `json:"local_latest,omitempty"`
	UpstreamLatest     string   `json:"upstream_latest,omitempty"`
	NewerUpstream      []string `json:"newer_upstream,omitempty"`      // versions upstream has that we don't AND are newer than our latest
	MissingLocally     []string `json:"missing_locally,omitempty"`     // ALL upstream-only versions (includes older if any)
	YankedAtUpstream   []string `json:"yanked_at_upstream,omitempty"`  // versions we hold that upstream yanked
	LocalOnlyVersions  []string `json:"local_only_versions,omitempty"` // versions we hold upstream doesn't (may be canopy-published)
	Error              string   `json:"error,omitempty"`
}

// Report is the full mirror drift report.
type Report struct {
	UpstreamURL string         `json:"upstream_url"`
	MirrorRoot  string         `json:"mirror_root"`
	Modules     []ModuleDrift  `json:"modules"`
	Summary     ReportSummary  `json:"summary"`
}

// ReportSummary tallies module statuses.
type ReportSummary struct {
	Total          int `json:"total"`
	InSync         int `json:"in_sync"`
	Behind         int `json:"behind"`
	YankedUpstream int `json:"yanked_upstream"`
	LocalOnly      int `json:"local_only"`
	UpstreamError  int `json:"upstream_error"`
}

// Options configures drift computation.
type Options struct {
	// Module, if non-empty, limits the scan to that one module name.
	Module string

	// Workers controls concurrent upstream fetches. 0 → 4.
	Workers int
}

// Compute walks the local mirror at mirrorRoot, then for each (or just one
// via opts.Module) module fetches upstream metadata.json from upstreamURL
// and produces a drift report.
func Compute(ctx context.Context, mirrorRoot, upstreamURL string, opts Options) (*Report, error) {
	modules, err := readMirrorModules(mirrorRoot)
	if err != nil {
		return nil, err
	}
	if opts.Module != "" {
		modules = filterModules(modules, opts.Module)
	}
	if opts.Workers <= 0 {
		opts.Workers = 4
	}

	client := fetch.NewClient()
	results := make([]ModuleDrift, len(modules))

	sem := make(chan struct{}, opts.Workers)
	var wg sync.WaitGroup
	for i, mod := range modules {
		i, mod := i, mod
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = compareOne(ctx, client, upstreamURL, mod)
		}()
	}
	wg.Wait()

	r := &Report{
		UpstreamURL: upstreamURL,
		MirrorRoot:  mirrorRoot,
		Modules:     results,
	}
	r.Summary = summarize(results)
	// Stable presentation order: most-divergent first, then alpha.
	sort.Slice(r.Modules, func(i, j int) bool {
		ri, rj := r.Modules[i], r.Modules[j]
		if statusRank(ri.Status) != statusRank(rj.Status) {
			return statusRank(ri.Status) > statusRank(rj.Status)
		}
		return ri.Name < rj.Name
	})
	return r, nil
}

// localModule captures what the on-disk metadata.json says we have.
type localModule struct {
	Name     string
	Versions []string
}

// readMirrorModules walks mirrorRoot/modules/*/metadata.json. Modules missing
// metadata.json (perhaps manually-published variants) are still listed via
// their version subdirectories.
func readMirrorModules(mirrorRoot string) ([]localModule, error) {
	modulesDir := filepath.Join(mirrorRoot, "modules")
	entries, err := os.ReadDir(modulesDir)
	if err != nil {
		return nil, fmt.Errorf("read mirror modules dir: %w", err)
	}
	var out []localModule
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		lm := localModule{Name: e.Name()}
		metaPath := filepath.Join(modulesDir, lm.Name, "metadata.json")
		if b, err := os.ReadFile(metaPath); err == nil {
			var m struct {
				Versions []string `json:"versions"`
			}
			if json.Unmarshal(b, &m) == nil {
				lm.Versions = m.Versions
			}
		}
		// Backfill from on-disk version dirs if metadata.json was missing or empty.
		if len(lm.Versions) == 0 {
			vDirs, _ := os.ReadDir(filepath.Join(modulesDir, lm.Name))
			for _, v := range vDirs {
				if v.IsDir() {
					lm.Versions = append(lm.Versions, v.Name())
				}
			}
		}
		sort.Strings(lm.Versions)
		out = append(out, lm)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func filterModules(in []localModule, name string) []localModule {
	for _, m := range in {
		if m.Name == name {
			return []localModule{m}
		}
	}
	return nil
}

func compareOne(ctx context.Context, client *fetch.Client, upstreamURL string, local localModule) ModuleDrift {
	d := ModuleDrift{
		Name:          local.Name,
		LocalVersions: append([]string(nil), local.Versions...),
		LocalLatest:   pickLatest(local.Versions),
	}

	up, err := client.GetMetadata(ctx, upstreamURL, local.Name)
	if err != nil {
		// Distinguish "module doesn't exist upstream" from real
		// network errors. fetch.ErrNotFound is the typed sentinel for
		// 404s — switching off this rather than string-matching
		// "HTTP 404" keeps drift detection robust against changes to
		// the fetch package's error formatting.
		if errors.Is(err, fetch.ErrNotFound) {
			d.Status = LocalOnly
			d.LocalOnlyVersions = append([]string(nil), local.Versions...)
			return d
		}
		d.Status = UpstreamError
		d.Error = err.Error()
		return d
	}
	d.UpstreamVersions = append([]string(nil), up.Versions...)
	d.UpstreamLatest = pickLatest(up.Versions)

	// Set diffs.
	localSet := toSet(local.Versions)
	upSet := toSet(up.Versions)
	for _, v := range up.Versions {
		if !localSet[v] {
			d.MissingLocally = append(d.MissingLocally, v)
		}
	}
	for _, v := range local.Versions {
		if !upSet[v] {
			d.LocalOnlyVersions = append(d.LocalOnlyVersions, v)
		}
		if _, yanked := up.YankedVersions[v]; yanked {
			d.YankedAtUpstream = append(d.YankedAtUpstream, v)
		}
	}
	for _, v := range d.MissingLocally {
		if compareVersions(v, d.LocalLatest) > 0 {
			d.NewerUpstream = append(d.NewerUpstream, v)
		}
	}

	switch {
	case len(d.YankedAtUpstream) > 0:
		d.Status = YankedUpstream
	case len(d.NewerUpstream) > 0:
		d.Status = Behind
	default:
		d.Status = InSync
	}
	return d
}

// pickLatest returns the lexicographically/numerically largest version under
// a best-effort Bazel-version-style comparator.
func pickLatest(versions []string) string {
	if len(versions) == 0 {
		return ""
	}
	best := versions[0]
	for _, v := range versions[1:] {
		if compareVersions(v, best) > 0 {
			best = v
		}
	}
	return best
}

// compareVersions returns -1/0/1 like strings.Compare but treats each
// dot-separated segment as a number when possible. Good enough for BCR
// versions like "1.7.1", "1.8.2", "0.0.10" — not a full Bazel Version.java
// implementation but matches the common cases we care about.
func compareVersions(a, b string) int {
	as, bs := versionSegs(a), versionSegs(b)
	for i := 0; i < len(as) || i < len(bs); i++ {
		var av, bv versionSeg
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		if c := av.cmp(bv); c != 0 {
			return c
		}
	}
	return 0
}

type versionSeg struct {
	n   int
	s   string
	num bool
}

func (a versionSeg) cmp(b versionSeg) int {
	if a.num && b.num {
		switch {
		case a.n < b.n:
			return -1
		case a.n > b.n:
			return 1
		}
		return 0
	}
	// Mixed or string segments — fall back to string compare; numeric wins
	// over string ("1" < "1a" still works because Cut splits on dots, not letters).
	return strings.Compare(a.s, b.s)
}

func versionSegs(v string) []versionSeg {
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ".")
	out := make([]versionSeg, len(parts))
	for i, p := range parts {
		seg := versionSeg{s: p}
		// Parse leading digits.
		n := 0
		ok := false
		for _, r := range p {
			if r < '0' || r > '9' {
				break
			}
			n = n*10 + int(r-'0')
			ok = true
		}
		if ok && allDigits(p) {
			seg.n = n
			seg.num = true
		}
		out[i] = seg
	}
	return out
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func toSet(ss []string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return m
}

func summarize(modules []ModuleDrift) ReportSummary {
	s := ReportSummary{Total: len(modules)}
	for _, m := range modules {
		switch m.Status {
		case InSync:
			s.InSync++
		case Behind:
			s.Behind++
		case YankedUpstream:
			s.YankedUpstream++
		case LocalOnly:
			s.LocalOnly++
		case UpstreamError:
			s.UpstreamError++
		}
	}
	return s
}

func statusRank(s Status) int {
	// Higher rank → printed first.
	switch s {
	case YankedUpstream:
		return 4
	case Behind:
		return 3
	case LocalOnly:
		return 2
	case UpstreamError:
		return 1
	default:
		return 0
	}
}

