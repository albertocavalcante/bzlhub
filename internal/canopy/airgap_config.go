package canopy

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/albertocavalcante/canopy/internal/api"
)

// AirgapDownloaderConfig renders a Bazel downloader-config text file.
// One `rewrite` line per unique source host; tainted/unresolved URLs
// are surfaced as comments so the operator knows they need manual
// handling.
//
// The file is consumed by Bazel via:
//   - Bazel ≥ 9.0.0:  --downloader_config=<file>
//   - Bazel ≤ 8.x:    --experimental_downloader_config=<file>
//
// (The "experimental_" prefix was dropped in the Bazel 9.0.0
// pre-release line; the old flag remains accepted as an alias.) The
// file format itself is identical across versions.
//
// Operators on Bazel ≥ 8.4 may prefer --module_mirrors for the
// registry-derived slice of the surface (modules pulled via a Bazel
// registry); 8.5 added per-registry scoping via
// --module_mirrors=<registry>=<mirror1>,<mirror2>,... The two flags
// are complementary: downloader config covers every URL (repo rules
// + extensions + registry); --module_mirrors only covers registry
// sources. Canopy's downloader-config output already covers the
// registry slice, so --module_mirrors is optional, not a replacement —
// for the .bazelrc-shaped sibling artifact, see AirgapModuleMirrors.
//
// Reference: https://bazel.build/external/extension#downloader_config
func (s *Service) AirgapDownloaderConfig(ctx context.Context, name, version string, opts api.DownloaderConfigOptions) (*api.DownloaderConfig, error) {
	mirror, err := validateBazelrcURL("mirror", opts.MirrorBase)
	if err != nil {
		return nil, err
	}
	if mirror == "" {
		mirror = "http://mirror.internal/"
	}
	if !strings.HasSuffix(mirror, "/") {
		mirror += "/"
	}

	var refs []api.ExternalRef
	if opts.Recursive {
		closure, err := s.AirgapSurface(ctx, name, version)
		if err != nil {
			return nil, fmt.Errorf("airgap surface: %w", err)
		}
		refs = closure.Refs
	} else {
		ext, err := s.ExternalSurface(ctx, name, version)
		if err != nil {
			return nil, fmt.Errorf("external surface: %w", err)
		}
		refs = ext.Refs
	}

	type hostBucket struct {
		host    string
		urls    int
		tainted int
	}
	byHost := map[string]*hostBucket{}
	var taintedRefs []api.ExternalRef

	for _, r := range refs {
		if r.Tainted || r.URL == "<unresolved>" {
			taintedRefs = append(taintedRefs, r)
			continue
		}
		h := r.Host
		if h == "" {
			// URLs we couldn't classify by host don't get rewrite
			// rules — fall through to the tainted comment block.
			taintedRefs = append(taintedRefs, r)
			continue
		}
		b := byHost[h]
		if b == nil {
			b = &hostBucket{host: h}
			byHost[h] = b
		}
		b.urls++
	}

	hosts := make([]*hostBucket, 0, len(byHost))
	for _, b := range byHost {
		hosts = append(hosts, b)
	}
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].host < hosts[j].host })

	var out strings.Builder
	scope := "per-module"
	if opts.Recursive {
		scope = "closure-wide"
	}
	fmt.Fprintf(&out, "# canopy airgap downloader config\n")
	fmt.Fprintf(&out, "# Module:      %s@%s (%s)\n", name, version, scope)
	fmt.Fprintf(&out, "# Mirror base: %s\n", mirror)
	fmt.Fprintf(&out, "# Use via:    Bazel >= 9.0.0  --downloader_config=<this file>\n")
	fmt.Fprintf(&out, "#             Bazel <= 8.x   --experimental_downloader_config=<this file>\n")
	fmt.Fprintf(&out, "# Note:       Bazel >= 8.4 also accepts --module_mirrors for the\n")
	fmt.Fprintf(&out, "#             registry-derived slice only; this file is the\n")
	fmt.Fprintf(&out, "#             superset and covers repo rules + extensions too.\n")
	fmt.Fprintf(&out, "# Hosts:       %d\n", len(hosts))
	totalURLsForHeader := 0
	for _, b := range byHost {
		totalURLsForHeader += b.urls
	}
	fmt.Fprintf(&out, "# URLs:        %d (skipped %d tainted/unresolved)\n", totalURLsForHeader, len(taintedRefs))
	fmt.Fprintf(&out, "# Reference:   https://bazel.build/external/extension#downloader_config\n")
	fmt.Fprintf(&out, "\n")

	totalURLs := 0
	for _, b := range hosts {
		fmt.Fprintf(&out, "# host=%s  urls=%d\n", b.host, b.urls)
		fmt.Fprintf(&out, "rewrite %s/(.*) %s%s/$1\n",
			regexp.QuoteMeta("https://"+b.host),
			mirror, b.host)
		fmt.Fprintf(&out, "rewrite %s/(.*) %s%s/$1\n",
			regexp.QuoteMeta("http://"+b.host),
			mirror, b.host)
		fmt.Fprintf(&out, "\n")
		totalURLs += b.urls
	}

	// Allow the mirror host so canopy-rewritten requests aren't
	// recursively re-rewritten. The hostname is extracted from the
	// mirror base.
	if mirrorHost := extractMirrorHost(mirror); mirrorHost != "" {
		fmt.Fprintf(&out, "# Allow the mirror host so rewritten requests resolve cleanly.\n")
		fmt.Fprintf(&out, "allow %s\n", mirrorHost)
		fmt.Fprintf(&out, "\n")
	}

	if len(taintedRefs) > 0 {
		fmt.Fprintf(&out, "# === MANUAL ATTENTION REQUIRED ===\n")
		fmt.Fprintf(&out, "# %d URLs couldn't be auto-rewritten (tainted or unresolved).\n", len(taintedRefs))
		fmt.Fprintf(&out, "# These come from impls where the URL depended on opaque state\n")
		fmt.Fprintf(&out, "# (ctx.execute output, conditional branches over external loads, etc.).\n")
		fmt.Fprintf(&out, "# Verify each by running the rule and copy a rewrite line manually.\n")
		for _, r := range taintedRefs {
			fmt.Fprintf(&out, "#   - %s  (rule=%s, platform=%s, file=%s)\n",
				r.URL, r.RuleName, r.Platform, r.File)
		}
	}

	return &api.DownloaderConfig{
		Module:     name,
		Version:    version,
		MirrorBase: mirror,
		Recursive:  opts.Recursive,
		Text:       out.String(),
		HostCount:  len(hosts),
		URLCount:   totalURLs,
	}, nil
}

func extractMirrorHost(mirrorBase string) string {
	// Use net/url so userinfo, ports, IPv6 brackets etc. are handled
	// correctly. Fall back to a naive scheme-strip for inputs net/url
	// can't make sense of (e.g. bare "mirror.example.com/").
	if u, err := url.Parse(mirrorBase); err == nil && u.Host != "" {
		return u.Host
	}
	s := strings.TrimPrefix(mirrorBase, "https://")
	s = strings.TrimPrefix(s, "http://")
	if i := strings.IndexByte(s, '/'); i >= 0 {
		return s[:i]
	}
	return s
}
