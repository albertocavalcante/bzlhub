package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/albertocavalcante/bzlhub/internal/api"
	"github.com/albertocavalcante/bzlhub/internal/backend"
	"github.com/albertocavalcante/bzlhub/internal/bcrprobe"
	"github.com/albertocavalcante/bzlhub/internal/bzlhub/health"
	"github.com/albertocavalcante/bzlhub/internal/version"
)

// apiFeatures publishes the UI-safe feature-flag snapshot.
//
// Intentionally read-only and unauthenticated: the UI uses it to
// decide whether to render write affordances. Server-internal flags
// (registry URL, rate-limit bypass IPs, concurrency caps) are excluded
// at the type level via featureflags.PublicSnapshot.
func (h *handler) apiFeatures(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.opts.Flags.Public())
}

// apiBCRProbe does a fast read-only "does this (module, version)
// exist?" check against the configured upstream registry. Two HTTP
// hops at most: source.json first; if 404, metadata.json so we can
// echo back the versions that DO exist for that module.
//
// Errors other than 404 (5xx, TLS, DNS) are surfaced as 502 Bad
// Gateway with the underlying message: the UI then renders "registry
// temporarily unreachable" rather than confidently lying about
// whether the module exists.
func (h *handler) apiBCRProbe(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	module := q.Get("module")
	version := q.Get("version")
	if module == "" || version == "" {
		http.Error(w, "module and version query params are required", http.StatusBadRequest)
		return
	}
	res, err := bcrprobe.Probe(r.Context(), h.bcrFetch, h.opts.Flags.RegistryURL, module, version)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "upstream registry unreachable: " + err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// apiEvents is a Server-Sent Events stream. When the configured Canopy
// service implements api.EventSubscriber, events from the bus
// (module_indexed, etc.) are forwarded as `event: <kind>\ndata: <json>`
// frames. Otherwise the stream is keep-alive-only.
func (h *handler) apiEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Subscribe to bus events when supported. Buffer is generous: a
	// noisy ingest can fire dozens of module_indexed events per second
	// during a recursive walk; we'd rather buffer than drop.
	var (
		events <-chan api.SSEEvent
		unsub  func() = func() {}
	)
	if sub, ok := h.c.(api.EventSubscriber); ok {
		events, unsub = sub.Subscribe(256)
	}
	defer unsub()

	// Send an initial comment so the client knows it's connected.
	_, _ = w.Write([]byte(": connected\n\n"))
	flusher.Flush()

	// Keep-alive ping every 25s; otherwise idle.
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case ev, open := <-events:
			if !open {
				// Subscription closed (bus shutdown). End the stream;
				// the client will reconnect via EventSource's default.
				return
			}
			data, err := json.Marshal(ev.Data)
			if err != nil {
				continue
			}
			frame := "event: " + ev.Kind + "\ndata: " + string(data) + "\n\n"
			if _, err := w.Write([]byte(frame)); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// apiVersion returns build metadata populated via -ldflags. Stable
// JSON shape: {"version","commit","built_at"}. Carries no service
// deps; always routed even when both backend and bzlhub.Service are
// nil (useful for liveness probes / deploy verification).
func (h *handler) apiVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"version":  version.Version,
		"commit":   version.Commit,
		"built_at": version.BuiltAt,
	})
}

// apiStatus returns the single-shot human-and-monitor-shaped
// snapshot of this canopy instance. Contract: plan-65 v2 §Part 3.
// Drives the /status page (15s polling) and any external poller that
// wants one JSON read per probe.
//
// Composition rules (also in the api.SystemStatus doc comment):
//   - Every field is derived from state canopy already tracks.
//   - Fields with no honest source are omitted (omitempty) rather
//     than emitted as theatrical 0 / null.
//   - No probes happen inside this handler. The federation
//     reachability snapshot reads the last cached probe state from
//     backend.Cascade (refreshed by the cascade's own goroutine);
//     the drift counts read cached per-module DriftSummary already
//     persisted into ModuleSummary. Both are O(modules) loops over
//     in-memory data; no upstream HTTP, no SQL aggregates beyond
//     what ListModules already pays for.
//
// Cache-Control: no-store so reverse proxies / Cloudflare don't
// serve stale state to the next visitor.
func (h *handler) apiStatus(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	status := api.SystemStatus{
		Version:       version.Version,
		Commit:        version.Commit,
		BuiltAt:       version.BuiltAt,
		UptimeSeconds: int64(now.Sub(h.startedAt).Seconds()),
		Federation:    api.FederationStatus{Upstreams: []api.UpstreamStatus{}},
	}

	// Mirror snapshot — derived from ListModules. Walks each module
	// summary once: sum versions, find max LatestIngestedAt. Keeps
	// the request O(modules); on a 27-module mirror this is ~µs.
	if h.c != nil {
		if mods, err := h.c.ListModules(r.Context()); err == nil {
			status.Mirror.ModulesIndexed = len(mods)
			var latest string
			var behind, yanked int
			var latestDriftRefresh string
			for _, m := range mods {
				status.Mirror.VersionsIndexed += m.VersionCount
				if m.LatestIngestedAt > latest {
					latest = m.LatestIngestedAt
				}
				switch m.Drift.Status {
				case api.DriftStatusBehind:
					behind++
				case api.DriftStatusYankedUpstream:
					yanked++
				}
				if m.Drift.Status != "" && m.LatestIngestedAt > latestDriftRefresh {
					latestDriftRefresh = m.LatestIngestedAt
				}
			}
			status.Mirror.LastIngestAt = latest
			status.Drift = api.DriftStatusInfo{
				LastRefreshAt:         latestDriftRefresh,
				ModulesBehind:         behind,
				ModulesYankedUpstream: yanked,
			}
		}

		// Optional interface — implementations without a Mirror
		// (mocks, File-backed) skip.
		if mh, ok := h.c.(api.MirrorHeader); ok {
			sha, lastSync := mh.MirrorHead(r.Context())
			status.Mirror.HeadSHA = sha
			if !lastSync.IsZero() {
				status.Mirror.LastSyncAt = lastSync.UTC().Format(time.RFC3339)
			}
		}
	}

	// Federation snapshot — same data source as /api/v1/upstreams,
	// reshaped. Read the cascade's last-known probe state per
	// upstream + the shared response-cache stats. When the cascade
	// is absent (non-federated config) Upstreams stays the empty
	// array.
	if c, ok := h.b.(*backend.Cascade); ok {
		cs := c.CacheStats()
		hitRate := 0.0
		if lookups := cs.Hits + cs.Misses; lookups > 0 {
			hitRate = float64(cs.Hits) / float64(lookups)
		}
		for _, u := range c.Upstreams() {
			reachable, lastProbe, latency, errMsg := u.Reachable()
			entry := api.UpstreamStatus{
				URL:          u.URL,
				Reachable:    reachable,
				CacheEntries: cs.Entries,
				CacheHitRate: hitRate,
			}
			if !lastProbe.IsZero() {
				entry.LastProbeAt = lastProbe.UTC().Format(time.RFC3339)
				entry.LastProbeLatencyMs = latency.Milliseconds()
			}
			entry.LastProbeError = errMsg
			status.Federation.Upstreams = append(status.Federation.Upstreams, entry)
		}
	}

	// Addons. promote_on_serve / snapshot_publishing / litestream
	// stay unwired in v0 (the underlying capabilities don't ship
	// yet). mcp_http reflects the live flag so /status answers
	// "is the /mcp endpoint live on this instance?" honestly.
	status.Addons = api.AddonsStatus{
		PromoteOnServe:     false,
		SnapshotPublishing: false,
		Litestream:         false,
		MCPHTTP:            h.opts.Flags.MCPHTTPEnabled,
	}

	// Server-derived instant state. Computed AFTER every source
	// field is populated so the verdict reflects the same payload
	// the wire carries. Thresholds live in internal/canopy/health
	// — single source of truth for both /status (UI) and `canopy
	// status` (CLI).
	status.Computed = health.Derive(status, time.Now())

	w.Header().Set("Cache-Control", "no-store, must-revalidate")
	writeJSON(w, http.StatusOK, status)
}
