package admit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/albertocavalcante/bzlhub/internal/archive"
	"github.com/albertocavalcante/bzlhub/internal/preflight"
	"github.com/albertocavalcante/bzlhub/internal/publish"
	"github.com/albertocavalcante/bzlhub/internal/store"
)

// pipelineDeps bundles the shared collaborators admitOne needs. The
// Runner builds one of these at construction and reuses it across
// every request.
type pipelineDeps struct {
	fetcher         Fetcher
	publisher       publish.Publisher
	bot             publish.Identity
	extractMaxBytes int64
}

// admitResult is the value admitOne returns on success. The runner
// persists CommitSHA + Integrity on the request row and writes them
// into the audit payload.
type admitResult struct {
	CommitSHA string
	Integrity string
	Bytes     int64
}

// admitOne runs the full fetch → extract → publish pipeline for one
// approved request. Stateless apart from the publisher's worktree
// lock; safe to call concurrently from multiple goroutines IF the
// publisher serializes its own writes (publish.GitDirectPublisher
// does).
func admitOne(ctx context.Context, deps pipelineDeps, req store.Request) (admitResult, error) {
	// Source URL precedence:
	//   1. Explicit req.SourceURL — user-provided at submit time
	//   2. preflight_json.CascadeSource.URL — populated when the
	//      preflight cascade probe found this (m, v) in the
	//      upstream BCR. Lets the user submit "rules_python 1.5.0"
	//      with no URL and have admit fetch from BCR.
	//   3. Nothing → fail.
	url := req.SourceURL
	var cascadeStrip string
	if url == "" {
		if hit := cascadeFromPreflight(req); hit != nil {
			url = hit.URL
			cascadeStrip = hit.StripPrefix
		}
	}
	if url == "" {
		return admitResult{}, errors.New("no source_url on request and preflight found no cascade source")
	}

	sink, err := deps.publisher.BeginBlob(ctx, url)
	if err != nil {
		return admitResult{}, fmt.Errorf("begin blob: %w", err)
	}
	bytesFetched, fetchErr := deps.fetcher.Fetch(ctx, url, sink)
	if fetchErr != nil {
		sink.Abort()
		return admitResult{}, fmt.Errorf("fetch %s: %w", url, fetchErr)
	}
	blobRef, err := sink.Close()
	if err != nil {
		return admitResult{}, fmt.Errorf("close blob: %w", err)
	}

	moduleBazel, stripPrefix, err := extractFromBlob(blobRef.Key, deps.extractMaxBytes)
	if err != nil {
		return admitResult{}, fmt.Errorf("extract %s@%s: %w", req.Module, req.Version, err)
	}
	// Prefer the cascade-supplied strip_prefix when present — the
	// upstream BCR knows the canonical value, even when our local
	// extraction heuristic might be wrong.
	if cascadeStrip != "" {
		stripPrefix = cascadeStrip
	}

	sourceJSON, err := BuildSourceJSON(url, blobRef.Integrity, stripPrefix)
	if err != nil {
		return admitResult{}, fmt.Errorf("build source.json: %w", err)
	}

	requester := publish.Identity{Name: req.SubmitterSub, Email: req.SubmitterEmail}
	if requester.IsZero() {
		// Fall back to the bot when the request didn't capture an
		// identity (anonymous submit under an `open` profile).
		requester = deps.bot
	}

	receipt, err := deps.publisher.Publish(ctx, publish.PublishRequest{
		Module:      req.Module,
		Version:     req.Version,
		SourceJSON:  sourceJSON,
		ModuleBazel: moduleBazel,
		Blob:        blobRef,
		Requester:   requester,
		SourceURL:   url,
	})
	if err != nil {
		return admitResult{}, fmt.Errorf("publish: %w", err)
	}

	return admitResult{
		CommitSHA: receipt.Commit,
		Integrity: blobRef.Integrity,
		Bytes:     bytesFetched,
	}, nil
}

// extractFromBlob reads the gzipped tarball at blobPath into a
// scratch directory, locates the module's MODULE.bazel, and detects
// the common strip_prefix. Cleans up the scratch dir before
// returning (caller doesn't need the on-disk tree past this point).
//
// maxBytes ≤ 0 falls back to a generous 1 GiB cap — sources past
// that aren't realistic Bazel modules.
func extractFromBlob(blobPath string, maxBytes int64) (moduleBazel []byte, stripPrefix string, err error) {
	const defaultExtractCap = 1 << 30
	if maxBytes <= 0 {
		maxBytes = defaultExtractCap
	}

	scratch, err := os.MkdirTemp("", "canopy-admit-extract-")
	if err != nil {
		return nil, "", fmt.Errorf("mkdir scratch: %w", err)
	}
	defer os.RemoveAll(scratch)

	f, err := os.Open(blobPath)
	if err != nil {
		return nil, "", fmt.Errorf("open blob: %w", err)
	}
	defer f.Close()
	if _, err := archive.ExtractTarGz(f, scratch, "", maxBytes); err != nil {
		return nil, "", fmt.Errorf("extract tar.gz: %w", err)
	}

	entries, err := topLevelEntries(scratch)
	if err != nil {
		return nil, "", err
	}
	stripPrefix = DetectStripPrefix(entries)

	// MODULE.bazel lives at <stripPrefix>/MODULE.bazel when the
	// archive has the common GitHub shape, else at the root.
	modulePath := filepath.Join(scratch, "MODULE.bazel")
	if stripPrefix != "" {
		modulePath = filepath.Join(scratch, stripPrefix, "MODULE.bazel")
	}
	moduleBazel, err = os.ReadFile(modulePath)
	if err != nil {
		// Module without a MODULE.bazel is rare but legal — return
		// nil + no error so the publish call proceeds without it.
		if errors.Is(err, os.ErrNotExist) {
			return nil, stripPrefix, nil
		}
		return nil, "", fmt.Errorf("read MODULE.bazel: %w", err)
	}
	return moduleBazel, stripPrefix, nil
}

// topLevelEntries returns the relative paths of every file under
// root, formatted as "first/second/...".  Used by DetectStripPrefix
// to find the common leading directory.
func topLevelEntries(root string) ([]string, error) {
	var out []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		rel := strings.TrimPrefix(path, root+string(filepath.Separator))
		// Walk emits directories too; we only want file paths to keep
		// DetectStripPrefix's "no top-level file" check correct.
		if info.IsDir() {
			return nil
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// cascadeFromPreflight extracts the cascade hit (URL + integrity +
// strip_prefix) the preflight checker stored in preflight_json
// when the request auto-passed via the upstream-BCR short-circuit.
// Returns nil for requests that took a different verdict path —
// admit must require an explicit source_url in that case.
//
// Tolerates malformed preflight_json by returning nil; the caller's
// subsequent "no url" error surfaces the real problem.
func cascadeFromPreflight(req store.Request) *preflight.CascadeHit {
	if len(req.PreflightJSON) == 0 {
		return nil
	}
	var v preflight.Verdict
	if err := json.Unmarshal(req.PreflightJSON, &v); err != nil {
		return nil
	}
	return v.CascadeSource
}

