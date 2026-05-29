package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/albertocavalcante/canopy/internal/api"
	"github.com/albertocavalcante/canopy/internal/bcrprobe"
	"github.com/albertocavalcante/canopy/internal/version"
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
// deps; always routed even when both backend and canopy.Service are
// nil (useful for liveness probes / deploy verification).
func (h *handler) apiVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"version":  version.Version,
		"commit":   version.Commit,
		"built_at": version.BuiltAt,
	})
}
