package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

func (h *handler) apiError(w http.ResponseWriter, err error) {
	h.log.Error("api error", "err", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(body)
}

// writeJSONWithETag is writeJSON + content-hash ETag for conditional
// GETs. Used by surface endpoints (External, Airgap) so the UI can
// re-fetch on tab clicks without re-downloading unchanged payloads.
// On If-None-Match hit, returns 304 with no body.
//
// Marshals the body to compute the hash; this isn't free, but the
// surface payloads are small (≤ ~100KB typical) and the round-trip
// savings on the client compensate.
func writeJSONWithETag(w http.ResponseWriter, r *http.Request, status int, body any) {
	buf, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sum := sha256.Sum256(buf)
	etag := `"` + hex.EncodeToString(sum[:]) + `"`
	w.Header().Set("ETag", etag)
	// no-cache + ETag = "revalidate on every request, but a matching
	// ETag short-circuits to 304." Right call here: data changes on
	// re-ingest, never on a fixed schedule.
	w.Header().Set("Cache-Control", "no-cache")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(buf)
}

func atoiOrDefault(s string, def int) int {
	if s == "" {
		return def
	}
	var n int
	for _, r := range s {
		if r < '0' || r > '9' {
			return def
		}
		n = n*10 + int(r-'0')
		if n > 10000 {
			return 10000
		}
	}
	return n
}

// accessLog returns chi middleware that logs one INFO line per request:
//
//	method, path, status, bytes, duration, user-agent.
//
// Useful for verifying who actually hit the registry (Bazel vs. cache).
func accessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: 200}
			next.ServeHTTP(rec, r)
			logger.Info("req",
				"method", r.Method, "path", r.URL.Path, "status", rec.status,
				"bytes", rec.bytes, "dur", time.Since(start).String(),
				"ua", r.UserAgent(),
			)
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) { s.status = code; s.ResponseWriter.WriteHeader(code) }
func (s *statusRecorder) Write(b []byte) (int, error) {
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// Flush forwards to the underlying ResponseWriter if it supports it. SSE
// handlers type-assert for http.Flusher, so the recorder must too.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
