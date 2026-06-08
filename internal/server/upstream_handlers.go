package server

import (
	"context"
	"net/http"

	"github.com/albertocavalcante/bzlhub/internal/api"
	"github.com/albertocavalcante/bzlhub/internal/backend"
)

// collisionReader is the slice of the Canopy contract that the
// upstreams endpoint uses to surface Plan 16 Layer D state. Defined
// here (not on api.Canopy) because the federation collision audit
// is only meaningful when canopy is running with --root + a store;
// requiring it on the base interface would force every mock to
// implement an audit path that doesn't apply.
type collisionReader interface {
	CollisionsCount(ctx context.Context) (int, error)
	CollisionsSample(ctx context.Context, limit int) ([]api.ModuleCollisionInfo, error)
}

// apiGetUpstreams reports the federation backend's current state:
// primary kind + per-upstream reachability snapshot (Plan 16 F3).
//
// When canopy serves a non-federated config (no --upstream flag /
// BZLHUB_UPSTREAMS env), Upstreams is the empty array; clients can
// treat that as "federation disabled."
//
// Introspection is via interface assertion against the backend:
// we don't add Primary/Upstreams to the base Backend interface
// because they're only meaningful for Cascade. A non-Cascade backend
// (File alone) reports only the primary kind.
func (h *handler) apiGetUpstreams(w http.ResponseWriter, r *http.Request) {
	resp := api.UpstreamsResponse{
		Primary:   api.PrimaryInfo{Kind: "none"},
		Upstreams: []api.UpstreamInfo{},
	}
	// Type-switch over the backends we know about. Order matters:
	// Cascade wraps a primary, so check Cascade first and recurse
	// into its primary for kind reporting.
	primary := h.b
	if c, ok := h.b.(*backend.Cascade); ok {
		for _, u := range c.Upstreams() {
			reachable, lastProbe, latency, errMsg := u.Reachable()
			resp.Upstreams = append(resp.Upstreams, api.UpstreamInfo{
				URL:                u.URL,
				Reachable:          reachable,
				LastProbe:          lastProbe,
				LastProbeLatencyMs: latency.Milliseconds(),
				LastProbeErrorMsg:  errMsg,
			})
		}
		cs := c.CacheStats()
		resp.CacheStats = api.CacheStatsInfo{
			Entries: cs.Entries,
			Hits:    cs.Hits,
			Misses:  cs.Misses,
		}
		primary = c.Primary()
	}
	if f, ok := primary.(*backend.File); ok {
		resp.Primary = api.PrimaryInfo{Kind: "local", Root: f.Root}
	}
	// Plan 16 Layer D: collisions_count + collisions_sample. Read
	// from the store when one is wired; otherwise zero+empty. The
	// store interface lookup is via type assertion against the
	// Canopy svc - keeps the api package out of store imports.
	if cl, ok := h.c.(collisionReader); ok {
		ctx := r.Context()
		count, err := cl.CollisionsCount(ctx)
		if err == nil {
			resp.CollisionsCount = count
		}
		sample, err := cl.CollisionsSample(ctx, 10)
		if err == nil {
			resp.CollisionsSample = make([]api.ModuleCollisionInfo, 0, len(sample))
			for _, c := range sample {
				resp.CollisionsSample = append(resp.CollisionsSample, api.ModuleCollisionInfo{
					Module:     c.Module,
					Version:    c.Version,
					ServedFrom: c.ServedFrom,
					Shadowed:   c.Shadowed,
					LastSeen:   c.LastSeen,
				})
			}
		}
	}
	// Always serialize as an array, never null: the wire contract
	// distinguishes "no collisions yet" from "field missing" by
	// presence of the empty array.
	if resp.CollisionsSample == nil {
		resp.CollisionsSample = []api.ModuleCollisionInfo{}
	}
	writeJSON(w, http.StatusOK, resp)
}
