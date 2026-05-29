// Package summary produces a single "what is this module?" view of a
// Bazel module by composing three orthogonal data sources:
//
//  1. MODULE.bazel — name, version, compat level, bazel_deps
//     (parsed via assay/modulefile, which wraps go-bzlmod)
//  2. Module-root assets — README, LICENSE, example directories
//     (extracted via assay/assets)
//  3. BCR registry metadata.json — homepage, maintainers, yanked
//     versions (parsed locally; metadata.json doesn't live in the
//     module source tarball, it lives in the registry tree)
//
// Why a separate library: callers like canopy's UI, an MCP tool, a
// CLI summarizer, or an external registry-mirroring script all need
// EXACTLY this composed view. Keeping it in a shared library means
// the Summary shape (and the merge logic) has one canonical
// definition; consumers stop reinventing partial versions.
//
// Why not stuff this into assay: assay's job is heavy static
// analysis (.bzl walking, hermeticity classification, interpreter
// hydration). The Summary is a strict subset that doesn't require
// any of that machinery. Splitting it lets non-canopy callers depend
// on this lib without pulling assay's interpreter dep chain.
//
// Design choices:
//
//   - FromDir is the standard entry point and takes a source-tree
//     path. Optional functional Options let callers also feed a
//     metadata.json (path or bytes) to enrich the result with
//     registry-level fields the source tree doesn't have.
//   - All three data sources degrade gracefully: missing MODULE.bazel
//     is a hard error (no module without that), but missing assets or
//     metadata.json just leave their corresponding fields empty.
//   - The Summary type is meant for both JSON serialization (MCP /
//     HTTP responses) and Go consumption; field tags use snake_case
//     to match canopy's existing API conventions.
package summary

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/albertocavalcante/assay/assets"
	"github.com/albertocavalcante/assay/modulefile"
)

// Summary is the unified per-module description meant for "first
// impression" surfaces — a registry page hero, an MCP tool result,
// a CLI dump. Holds only the fields a reader would want to see; for
// the deep .bzl analysis (rules, providers, hermeticity, etc.) use
// assay.Analyze directly.
type Summary struct {
	Name               string   `json:"name"`
	Version            string   `json:"version,omitempty"`
	CompatibilityLevel int      `json:"compatibility_level,omitempty"`
	BazelCompatibility []string `json:"bazel_compatibility,omitempty"`

	// Deps are the bazel_dep() declarations from MODULE.bazel.
	// Note: this is the DECLARED dep set, not the transitive
	// closure. Callers that want the full closure should layer their
	// own resolver (go-bzlmod) on top.
	Deps []Dep `json:"deps,omitempty"`

	// Asset fields — populated from the module source tree.
	// Mirror the shape used by canopy's UI verbatim so a UI client
	// can drop a Summary in without translation.
	Readme      string   `json:"readme,omitempty"`
	ReadmePath  string   `json:"readme_path,omitempty"`
	License     string   `json:"license,omitempty"`
	LicensePath string   `json:"license_path,omitempty"`
	LicenseName string   `json:"license_name,omitempty"`
	ExampleDirs []string `json:"example_dirs,omitempty"`

	// Registry-metadata fields — populated only when the caller
	// supplied a metadata.json via WithMetadataJSON*. Empty when not
	// supplied; never inferred from the source tree (the canonical
	// source for these is BCR's metadata.json, not the module).
	Homepage       string            `json:"homepage,omitempty"`
	Repository     []string          `json:"repository,omitempty"`
	Maintainers    []Maintainer      `json:"maintainers,omitempty"`
	YankedVersions map[string]string `json:"yanked_versions,omitempty"`
}

// Dep is one bazel_dep entry.
type Dep struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Maintainer mirrors BCR's metadata.json maintainer shape: name +
// optional contact channels. Both email and github are surfaced
// because real maintainer entries use one or the other (or both).
type Maintainer struct {
	Name   string `json:"name"`
	Email  string `json:"email,omitempty"`
	Github string `json:"github,omitempty"`
}

// metadataDoc is the wire shape of BCR's modules/<m>/metadata.json.
// Defined locally rather than reusing some upstream type because
// upstream BCR doesn't ship a Go SDK and the shape is tiny.
type metadataDoc struct {
	Homepage       string            `json:"homepage"`
	Maintainers    []Maintainer      `json:"maintainers"`
	Repository     []string          `json:"repository"`
	YankedVersions map[string]string `json:"yanked_versions"`
	// Versions / compatibility intentionally NOT lifted into Summary
	// — Summary describes ONE module-version, the metadata's
	// versions list is a per-module concern that belongs elsewhere.
}

// RegistryMetadata is the subset of BCR metadata.json fields that
// describe the module-as-a-whole (not a specific version). Exposed
// so callers like canopy's UI handler can lift these fields into
// their own response shape without invoking the full FromDir path
// (which reads MODULE.bazel + assets too).
type RegistryMetadata struct {
	Homepage       string            `json:"homepage,omitempty"`
	Maintainers    []Maintainer      `json:"maintainers,omitempty"`
	Repository     []string          `json:"repository,omitempty"`
	YankedVersions map[string]string `json:"yanked_versions,omitempty"`
}

// ReadMetadataJSON parses the BCR-shape modules/<m>/metadata.json at
// the given path. Returns a zero-value RegistryMetadata (NOT an
// error) when the file is absent — callers can wire this against
// arbitrary mirror trees without first checking existence. Genuine
// parse / read failures surface as errors.
func ReadMetadataJSON(path string) (*RegistryMetadata, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &RegistryMetadata{}, nil
		}
		return nil, err
	}
	return ParseMetadataJSON(b)
}

// ParseMetadataJSON is ReadMetadataJSON for callers who already have
// the bytes in hand (e.g. just fetched them over HTTP).
func ParseMetadataJSON(b []byte) (*RegistryMetadata, error) {
	if len(b) == 0 {
		return &RegistryMetadata{}, nil
	}
	var m metadataDoc
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &RegistryMetadata{
		Homepage:       m.Homepage,
		Maintainers:    append([]Maintainer(nil), m.Maintainers...),
		Repository:     append([]string(nil), m.Repository...),
		YankedVersions: cloneMap(m.YankedVersions),
	}, nil
}

// Option configures FromDir.
type Option func(*options)

type options struct {
	metadataPath  string
	metadataBytes []byte
	canonicalName    string
	canonicalVersion string
}

// WithCanonicalCoordinate lets callers override the (Name, Version)
// fields with the registry-canonical coordinate they ASKED FOR.
//
// Real-world MODULE.bazel files frequently carry a stub `version =
// "0.0.0"` because release tooling injects the actual version
// post-publish. Summary built from such a tree would report the
// stub, surprising agents/UIs that requested a specific version.
//
// Pass the requested coordinate via this option and Summary takes
// it as the source of truth, ignoring the stub in MODULE.bazel.
// Empty values fall through to the MODULE.bazel-declared field.
func WithCanonicalCoordinate(name, version string) Option {
	return func(o *options) {
		o.canonicalName = name
		o.canonicalVersion = version
	}
}

// WithMetadataJSON points FromDir at a metadata.json on disk
// (typically modules/<m>/metadata.json under a BCR registry root).
// Read errors are propagated; missing file is silently ignored so
// callers can wire this unconditionally without pre-checking
// existence.
func WithMetadataJSON(path string) Option {
	return func(o *options) { o.metadataPath = path }
}

// WithMetadataJSONBytes is the in-memory variant of WithMetadataJSON.
// Useful when the caller already has the bytes (e.g. fetched via
// HTTP and not yet persisted).
func WithMetadataJSONBytes(b []byte) Option {
	return func(o *options) { o.metadataBytes = b }
}

// FromDir builds a Summary by reading the module source tree at
// moduleDir (which must contain a MODULE.bazel) and applying any
// supplied options. Errors only on hard failures (no MODULE.bazel,
// malformed JSON when bytes were provided); soft conditions like
// missing README / missing metadata.json fall through cleanly.
func FromDir(moduleDir string, opts ...Option) (*Summary, error) {
	o := options{}
	for _, opt := range opts {
		opt(&o)
	}

	// Phase 1: MODULE.bazel is the load-bearing source. No file
	// here means there's no module to summarize — surface a clear
	// error so callers can distinguish from "found but empty."
	rep, err := modulefile.ParseFile(filepath.Join(moduleDir, "MODULE.bazel"))
	if err != nil {
		return nil, fmt.Errorf("parse MODULE.bazel: %w", err)
	}

	s := &Summary{
		Name:               rep.Name,
		Version:            rep.Version,
		CompatibilityLevel: rep.CompatibilityLevel,
		BazelCompatibility: append([]string(nil), rep.BazelCompatibility...),
	}
	// Canonical coordinate override — see WithCanonicalCoordinate
	// for the why. Applied AFTER reading MODULE.bazel so a missing
	// override falls through cleanly to the declared values.
	if o.canonicalName != "" {
		s.Name = o.canonicalName
	}
	if o.canonicalVersion != "" {
		s.Version = o.canonicalVersion
	}
	for _, d := range rep.BazelDeps {
		s.Deps = append(s.Deps, Dep{Name: d.Name, Version: d.Version})
	}

	// Phase 2: assets — entirely independent of MODULE.bazel,
	// extracted from the same source root. assets.ModuleAssetsFor
	// already handles missing-file fallbacks and the 256KB cap.
	a := assets.ModuleAssetsFor(moduleDir)
	s.Readme = a.Readme
	s.ReadmePath = a.ReadmePath
	s.License = a.License
	s.LicensePath = a.LicensePath
	s.LicenseName = a.LicenseName
	s.ExampleDirs = append([]string(nil), a.ExampleDirs...)

	// Phase 3: optional metadata.json enrichment.
	if o.metadataBytes != nil {
		if err := applyMetadata(s, o.metadataBytes); err != nil {
			return nil, fmt.Errorf("parse metadata.json bytes: %w", err)
		}
	} else if o.metadataPath != "" {
		b, err := os.ReadFile(o.metadataPath)
		if err != nil {
			// Missing metadata.json is OK — registry-level fields
			// just stay empty. Other read errors surface (likely
			// permission issues the caller wants to know about).
			if !errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("read metadata.json: %w", err)
			}
		} else if err := applyMetadata(s, b); err != nil {
			return nil, fmt.Errorf("parse metadata.json: %w", err)
		}
	}

	return s, nil
}

// applyMetadata merges parsed metadata.json fields into s. Pulled
// out so FromDir's caller paths (bytes vs file) share one merge.
func applyMetadata(s *Summary, b []byte) error {
	var m metadataDoc
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	s.Homepage = m.Homepage
	s.Repository = append([]string(nil), m.Repository...)
	s.Maintainers = append([]Maintainer(nil), m.Maintainers...)
	s.YankedVersions = cloneMap(m.YankedVersions)
	return nil
}

func cloneMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
