package publish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/albertocavalcante/canopy/internal/fetch"
	"github.com/albertocavalcante/canopy/internal/publish"
	"github.com/albertocavalcante/canopy/internal/resolve"
)

// resolveAndStage handles the three source paths. Returns a
// PublishRequest populated with everything except Requester.
func resolveAndStage(
	ctx context.Context, o *publishOutput, src publishSource,
	module, ver string, pub publish.Publisher,
) (publish.PublishRequest, error) {
	switch {
	case src.from != "":
		return resolveFromUpstream(ctx, o, src.from, module, ver, pub)
	case src.directURL != "":
		return resolveFromDirectURL(ctx, o, src.directURL, src.directIntegrity, module, ver, pub)
	case src.sourceJSONPath != "":
		return resolveFromSourceJSONFile(ctx, o, src.sourceJSONPath, module, ver, pub)
	}
	return publish.PublishRequest{}, errors.New("canopy publish: no source specified")
}

// resolveFromDirectURL walks Story B: user supplies the tarball URL +
// SRI directly. We synthesize a minimal source.json from those two
// fields and run the same stream-through-blob-sink + extract + verify
// pipeline used by Story A. No upstream metadata.json to lift since
// the source isn't a BCR-shape registry.
//
// Limitations vs --source-json:
//   - No strip_prefix support (use --source-json for tarballs that
//     unpack into a versioned directory)
//   - No archive_type override (auto-detected from URL extension)
//   - No patches / patch_strip
//
// Users needing those should use --source-json with a pre-made file.
func resolveFromDirectURL(
	ctx context.Context, o *publishOutput,
	url, integrity, module, ver string, pub publish.Publisher,
) (publish.PublishRequest, error) {
	if integrity == "" {
		// cobra's MarkFlagsRequiredTogether on (--source-url, --source-integrity)
		// catches this at parse time, so reaching here means programmer
		// error in the wiring. Defense in depth.
		return publish.PublishRequest{}, errors.New(
			"canopy publish: --source-url requires --source-integrity")
	}
	o.step("resolving direct URL: %s", url)

	// Minimal source.json — just URL + integrity. archive_type is
	// detected from URL extension by resolve.FromSource.
	srcShape := map[string]string{
		"url":       url,
		"integrity": integrity,
	}
	srcBytes, err := json.MarshalIndent(srcShape, "", "  ")
	if err != nil {
		return publish.PublishRequest{}, fmt.Errorf("synthesize source.json: %w", err)
	}
	srcBytes = append(srcBytes, '\n')

	sj := &fetch.SourceJSON{URL: url, Integrity: integrity}
	return resolveFromExplicitSource(ctx, o, sj, srcBytes, module, ver, url, pub)
}

// resolveFromSourceJSONFile walks Story C: user points at a pre-made
// source.json on disk. We read it verbatim (preserving any
// strip_prefix / archive_type / patches the user encoded) and run the
// same downstream pipeline.
func resolveFromSourceJSONFile(
	ctx context.Context, o *publishOutput,
	path, module, ver string, pub publish.Publisher,
) (publish.PublishRequest, error) {
	o.step("resolving source.json: %s", path)
	srcBytes, err := os.ReadFile(path)
	if err != nil {
		return publish.PublishRequest{}, fmt.Errorf("read %s: %w", path, err)
	}
	sj, err := fetch.ParseSourceJSON(srcBytes)
	if err != nil {
		return publish.PublishRequest{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if sj.URL == "" || sj.Integrity == "" {
		return publish.PublishRequest{}, fmt.Errorf(
			"%s missing required fields (url + integrity)", path)
	}
	return resolveFromExplicitSource(ctx, o, sj, srcBytes, module, ver, sj.URL, pub)
}

// resolveFromExplicitSource is the shared backend for the two non-
// registry source paths (Stories B and C). The caller has already
// loaded source.json bytes (synthesized or from disk) and parsed
// them into a *fetch.SourceJSON. This function:
//
//  1. Opens the publisher's blob sink for streaming.
//  2. Fetches the tarball via HTTP, integrity-verifies, extracts.
//     Bytes tee through the blob sink as they flow.
//  3. Reads MODULE.bazel from the extracted directory if present —
//     no upstream metadata to lift, so the returned request has
//     nil UpstreamMetadata.
//
// No upstream metadata.json lift because the source isn't a BCR-
// shape registry. The local registry's metadata.json (managed by
// publish.mirror.MergeMetadata) is the authority on this version's
// surrounding metadata.
func resolveFromExplicitSource(
	ctx context.Context, o *publishOutput,
	sj *fetch.SourceJSON, srcBytes []byte,
	module, ver, sourceURL string, pub publish.Publisher,
) (publish.PublishRequest, error) {
	fc := fetch.NewClient()

	sink, err := pub.BeginBlob(ctx, sourceURL)
	if err != nil {
		return publish.PublishRequest{}, fmt.Errorf("begin blob: %w", err)
	}
	abortOnFail := func() { sink.Abort() }

	m, err := resolve.FromSource(ctx, fc, sj, srcBytes, nil, nil, resolve.Options{
		Tee:          sink,
		CaptureBytes: true,
	})
	if err != nil {
		abortOnFail()
		return publish.PublishRequest{}, fmt.Errorf("resolve: %w", err)
	}
	defer m.Cleanup()

	ref, err := sink.Close()
	if err != nil {
		return publish.PublishRequest{}, fmt.Errorf("close blob: %w", err)
	}
	o.verboseLog("blob: %d bytes, integrity %s", ref.Bytes, ref.Integrity)

	// Pick up MODULE.bazel from the extracted dir; resolve.FromSource
	// won't have populated m.ModuleBytes here because we didn't pass a
	// modBytesFallback.
	moduleBytes, err := os.ReadFile(filepath.Join(m.Dir, "MODULE.bazel"))
	if err != nil {
		// Not fatal in the abstract — some modules might ship without
		// a top-level MODULE.bazel — but assay's downstream analysis
		// requires it, so emit a warning. The publish itself will
		// proceed; assay errors land downstream during ingest.
		o.verboseLog("no MODULE.bazel at module root (assay analysis may fail): %v", err)
		moduleBytes = nil
	}

	return publish.PublishRequest{
		Module:      module,
		Version:     ver,
		SourceJSON:  srcBytes,
		ModuleBazel: moduleBytes,
		// No UpstreamMetadata — explicit-source paths don't have an
		// upstream BCR registry to lift from.
		Blob:      ref,
		SourceURL: sourceURL,
	}, nil
}

// resolveFromUpstream walks the Story A path: fetch source.json +
// metadata.json from the upstream registry, stream the SRI-verified
// archive through the publisher's BlobSink.
func resolveFromUpstream(
	ctx context.Context, o *publishOutput,
	upstreamURL, module, ver string, pub publish.Publisher,
) (publish.PublishRequest, error) {
	fc := fetch.NewClient()

	// 1. Probe source.json so we have the upstream URL for the blob
	//    sink's logging identity.
	o.step("resolving %s@%s from %s", module, ver, upstreamURL)
	srcProbe, err := fc.GetSourceJSON(ctx, upstreamURL, module, ver)
	if err != nil {
		return publish.PublishRequest{}, fmt.Errorf("fetch source.json: %w", err)
	}

	// 2. Open the publisher's blob sink. The archive bytes will tee
	//    here as resolve streams + integrity-verifies.
	sink, err := pub.BeginBlob(ctx, srcProbe.URL)
	if err != nil {
		return publish.PublishRequest{}, fmt.Errorf("begin blob: %w", err)
	}
	abortOnFail := func() { sink.Abort() }

	// 3. Resolve with Tee → sink. Resolve also extracts to a temp dir
	//    (which we don't actually need here; the caller can ignore
	//    Materialized.Dir).
	m, err := resolve.FromRegistryWithClient(ctx, fc, upstreamURL, module, ver, resolve.Options{
		Tee:          sink,
		CaptureBytes: true,
	})
	if err != nil {
		abortOnFail()
		return publish.PublishRequest{}, fmt.Errorf("resolve: %w", err)
	}
	defer m.Cleanup()

	ref, err := sink.Close()
	if err != nil {
		return publish.PublishRequest{}, fmt.Errorf("close blob: %w", err)
	}
	o.verboseLog("blob: %d bytes, integrity %s", ref.Bytes, ref.Integrity)

	// 4. Fetch upstream metadata.json so the local merge can lift
	//    homepage/maintainers/repository/yanked_versions.
	upstreamMeta, err := fc.GetMetadataBytes(ctx, upstreamURL, module)
	if err != nil {
		// Non-fatal: not every registry serves metadata.json, and
		// the local merge handles nil upstreamBytes gracefully.
		o.verboseLog("upstream metadata.json not available: %v", err)
		upstreamMeta = nil
	}

	return publish.PublishRequest{
		Module:           module,
		Version:          ver,
		SourceJSON:       m.SourceBytes,
		ModuleBazel:      m.ModuleBytes,
		UpstreamMetadata: upstreamMeta,
		Blob:             ref,
		SourceURL:        srcProbe.URL,
	}, nil
}
