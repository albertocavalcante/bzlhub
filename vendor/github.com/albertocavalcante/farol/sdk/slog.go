package farol

import (
	"context"
	"errors"
	"log/slog"
	"os"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// newFallbackLogger returns a slog.Logger that writes to stderr only.
// Used before Setup runs and when Setup is in disabled/noop mode.
func newFallbackLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
}

// newOTelLogger returns a slog.Logger that ALWAYS emits to stderr
// AND, when the log signal isn't disabled, ALSO bridges to the OTel
// logs pipeline.
//
// Why both, always:
//
//   - stderr is captured by the docker-socket scrape and lands in
//     Loki under `{container="<app>"}`. Works in every deploy
//     pattern, survives OTel-disabled, survives a backend outage.
//   - The OTel bridge emits to OTLP which (in our shared-VPS setup)
//     lands in the same Loki under `{service_name="<app>",
//     exporter="OTLP"}` with all OTel attributes preserved as
//     queryable fields (request_id, span context, http.status, …).
//
// The two paths overlap on purpose — the OTLP path preserves
// structure the text path drops; the stderr path is the safety net
// when OTel is misconfigured or the collector is down. Dedup at
// query time is cheaper than signal loss at runtime.
//
// When cfg.LogsDisabled is true (OTEL_LOGS_EXPORTER=none or
// WithLogsDisabled()), the OTel handler is skipped entirely — only
// stderr remains. No retry-spam on an unsupported backend.
func newOTelLogger(cfg *Config, lp *sdklog.LoggerProvider) *slog.Logger {
	level := slog.LevelInfo
	if cfg.Environment == "dev" {
		level = slog.LevelDebug
	}

	stderr := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})

	if cfg.LogsDisabled {
		return slog.New(stderr)
	}

	otelHandler := otelslog.NewHandler(cfg.ServiceName,
		otelslog.WithLoggerProvider(lp),
	)
	// Wrap with a level filter — otelslog's handler doesn't apply
	// slog's level. We do that here.
	filtered := &leveledHandler{inner: otelHandler, level: level}

	return slog.New(multiHandler{filtered, stderr})
}

// leveledHandler wraps a slog.Handler and applies a level filter.
type leveledHandler struct {
	inner slog.Handler
	level slog.Level
}

func (h *leveledHandler) Enabled(ctx context.Context, l slog.Level) bool {
	// Both our level filter and the inner handler must consent.
	// Defensive against an inner handler that does its own gating
	// (e.g., context-bound level overrides).
	return l >= h.level && h.inner.Enabled(ctx, l)
}

func (h *leveledHandler) Handle(ctx context.Context, r slog.Record) error {
	return h.inner.Handle(ctx, r)
}

func (h *leveledHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &leveledHandler{inner: h.inner.WithAttrs(attrs), level: h.level}
}

func (h *leveledHandler) WithGroup(name string) slog.Handler {
	return &leveledHandler{inner: h.inner.WithGroup(name), level: h.level}
}

// multiHandler fans out a record to every wrapped handler.
type multiHandler []slog.Handler

func (m multiHandler) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range m {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (m multiHandler) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range m {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		if err := h.Handle(ctx, r.Clone()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(multiHandler, len(m))
	for i, h := range m {
		out[i] = h.WithAttrs(attrs)
	}
	return out
}

func (m multiHandler) WithGroup(name string) slog.Handler {
	out := make(multiHandler, len(m))
	for i, h := range m {
		out[i] = h.WithGroup(name)
	}
	return out
}
