package audit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/albertocavalcante/bzlhub/internal/store"
)

// captureServer records every POST body so tests can assert on the
// shape and count of delivered events.
type captureServer struct {
	mu       sync.Mutex
	bodies   []map[string]any
	statuses []int
	failNext int32 // when > 0, the next N requests return 500
}

func (c *captureServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		var parsed map[string]any
		_ = json.Unmarshal(body, &parsed)
		c.mu.Lock()
		c.bodies = append(c.bodies, parsed)
		fail := atomic.LoadInt32(&c.failNext)
		if fail > 0 {
			atomic.AddInt32(&c.failNext, -1)
			c.statuses = append(c.statuses, http.StatusInternalServerError)
			c.mu.Unlock()
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		c.statuses = append(c.statuses, http.StatusOK)
		c.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
}

func (c *captureServer) deliveredCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, s := range c.statuses {
		if s < 400 {
			n++
		}
	}
	return n
}

func newWebhookTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "bzlhub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestWebhook_DeliversNewEvents(t *testing.T) {
	cap := &captureServer{}
	srv := httptest.NewServer(cap.handler())
	defer srv.Close()

	s := newWebhookTestStore(t)
	d := NewWebhookDaemon(s, WebhookOptions{
		URL:      srv.URL,
		Interval: 10 * time.Millisecond,
		Client:   http.DefaultClient,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)

	// Record three events while daemon is running.
	for i := range 3 {
		_ = s.RecordAudit(ctx, store.AuditEvent{
			Kind:   "test_event",
			Source: "test",
			OK:     true,
			Module: "rules_x",
			Version: string(rune('0' + i)),
		})
	}

	// Wait for delivery.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if cap.deliveredCount() >= 3 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := cap.deliveredCount(); got != 3 {
		t.Errorf("delivered = %d, want 3", got)
	}

	// First delivered body should carry the expected fields.
	cap.mu.Lock()
	first := cap.bodies[0]
	cap.mu.Unlock()
	if first["kind"] != "test_event" {
		t.Errorf("body.kind = %v, want test_event", first["kind"])
	}
	if first["module"] != "rules_x" {
		t.Errorf("body.module = %v", first["module"])
	}
}

func TestWebhook_SkipsPreExistingEvents(t *testing.T) {
	// Events recorded BEFORE the daemon starts shouldn't be
	// re-delivered. The daemon's boot watermark is MaxAuditID at
	// start time.
	s := newWebhookTestStore(t)
	ctx := context.Background()
	for range 5 {
		_ = s.RecordAudit(ctx, store.AuditEvent{Kind: "pre_existing", OK: true})
	}

	cap := &captureServer{}
	srv := httptest.NewServer(cap.handler())
	defer srv.Close()

	d := NewWebhookDaemon(s, WebhookOptions{
		URL:      srv.URL,
		Interval: 10 * time.Millisecond,
		Client:   http.DefaultClient,
	})
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go d.Run(runCtx)

	// Give the daemon a couple ticks; nothing new should have
	// been delivered.
	time.Sleep(60 * time.Millisecond)
	if got := cap.deliveredCount(); got != 0 {
		t.Errorf("delivered = %d for pre-existing rows, want 0", got)
	}

	// Now record one new event; daemon should pick it up.
	_ = s.RecordAudit(ctx, store.AuditEvent{Kind: "post_boot", OK: true})
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if cap.deliveredCount() >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := cap.deliveredCount(); got != 1 {
		t.Errorf("delivered = %d, want 1 post-boot event", got)
	}
}

func TestWebhook_EmptyURL_DaemonInert(t *testing.T) {
	s := newWebhookTestStore(t)
	d := NewWebhookDaemon(s, WebhookOptions{URL: "", Interval: 10 * time.Millisecond})

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	d.Run(ctx) // should return immediately

	if d.deliveries.Load() != 0 {
		t.Errorf("inert daemon should not deliver; got %d", d.deliveries.Load())
	}
}

func TestWebhook_FailedDeliveryAdvancesWatermarkAnyway(t *testing.T) {
	// At-least-once would re-deliver on failure; we ship
	// best-effort-once for v0 — a failed POST advances the
	// watermark so we don't spam the endpoint with retries.
	// Documented in the daemon comment + tested here.
	cap := &captureServer{failNext: 1}
	srv := httptest.NewServer(cap.handler())
	defer srv.Close()

	s := newWebhookTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := NewWebhookDaemon(s, WebhookOptions{
		URL:      srv.URL,
		Interval: 10 * time.Millisecond,
		Client:   http.DefaultClient,
	})
	go d.Run(ctx)

	_ = s.RecordAudit(ctx, store.AuditEvent{Kind: "first", OK: true})
	_ = s.RecordAudit(ctx, store.AuditEvent{Kind: "second", OK: true})

	// First POST will 500, second will 200. Both events advance
	// the watermark — second gets exactly one delivery (no retry
	// of the first).
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		cap.mu.Lock()
		total := len(cap.statuses)
		cap.mu.Unlock()
		if total >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if len(cap.statuses) != 2 {
		t.Fatalf("attempts = %d, want 2 (one fail + one success, no retry)", len(cap.statuses))
	}
	if cap.statuses[0] != http.StatusInternalServerError {
		t.Errorf("first attempt status = %d, want 500", cap.statuses[0])
	}
	if cap.statuses[1] != http.StatusOK {
		t.Errorf("second attempt status = %d, want 200", cap.statuses[1])
	}
}

func TestWebhook_GracefulShutdown(t *testing.T) {
	cap := &captureServer{}
	srv := httptest.NewServer(cap.handler())
	defer srv.Close()
	s := newWebhookTestStore(t)

	d := NewWebhookDaemon(s, WebhookOptions{
		URL:      srv.URL,
		Interval: 50 * time.Millisecond,
		Client:   http.DefaultClient,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case <-done:
		// ok
	case <-time.After(200 * time.Millisecond):
		t.Fatal("daemon did not return after ctx cancel")
	}
}
