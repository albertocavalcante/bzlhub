package canopy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	bzlsummary "github.com/albertocavalcante/bazel-module-summary-go"

	"github.com/albertocavalcante/canopy/internal/githubmeta"
)

// RefreshGitHubMeta fetches the GitHub repo + languages for one
// module and persists the result. Resolves the (owner, repo) pair
// from the module's mirrored metadata.json (the BCR-lifted
// `repository` array, falling back to the homepage URL). No-op when
// the service has no GitHub client configured, or when the module's
// metadata doesn't surface a github.com repo identity.
//
// Returns an error only for unexpected failures (DB write, JSON
// marshal). Rate-limit hits and 404s are persisted as terminal
// states (http_status=429 / 404) so the next refresh can decide what
// to do without re-discovering the failure mode.
func (s *Service) RefreshGitHubMeta(ctx context.Context, module string) error {
	if s.GitHubMeta == nil || s.store == nil {
		return nil
	}
	owner, repo, ok := s.resolveGitHubRepo(module)
	if !ok {
		return nil
	}
	prior, _ := s.store.GetGitHubMeta(ctx, module)
	priorETag := ""
	if prior != nil {
		priorETag = prior.Meta.ETag
	}
	meta, err := s.GitHubMeta.Fetch(ctx, owner, repo, priorETag)
	switch {
	case err == nil:
		return s.store.UpsertGitHubMeta(ctx, module, *meta, 200)
	case errors.Is(err, githubmeta.ErrNotModified):
		if prior == nil {
			return nil
		}
		bumped := prior.Meta
		bumped.FetchedAt = time.Now().UTC()
		return s.store.UpsertGitHubMeta(ctx, module, bumped, 304)
	case errors.Is(err, githubmeta.ErrNotFound):
		// Persist the negative result so we don't re-query a
		// renamed/deleted repo every sweep. The flat columns carry
		// what we know (owner/repo); meta_json stays minimal.
		neg := githubmeta.Meta{Owner: owner, Repo: repo, FetchedAt: time.Now().UTC()}
		return s.store.UpsertGitHubMeta(ctx, module, neg, 404)
	case errors.Is(err, githubmeta.ErrRateLimited):
		if prior != nil {
			bumped := prior.Meta
			bumped.FetchedAt = time.Now().UTC()
			return s.store.UpsertGitHubMeta(ctx, module, bumped, 429)
		}
		// Nothing to preserve; record the rate-limit so subsequent
		// sweeps see a row and back off.
		neg := githubmeta.Meta{Owner: owner, Repo: repo, FetchedAt: time.Now().UTC()}
		return s.store.UpsertGitHubMeta(ctx, module, neg, 429)
	default:
		// Transient transport error: leave the row alone, surface
		// to the caller so the sweep can log + continue.
		return fmt.Errorf("fetch github meta %s/%s: %w", owner, repo, err)
	}
}

// GetGitHubMeta returns the cached social-signals payload for a
// module, or nil when no refresh has produced one yet. Thin
// pass-through to the store so the server layer can render without
// holding a *store.Store directly. 404/429 rows are hidden — those
// carry no useful social signal.
func (s *Service) GetGitHubMeta(ctx context.Context, module string) (*githubmeta.Meta, error) {
	if s.store == nil {
		return nil, nil
	}
	row, err := s.store.GetGitHubMeta(ctx, module)
	if err != nil || row == nil {
		return nil, err
	}
	if row.HTTPStatus != 200 && row.HTTPStatus != 304 {
		return nil, nil
	}
	m := row.Meta
	return &m, nil
}

// RefreshGitHubMetaAll walks every indexed module and refreshes
// staleness-ordered, capped at `budget` HTTP calls so the sweep
// stays inside GitHub's per-hour bucket. budget <= 0 means "until
// the candidate list is exhausted" — useful for one-shot CLI
// invocations.
//
// Cancellation honors ctx; partial progress is preserved (every
// refresh persists independently before the next call).
func (s *Service) RefreshGitHubMetaAll(ctx context.Context, budget int) (refreshed int, err error) {
	if s.GitHubMeta == nil || s.store == nil {
		return 0, nil
	}
	cands, err := s.store.ListGitHubMetaCandidates(ctx)
	if err != nil {
		return 0, err
	}
	for _, c := range cands {
		if budget > 0 && refreshed >= budget {
			break
		}
		if ctx.Err() != nil {
			return refreshed, ctx.Err()
		}
		if err := s.RefreshGitHubMeta(ctx, c.Module); err != nil {
			slog.Warn("github meta refresh failed", "module", c.Module, "err", err)
			continue
		}
		refreshed++
	}
	return refreshed, nil
}

// resolveGitHubRepo looks up the (owner, repo) for a module from
// its mirrored metadata.json. Returns ok=false when the module has
// no recognizable github.com identity — caller should skip rather
// than fail.
func (s *Service) resolveGitHubRepo(module string) (owner, repo string, ok bool) {
	if s.MirrorRoot == "" {
		return "", "", false
	}
	metaPath := filepath.Join(s.MirrorRoot, "modules", module, "metadata.json")
	meta, err := bzlsummary.ReadMetadataJSON(metaPath)
	if err != nil || meta == nil {
		return "", "", false
	}
	label := deriveRepoLabel(meta.Repository, meta.Homepage)
	return githubmeta.ParseRepoLabel(label)
}
