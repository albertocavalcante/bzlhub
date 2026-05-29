package publish

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/albertocavalcante/canopy/internal/mirror"
)

// FilesystemPublisher writes BCR-shape entries directly to a destination
// root. It is the publishing equivalent of FileBackend on the read side:
// no git, no forge, no network. Wraps internal/mirror without changing
// its on-disk format, so a tree produced by this publisher is byte-for-
// byte the same as one produced by today's `ingest --mirror-to`.
type FilesystemPublisher struct {
	root   string
	writer *mirror.Writer
}

// NewFilesystem returns a publisher rooted at root. The directory is
// created if absent, and bazel_registry.json is initialized if needed.
func NewFilesystem(root string) (*FilesystemPublisher, error) {
	mw, err := mirror.New(root)
	if err != nil {
		return nil, fmt.Errorf("init mirror at %s: %w", root, err)
	}
	if err := mw.EnsureRegistryJSON(); err != nil {
		return nil, fmt.Errorf("ensure bazel_registry.json: %w", err)
	}
	return &FilesystemPublisher{root: root, writer: mw}, nil
}

func (p *FilesystemPublisher) BeginBlob(_ context.Context, srcURL string) (BlobSink, error) {
	inner, err := p.writer.BlobWriter(srcURL)
	if err != nil {
		return nil, fmt.Errorf("open blob sink: %w", err)
	}
	return &fsBlobSink{inner: inner}, nil
}

func (p *FilesystemPublisher) Publish(_ context.Context, req PublishRequest) (Receipt, error) {
	if err := ValidateRequest(req); err != nil {
		return Receipt{}, err
	}
	if err := p.writer.WriteSource(req.Module, req.Version, req.SourceJSON); err != nil {
		return Receipt{}, fmt.Errorf("write source.json: %w", err)
	}
	if len(req.ModuleBazel) > 0 {
		if err := p.writer.WriteModuleBazel(req.Module, req.Version, req.ModuleBazel); err != nil {
			return Receipt{}, fmt.Errorf("write MODULE.bazel: %w", err)
		}
	}
	if err := p.writer.MergeMetadataWithUpstream(req.Module, req.Version, req.UpstreamMetadata); err != nil {
		return Receipt{}, fmt.Errorf("merge metadata.json: %w", err)
	}
	return Receipt{
		Strategy:    "filesystem",
		DiskPath:    filepath.Join(p.root, "modules", req.Module, req.Version),
		Diff:        fmt.Sprintf("filesystem mirror: %s@%s", req.Module, req.Version),
		PublishedAt: time.Now().UTC(),
	}, nil
}

// fsBlobSink adapts mirror.BlobSink to the publish.BlobSink interface.
// The shape is identical; the wrapper exists so callers depend only on
// the publish package's types.
type fsBlobSink struct {
	inner *mirror.BlobSink
}

func (s *fsBlobSink) Write(b []byte) (int, error) { return s.inner.Write(b) }

func (s *fsBlobSink) Close() (BlobRef, error) {
	path, integrity, n, err := s.inner.Close()
	if err != nil {
		return BlobRef{}, fmt.Errorf("close blob: %w", err)
	}
	return BlobRef{Key: path, Integrity: integrity, Bytes: n}, nil
}

func (s *fsBlobSink) Abort() { s.inner.Abort() }

// Compile-time assertion that FilesystemPublisher satisfies Publisher.
var _ Publisher = (*FilesystemPublisher)(nil)
