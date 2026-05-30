// Package assets extracts the "registry-page-grade" supporting material that
// sits next to a Bazel module's .bzl sources: README, LICENSE, and example
// directories.
//
// # Why it exists
//
// Static analysis on the source tree (assay/bzlwalk + assay/hermetic)
// answers "what rules / providers / hermeticity does this module
// declare?" That suits a build engineer. A registry page needs more —
// a reader landing on /modules/<m>/<v> wants the same things they'd
// expect on pkg.go.dev or docs.rs: what is this module, who owns it,
// what's the license, where do I see examples.
//
// This package fills that gap. It's intentionally small and
// dependency-free (only os, path/filepath, strings) — picking files
// off a disk tree the caller already pointed at, nothing more.
package assets

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/albertocavalcante/assay/report"
)

// maxReadmeBytes caps inlined README contents. READMEs >256KB are
// vanishingly rare and storing them inline in the JSON-serialized
// report would bloat /api/modules/{m}/{v} payloads. When (if) we hit
// the cap, the UI still gets the prefix and the path; users follow
// the path through code-nav for the full text.
const maxReadmeBytes = 256 * 1024

// maxLicenseBytes caps inlined LICENSE contents. Same reasoning as
// READMEs — typical LICENSE files are <20KB; we cap higher than that
// so multi-license assemblies still come through whole, but with a
// hard upper bound so a malicious or accidentally enormous file can't
// blow up the report.
const maxLicenseBytes = 256 * 1024

// readmeFilenames are the names we look for at the module root, in
// preference order. README.md wins because Markdown is what canopy
// renders; other forms fall through.
var readmeFilenames = []string{
	"README.md",
	"README.markdown",
	"README.rst",
	"README.txt",
	"README",
}

// licenseFilenames are checked in preference order. LICENSE wins over
// COPYING because Bazel rulesets overwhelmingly use the former.
var licenseFilenames = []string{
	"LICENSE",
	"LICENSE.md",
	"LICENSE.txt",
	"LICENCE",
	"COPYING",
	"COPYING.txt",
}

// exampleDirNames are directory basenames recognized as holding
// usage examples. Match is case-insensitive but only at the root —
// arbitrary "example" dirs deep in vendored trees aren't surfaced.
var exampleDirNames = []string{
	"example",
	"examples",
	"e2e",
}

// Extract scans moduleDir and populates r.Assets with whatever it
// finds. Best-effort: missing files, permission errors, and other I/O
// failures produce zero-value fields rather than aborting. The return
// type reflects that — callers don't need to special-case asset
// failures.
//
// moduleDir is expected to be the module's source root (the directory
// containing MODULE.bazel). Asset detection is intentionally rooted
// here; nested READMEs (e.g. a sub-package's README) are NOT picked
// up, since the registry page is about the module-as-a-whole.
func Extract(moduleDir string, r *report.ModuleReport) {
	if r == nil {
		return
	}
	r.Assets = ModuleAssetsFor(moduleDir)
}

// ModuleAssetsFor is the pure / call-site-friendly form: returns the
// computed assets struct without mutating a caller's report. Useful
// for tests + for callers that build their own report shape.
func ModuleAssetsFor(moduleDir string) report.ModuleAssets {
	var a report.ModuleAssets
	if moduleDir == "" {
		return a
	}

	// README — read the first matching name. We don't merge across
	// formats; the picker order encodes our preference (Markdown
	// first, since the UI's MarkdownDoc handles it natively).
	for _, name := range readmeFilenames {
		bytes, ok := readCapped(filepath.Join(moduleDir, name), maxReadmeBytes)
		if ok {
			a.Readme = string(bytes)
			a.ReadmePath = name
			break
		}
	}

	// LICENSE — same iteration pattern.
	for _, name := range licenseFilenames {
		bytes, ok := readCapped(filepath.Join(moduleDir, name), maxLicenseBytes)
		if ok {
			a.License = string(bytes)
			a.LicensePath = name
			a.LicenseName = detectLicense(string(bytes))
			break
		}
	}

	// Example directories at the root only. ReadDir is intentional
	// rather than WalkDir — depth-1 enumeration is much faster on
	// modules with large vendor trees, and "the examples folder" is
	// uniformly at the root in real BCR modules.
	a.ExampleDirs = findExampleDirs(moduleDir)

	return a
}

// readCapped reads at most max bytes from path. Returns ok=false on
// any error (missing file, permission denied, etc.) so callers can
// fall through to the next preference cleanly. On hit, returns the
// raw bytes up to max — caller decides whether to add an "(truncated)"
// marker.
func readCapped(path string, max int) ([]byte, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	buf := make([]byte, max+1) // +1 to detect overflow cheaply
	n, _ := f.Read(buf)
	if n > max {
		return buf[:max], true
	}
	return buf[:n], true
}

// findExampleDirs returns relative paths to convention-named
// example directories at moduleDir's root. Empty when none exist —
// nil-vs-empty doesn't matter because ModuleAssets.ExampleDirs has
// omitempty in its JSON tag.
func findExampleDirs(moduleDir string) []string {
	entries, err := os.ReadDir(moduleDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := strings.ToLower(e.Name())
		if slices.Contains(exampleDirNames, name) {
			out = append(out, e.Name())
		}
	}
	return out
}

// detectLicense returns an SPDX-shaped identifier when the LICENSE
// text matches one of the common, unambiguous headers we recognize.
// Empty string when no header matched — the UI still has License +
// LicensePath to render a generic "view license" link.
//
// EPISTEMIC STATUS — HEURISTIC. Narrow, fast substring check against
// the first 2KB of the file, NOT a full SPDX classifier. False
// positives would mislabel a real license; false negatives just mean
// "view license" instead of a clean badge. We prefer the latter, so
// when in doubt we return "" rather than guessing. Consumers should
// treat any non-empty LicenseName as "this header's keywords looked
// like X" rather than "this file is X-licensed."
func detectLicense(text string) string {
	// Cheap upfront slice: license headers are at the top.
	head := text
	if len(head) > 2048 {
		head = head[:2048]
	}
	head = strings.ToLower(head)

	switch {
	case strings.Contains(head, "apache license") && strings.Contains(head, "version 2.0"):
		return "Apache-2.0"
	case strings.Contains(head, "mit license"):
		return "MIT"
	case strings.Contains(head, "bsd 3-clause") || strings.Contains(head, `redistribution and use in source and binary forms`) && strings.Contains(head, "3."):
		return "BSD-3-Clause"
	case strings.Contains(head, "bsd 2-clause"):
		return "BSD-2-Clause"
	case strings.Contains(head, "mozilla public license") && strings.Contains(head, "version 2.0"):
		return "MPL-2.0"
	case strings.Contains(head, "gnu general public license") && strings.Contains(head, "version 3"):
		return "GPL-3.0"
	case strings.Contains(head, "gnu general public license") && strings.Contains(head, "version 2"):
		return "GPL-2.0"
	case strings.Contains(head, "gnu lesser general public license") && strings.Contains(head, "version 3"):
		return "LGPL-3.0"
	case strings.Contains(head, "gnu lesser general public license") && strings.Contains(head, "version 2.1"):
		return "LGPL-2.1"
	case strings.Contains(head, "the unlicense") || strings.Contains(head, "this is free and unencumbered software"):
		return "Unlicense"
	case strings.Contains(head, "isc license"):
		return "ISC"
	case strings.Contains(head, "creative commons") && strings.Contains(head, "cc0"):
		return "CC0-1.0"
	}
	return ""
}
