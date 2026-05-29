// Package bcrprobe answers "is this (module, version) coordinate
// available on the configured upstream registry?" without committing
// to a full ingest.
//
// It powers GET /api/bcr-probe, which the UI's friendly-404 page calls
// before showing the user the Ingest button — so the user sees a
// clear, accurate answer ("BCR has this; click Ingest" vs "BCR doesn't
// have this; here's what it does have") instead of clicking Ingest
// and waiting for the closure walker to report 404.
//
// Two HTTP hops at most:
//   - GET .../modules/<m>/<v>/source.json — establishes the precise
//     coordinate exists
//   - GET .../modules/<m>/metadata.json — only fetched when the
//     version probe 404s, so we can tell the user which versions DO
//     exist for that module name
//
// Deliberately scoped narrow: name-similarity ("did you mean
// aspect_rules_js?") is NOT done here — that requires either a cached
// BCR module index or a search API call, both of which are larger
// scope. The UI can layer a local-index suggestion on top using
// existing /api/modules data.
package bcrprobe

import (
	"context"
	"errors"

	"github.com/albertocavalcante/canopy/internal/fetch"
)

// Result is the wire shape returned by GET /api/bcr-probe. JSON tags
// snake_case to match the rest of canopy's API surface.
type Result struct {
	Module             string   `json:"module"`
	Version            string   `json:"version"`
	VersionExists      bool     `json:"version_exists"`
	ModuleExists       bool     `json:"module_exists"`
	VersionsAvailable  []string `json:"versions_available,omitempty"`
	LatestVersion      string   `json:"latest_version,omitempty"`
	// RegistryURL echoes the upstream we probed so the UI can render
	// a "checked against <url>" caption without guessing.
	RegistryURL string `json:"registry_url"`
}

// Prober is the dependency surface — anything that can fetch JSON
// from a BCR-shape registry. *fetch.Client is the production
// implementation; tests substitute a fake.
type Prober interface {
	GetSourceJSON(ctx context.Context, registryURL, module, version string) (*fetch.SourceJSON, error)
	GetMetadata(ctx context.Context, registryURL, module string) (*fetch.MetadataJSON, error)
}

// Probe answers whether (module, version) and (module) exist on the
// registry. Network errors other than 404 are returned as err — those
// are operational failures (DNS, TLS, 5xx) the UI should surface as
// "registry temporarily unreachable," not as a probe answer.
func Probe(ctx context.Context, p Prober, registryURL, module, version string) (Result, error) {
	res := Result{Module: module, Version: version, RegistryURL: registryURL}

	// Source-of-truth probe: does this exact coordinate exist?
	if _, err := p.GetSourceJSON(ctx, registryURL, module, version); err != nil {
		if !errors.Is(err, fetch.ErrNotFound) {
			return res, err
		}
		// Version not there — fall through to metadata probe to see
		// whether the module name itself is valid.
	} else {
		// Source.json fetched → the version exists. We can short-
		// circuit (no need for the metadata fetch) and still report
		// module_exists=true because the version's presence implies
		// the module's existence.
		res.VersionExists = true
		res.ModuleExists = true
		return res, nil
	}

	// Metadata probe: do any versions of this module exist?
	meta, err := p.GetMetadata(ctx, registryURL, module)
	if err != nil {
		if !errors.Is(err, fetch.ErrNotFound) {
			return res, err
		}
		// Both probes 404'd: neither the version nor the module exist.
		return res, nil
	}
	res.ModuleExists = true
	res.VersionsAvailable = meta.Versions
	if len(meta.Versions) > 0 {
		// metadata.json's versions slice is BCR-canonical order
		// (oldest first); the last entry is the latest. We don't
		// re-sort because some modules use non-semver schemes
		// (calendar dates, dotted git hashes) where BCR's natural
		// ordering is the authority.
		res.LatestVersion = meta.Versions[len(meta.Versions)-1]
	}
	return res, nil
}
