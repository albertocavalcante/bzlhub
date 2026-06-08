package verify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/albertocavalcante/assay"
	"github.com/albertocavalcante/assay/report"

	"github.com/albertocavalcante/bzlhub/internal/archive"
	"github.com/albertocavalcante/bzlhub/internal/modulediff"
)

// checkDeep re-runs assay on each module's archive and compares the
// freshly-computed ModuleReport against the one stored in the SQLite
// index. Any non-trivial diff surfaces as KindDeepReportMismatch with
// rule/provider/macro/etc. counts in Details so the operator can
// decide whether the stored row is stale (re-ingest needed) or the
// blob has been tampered with (mirror restore needed).
//
// This is the slow opt-in path. We extract one blob at a time and
// delete the staging directory immediately after analysis to keep disk
// usage flat across a large mirror. Modules without a DB index row are
// skipped — there's nothing to compare against.
func checkDeep(ctx context.Context, s *state) []Finding {
	if s.store == nil {
		// Without an index we have no stored report to diff against;
		// surfacing one finding per module-version would just be noise.
		return nil
	}
	var out []Finding
	for _, k := range sortedModuleKeys(s.modules) {
		m := s.modules[k]
		if !s.indexed[k] {
			continue
		}
		if m.expectedBlobHex == "" {
			continue
		}
		blob, ok := s.blobs[m.expectedBlobHex]
		if !ok {
			continue
		}
		stored, err := s.store.GetReport(ctx, k.name, k.version)
		if err != nil {
			continue
		}

		tmp, err := os.MkdirTemp("", "canopy-verify-deep-*")
		if err != nil {
			out = append(out, Finding{
				Kind:     KindDeepReportMismatch,
				Severity: SevError,
				Module:   k.name,
				Version:  k.version,
				Message:  "could not create extraction tempdir: " + err.Error(),
			})
			continue
		}
		analyzed, aerr := analyzeBlob(ctx, blob.path, m.source.StripPrefix, tmp)
		_ = os.RemoveAll(tmp)
		if aerr != nil {
			out = append(out, Finding{
				Kind:     KindDeepReportMismatch,
				Severity: SevError,
				Module:   k.name,
				Version:  k.version,
				Path:     "blobs/" + m.expectedBlobHex,
				Message:  "could not re-analyze blob: " + aerr.Error(),
				Fix:      "verify the blob is a valid archive; re-ingest if not",
			})
			continue
		}
		// Stored reports often differ on metadata-only fields. Normalize
		// before diffing so we don't trip on naming-only diffs.
		analyzed.Name = stored.Name
		if analyzed.Version == "" {
			analyzed.Version = stored.Version
		}

		d := modulediff.Compute(stored, analyzed)
		if isEmptyDiff(d) {
			continue
		}
		out = append(out, Finding{
			Kind:     KindDeepReportMismatch,
			Severity: SevError,
			Module:   k.name,
			Version:  k.version,
			Path:     filepath.Join("modules", k.name, k.version),
			Message:  "stored ModuleReport does not match freshly-assayed report",
			Fix:      "re-ingest with `bzlhub ingest --from <upstream> <m>@<v>` to refresh the stored report",
			Details: map[string]any{
				"rules_added":       len(d.Rules.Added),
				"rules_removed":     len(d.Rules.Removed),
				"rules_changed":     len(d.Rules.Changed),
				"providers_added":   len(d.Providers.Added),
				"providers_removed": len(d.Providers.Removed),
				"providers_changed": len(d.Providers.Changed),
				"macros_added":      len(d.Macros.Added),
				"macros_removed":    len(d.Macros.Removed),
				"breaking":          len(d.Breaking),
			},
		})
	}
	return out
}

// analyzeBlob extracts the archive at blobPath into stagingDir
// (respecting strip_prefix, matching ingest's behavior) and runs
// assay.Analyze on the result. tar.gz is the only supported archive
// today — that's what BCR + canopy's mirror writer produce.
func analyzeBlob(ctx context.Context, blobPath, stripPrefix, stagingDir string) (*report.ModuleReport, error) {
	f, err := os.Open(blobPath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", blobPath, err)
	}
	defer f.Close()
	// Format sniffing: extension would normally hint, but content-
	// addressed blobs have no extension. tar.gz starts with the gzip
	// magic 0x1f8b; zip starts with 0x504b. Sniff a few bytes.
	hdr := make([]byte, 4)
	n, rerr := f.Read(hdr)
	// EOF on a sub-4-byte file is fine — the switch below falls through
	// to "unknown format" if magic bytes don't match. Any other read
	// failure (I/O error mid-stream) must surface.
	if rerr != nil && !errors.Is(rerr, io.EOF) {
		return nil, fmt.Errorf("read blob header: %w", rerr)
	}
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}
	switch {
	case n >= 2 && hdr[0] == 0x1f && hdr[1] == 0x8b:
		if _, err := archive.ExtractTarGz(f, stagingDir, stripPrefix, archive.MaxExtractBytes); err != nil {
			return nil, fmt.Errorf("extract tar.gz: %w", err)
		}
	case n >= 2 && hdr[0] == 0x50 && hdr[1] == 0x4b:
		if _, err := archive.ExtractZip(f, stagingDir, stripPrefix, archive.MaxExtractBytes); err != nil {
			return nil, fmt.Errorf("extract zip: %w", err)
		}
	default:
		return nil, fmt.Errorf("unknown archive format (magic %x)", hdr[:n])
	}
	r, err := assay.Analyze(ctx, stagingDir)
	if err != nil {
		// Some archives nest one level deeper than strip_prefix
		// suggests; surface a clearer hint in that case.
		if strings.Contains(err.Error(), "MODULE.bazel") {
			return nil, fmt.Errorf("assay analyze (no MODULE.bazel found — strip_prefix mismatch?): %w", err)
		}
		return nil, fmt.Errorf("assay analyze: %w", err)
	}
	return r, nil
}

// isEmptyDiff returns true when a modulediff.Report shows no
// meaningful change — used to suppress "no problem" deep-check
// findings.
func isEmptyDiff(d *modulediff.Report) bool {
	if d == nil {
		return true
	}
	n := 0
	n += len(d.Rules.Added) + len(d.Rules.Removed) + len(d.Rules.Changed)
	n += len(d.Providers.Added) + len(d.Providers.Removed) + len(d.Providers.Changed)
	n += len(d.Macros.Added) + len(d.Macros.Removed)
	n += len(d.Aspects.Added) + len(d.Aspects.Removed)
	n += len(d.Toolchains.Added) + len(d.Toolchains.Removed)
	n += len(d.RepositoryRules.Added) + len(d.RepositoryRules.Removed) + len(d.RepositoryRules.Changed)
	n += len(d.ModuleExtensions.Added) + len(d.ModuleExtensions.Removed) + len(d.ModuleExtensions.Changed)
	n += len(d.BazelDeps.Added) + len(d.BazelDeps.Removed) + len(d.BazelDeps.Changed)
	if d.CompatibilityLevel != nil || d.Hermeticity != nil {
		n++
	}
	return n == 0
}
