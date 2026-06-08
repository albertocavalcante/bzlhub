package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/albertocavalcante/bzlhub/internal/egress"
	"github.com/albertocavalcante/bzlhub/internal/store"
)

// WebhookSource is the slice of *store.Store the webhook daemon
// needs. Interface keeps tests independent of SQLite.
type WebhookSource interface {
	MaxAuditID(ctx context.Context) (int64, error)
	ListAuditAfterID(ctx context.Context, afterID int64, limit int) ([]store.AuditEvent, error)
}

// WebhookOptions configures a WebhookDaemon.
type WebhookOptions struct {
	// URL is the POST target. Empty string disables the daemon —
	// Run returns immediately.
	URL string

	// Interval is the poll cadence. 0 → 30s default.
	Interval time.Duration

	// BatchSize caps the number of events fetched per tick.
	// 0 → 100 default.
	BatchSize int

	// Client is the HTTP client used for delivery. Nil →
	// http.DefaultClient with a 10s timeout.
	Client *http.Client

	Log *slog.Logger
}

const (
	defaultWebhookInterval  = 30 * time.Second
	defaultWebhookBatchSize = 100
	defaultWebhookTimeout   = 10 * time.Second
)

// WebhookDaemon ships newly-recorded audit events to an HTTP
// endpoint via best-effort-once POSTs.
//
// Watermark semantics: the daemon stamps its boot watermark at
// MaxAuditID(store) on start, so events recorded before canopy
// started are NOT re-delivered. After that every new event with
// id > watermark is POSTed once. Failed deliveries still advance
// the watermark — we don't retry, to avoid spamming the endpoint
// when it's hard-down. Operators wanting at-least-once should
// front the webhook with a queue (or pull from the audit table
// directly).
type WebhookDaemon struct {
	source    WebhookSource
	url       string
	interval  time.Duration
	batchSize int
	client    *http.Client
	log       *slog.Logger

	// watermark = the highest audit id we've considered for
	// delivery (regardless of success). Atomically updated so
	// the boot stamp from the constructor and the per-sweep
	// advances stay race-free.
	watermark atomic.Int64

	// deliveries counts POST attempts (success + failure).
	// Exported via the test seam; not part of the production
	// observability surface.
	deliveries atomic.Int64
}

// NewWebhookDaemon constructs a daemon. Panics on nil source —
// misconfiguration, not a runtime condition.
//
// The boot watermark (highest audit id at start) is stamped
// synchronously here so events recorded between NewWebhookDaemon
// and `go d.Run(ctx)` are picked up by the first sweep rather
// than racing with the goroutine's startup.
func NewWebhookDaemon(source WebhookSource, opts WebhookOptions) *WebhookDaemon {
	if source == nil {
		panic("audit.NewWebhookDaemon: source is required")
	}
	if opts.Interval <= 0 {
		opts.Interval = defaultWebhookInterval
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = defaultWebhookBatchSize
	}
	if opts.Client == nil {
		opts.Client = egress.NewHTTPClient(egress.Policy{})
		opts.Client.Timeout = defaultWebhookTimeout
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	d := &WebhookDaemon{
		source:    source,
		url:       opts.URL,
		interval:  opts.Interval,
		batchSize: opts.BatchSize,
		client:    opts.Client,
		log:       opts.Log,
	}
	// Stamp boot watermark synchronously when URL is wired.
	// Failure is non-fatal; we start from 0 (events recorded
	// before canopy gets replayed once — operator-visible).
	if opts.URL != "" {
		if max, err := source.MaxAuditID(context.Background()); err == nil {
			d.watermark.Store(max)
		} else {
			d.log.Warn("audit webhook: initial watermark read failed; starting from 0",
				"err", err)
		}
	}
	return d
}

// Run starts the delivery loop and blocks until ctx is cancelled.
// When URL is empty, returns immediately.
func (d *WebhookDaemon) Run(ctx context.Context) {
	if d.url == "" {
		d.log.Info("audit webhook disabled (no URL configured)")
		return
	}
	d.log.Info("audit webhook daemon starting",
		"url", d.url, "interval", d.interval, "watermark", d.watermark.Load())

	tick := time.NewTicker(d.interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			d.log.Info("audit webhook daemon stopped")
			return
		case <-tick.C:
			d.sweep(ctx)
		}
	}
}

// sweep fetches everything past the current watermark and POSTs
// each event, advancing the watermark as it goes (even on POST
// failure — see daemon doc).
func (d *WebhookDaemon) sweep(ctx context.Context) {
	events, err := d.source.ListAuditAfterID(ctx, d.watermark.Load(), d.batchSize)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		d.log.Warn("audit webhook: list failed", "err", err)
		return
	}
	for _, ev := range events {
		if ctx.Err() != nil {
			return
		}
		d.deliver(ctx, ev)
		if ev.ID > d.watermark.Load() {
			d.watermark.Store(ev.ID)
		}
	}
}

// deliver POSTs one event. Logs failure but doesn't surface — the
// daemon's contract is best-effort-once.
func (d *WebhookDaemon) deliver(ctx context.Context, ev store.AuditEvent) {
	defer d.deliveries.Add(1)
	body, err := json.Marshal(ev)
	if err != nil {
		d.log.Warn("audit webhook: marshal failed", "err", err, "id", ev.ID)
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.url, bytes.NewReader(body))
	if err != nil {
		d.log.Warn("audit webhook: build request failed", "err", err, "id", ev.ID)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		d.log.Warn("audit webhook: POST failed", "err", err, "id", ev.ID, "url", d.url)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		d.log.Warn("audit webhook: non-2xx response",
			"status", resp.StatusCode, "id", ev.ID, "url", d.url)
		return
	}
	// Drain body so the connection can be reused.
	_, _ = io.Copy(io.Discard, resp.Body)
}
