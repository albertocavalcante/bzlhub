// Package canopy is the concrete implementation of the api.Canopy interface.
// It composes Store (the search index) and the ingestion pipeline behind a
// single Go object. REST/MCP/CLI all call into Service; transports never
// touch Store directly.
package canopy

import (
	"context"
	"log/slog"

	"github.com/albertocavalcante/assay/report"
	bcrmirror "github.com/albertocavalcante/go-bcr-mirror"

	"github.com/albertocavalcante/canopy/internal/api"
	"github.com/albertocavalcante/canopy/internal/auth"
	"github.com/albertocavalcante/canopy/internal/eventbus"
	"github.com/albertocavalcante/canopy/internal/githubapi/token"
	"github.com/albertocavalcante/canopy/internal/githubmeta"
	"github.com/albertocavalcante/canopy/internal/store"
)

// Service implements api.Canopy.
//
// MirrorRoot is the filesystem path serving the BCR-shape mirror. It's
// optional — only Drift and Bump require it. Empty MirrorRoot makes
// those return a clear "not configured" error rather than crashing.
//
// DefaultUpstream is the registry URL used when DriftOptions.Upstream or
// BumpOptions.Upstream is empty.
//
// Bus, if non-nil, receives lifecycle events (module_indexed on successful
// Bump/IngestDir). The HTTP server's /api/events forwards them as SSE.
// Tests typically leave it nil.
type Service struct {
	store           *store.Store
	MirrorRoot      string
	DefaultUpstream string
	Bus             *eventbus.Bus

	// AttrsInterpret turns on the Tier-3 (assay/interp) attrs
	// extractor after IngestDir/Bump. When true, rules whose attrs
	// couldn't be statically extracted are re-resolved by evaluating
	// the source .bzl in a sandboxed interpreter. See
	// featureflags.Flags.AttrsInterpret for the operator-facing knob.
	AttrsInterpret bool

	// SourcesCacheDir is the on-disk cache where Bump unpacks tarball
	// trees per (module, version). Used by Summary() to locate the
	// source root that bazel-module-summary-go reads from.
	SourcesCacheDir string

	// GitHubMeta, when non-nil, enables stars/forks/languages refresh
	// from the GitHub REST API. Left nil disables the feature
	// entirely (RefreshGitHubMeta* become no-ops). Wired by
	// cmd/canopy/main.go from CANOPY_GITHUB_META_ENABLED + the
	// TokenProvider snapshot.
	GitHubMeta *githubmeta.Client

	// GitHubToken, when non-nil, is the credential source used for
	// non-githubmeta GitHub API calls (e.g. the BCR HEAD SHA probe
	// in bcrprov.go). Optional; nil falls back to anonymous (which
	// is fine given the 5-min TTL cache).
	GitHubToken token.Provider

	// bcrProvCache caches the BCR HEAD SHA for ~5 min so a recursive
	// ingest doesn't spend one GitHub API call per module. See
	// bcrprov.go.
	bcrProvCache bcrProv

	// mirror is the git-aware BCR mirror handle. Non-nil when
	// backend.NewFromRoot detected <root>/.git at boot and wired
	// the Mirror via UseMirror. Drives the git-aware drift
	// backfill (PR7) — when nil, BackfillDriftSummary stays a
	// no-op and operators fall back to the HTTP-probe `canopy
	// drift` CLI verb.
	mirror *bcrmirror.Mirror
}

// UseMirror attaches an opened bcrmirror.Mirror to the Service. Wired
// from cmd/canopy/serve.go after backend.NewFromRoot returns a
// *backend.BCRMirror. Calling with nil is a no-op (so the wiring
// code can call unconditionally and let auto-detect pick the path).
func (s *Service) UseMirror(m *bcrmirror.Mirror) {
	s.mirror = m
}

// New builds a Service backed by the given store.
func New(s *store.Store) *Service {
	return &Service{
		store:           s,
		DefaultUpstream: "https://bcr.bazel.build",
	}
}

// emit is a nil-safe shim so Service callers don't have to check.
func (s *Service) emit(kind string, data any) {
	if s.Bus != nil {
		s.Bus.Publish(eventbus.Event{Kind: kind, Data: data})
	}
}

// audit writes one audit_events row. Failures to write are logged but
// never propagated to the user — losing an audit entry is far less bad
// than failing a successful business operation because the audit table
// is momentarily wedged.
//
// Pulls the authenticated identity (when present) from ctx so write
// operations are recorded with who-did-what. Pre-auth-scaffold
// requests and anonymous reads pass through with UserID empty.
func (s *Service) audit(ctx context.Context, ev store.AuditEvent) {
	if ev.UserID == "" {
		if id, ok := auth.FromContext(ctx); ok && id.IsAuthenticated() {
			ev.UserID = id.DisplayName()
		}
	}
	if err := s.store.RecordAudit(ctx, ev); err != nil {
		slog.Warn("audit write failed", "err", err, "kind", ev.Kind, "module", ev.Module)
	}
	// Also publish on the bus so live subscribers see the entry without
	// polling. Kind "audit_recorded" is distinct from the per-domain
	// event kinds (module_indexed etc.) so subscribers can filter.
	if s.Bus != nil {
		s.Bus.Publish(eventbus.Event{Kind: "audit_recorded", Data: ev})
	}
}

// Subscribe satisfies api.EventSubscriber so the HTTP server can forward
// bus events as SSE without importing this package directly. Returns a
// nil channel + no-op unsubscribe when no bus is configured, letting the
// caller fall back to the keep-alive-only stream.
func (s *Service) Subscribe(buf int) (<-chan api.SSEEvent, func()) {
	if s.Bus == nil {
		return nil, func() {}
	}
	raw, unsub := s.Bus.Subscribe(buf)
	out := make(chan api.SSEEvent, buf)
	go func() {
		defer close(out)
		for e := range raw {
			out <- api.SSEEvent{Kind: e.Kind, Data: e.Data}
		}
	}()
	return out, unsub
}

// ModuleIndexedEvent is the payload published on every successful Bump
// or local IngestDir. It carries the canonical coordinate, the rule/
// provider/macro counts (cheap to compute and useful for the UI to
// avoid a second round-trip), and the hermeticity profile snapshot.
type ModuleIndexedEvent struct {
	Module      string                    `json:"module"`
	Version     string                    `json:"version"`
	Rules       int                       `json:"rules"`
	Providers   int                       `json:"providers"`
	Macros      int                       `json:"macros"`
	Hermeticity []report.HermeticityClass `json:"hermeticity,omitempty"`
}

func eventFromReport(r *report.ModuleReport) ModuleIndexedEvent {
	return ModuleIndexedEvent{
		Module:      r.Name,
		Version:     r.Version,
		Rules:       len(r.Rules),
		Providers:   len(r.Providers),
		Macros:      len(r.Macros),
		Hermeticity: r.Hermeticity.Classes,
	}
}

// Compile-time interface check.
var _ api.Canopy = (*Service)(nil)
