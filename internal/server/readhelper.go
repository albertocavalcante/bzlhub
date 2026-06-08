package server

import (
	"context"

	"github.com/albertocavalcante/bzlhub/internal/api"
	"github.com/albertocavalcante/bzlhub/internal/bzlhub"
	"github.com/albertocavalcante/bzlhub/internal/githubmeta"
	"github.com/albertocavalcante/bzlhub/internal/store"
)

// ReadHelper is the read-side surface server.go needs beyond
// api.Canopy. These methods exist on *bzlhub.Service but don't
// belong on the cross-transport api.Canopy contract — MCP and CLI
// callers don't need pin counts or GitHub-meta. Defining the
// dependency at the server layer (where it's actually used) lets
// api.Canopy stay narrow without forcing handler code to type-assert
// to the concrete service on every augmentation.
//
// Wired by main via Options.Helper; tests typically leave it nil,
// in which case the augmentation paths gracefully no-op (same shape
// as before the abstraction existed).
type ReadHelper interface {
	// Per-row version metadata: ingest timestamp, compat level,
	// tarball size. Used to build the versions-list rows.
	ListVersionsWithMeta(ctx context.Context, name string) ([]store.VersionRow, error)
	// Corpus-wide adoption counts: how many consumers reference
	// each dep name (any version). Cached at the service layer.
	ComputeUsageCounts(ctx context.Context) (map[string]int, error)
	// Per-version variant: usage[dep][version] = consumer count.
	// Powers the pin-count chips on the versions list.
	ComputeUsageCountsByVersion(ctx context.Context) (map[string]map[string]int, error)
	// Tarball size lookup for a single (module, version). 0 when
	// pre-migration or unknown.
	GetTarballSize(ctx context.Context, module, version string) (int64, error)
	// GitHub social-signals payload, or nil when the refresher
	// hasn't fetched the module yet.
	GetGitHubMeta(ctx context.Context, module string) (*githubmeta.Meta, error)
	// Aggregate corpus counters (module count, version count,
	// documented-symbols total) for the home-page dashboard.
	ComputeCorpusStats(ctx context.Context) (*bzlhub.CorpusStats, error)
	// Latest BCR provenance (bump_success audit payload) for one
	// (module, version). Nil when no BCR-stamped bump exists.
	GetLatestBumpProvenance(ctx context.Context, module, version string) (*api.BumpProvenance, error)
}
