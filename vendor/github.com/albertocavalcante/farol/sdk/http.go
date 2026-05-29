package farol

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.27.0"
	"go.opentelemetry.io/otel/trace"
)

// HTTPMiddleware wraps an http.Handler with farol's standard middleware:
//   - X-Request-Id generation/propagation
//   - W3C traceparent + Baggage propagation (one span per request)
//   - http.server.request.duration histogram (RED metrics)
//   - structured access log via slog
//   - panic recovery (records exception; if the response hasn't started
//     yet, writes 500 — otherwise leaves the wire status alone)
//
// Safe to call before Setup. Tracer and meter are resolved per request
// so a later Setup activates the real providers without re-wrapping.
//
// Limitation: span name uses raw URL path, so /orders/abc-123 produces
// per-instance span names. For low cardinality, wrap at the router
// layer instead (chi/gorilla integration is a follow-up).
func HTTPMiddleware(next http.Handler) http.Handler {
	// Histogram instrument resolution: keep it lazy + cached after first
	// successful creation, so the post-Setup meter (not the wrap-time
	// noop) is the one we record against.
	var (
		histOnce sync.Once
		hist     metric.Float64Histogram
	)

	resolveHist := func() metric.Float64Histogram {
		histOnce.Do(func() {
			h, err := Meter("farol/http").Float64Histogram(
				"http.server.request.duration",
				metric.WithUnit("s"),
				metric.WithDescription("Duration of HTTP server requests."),
			)
			if err == nil {
				hist = h
			}
		})
		return hist
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Extract upstream context using the globally-configured propagator
		// so Baggage (and any other registered propagators) round-trips,
		// not just TraceContext.
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))

		reqID := r.Header.Get("X-Request-Id")
		if reqID == "" {
			reqID = newRequestID()
		}

		// Resolve tracer per request so Setup ordering doesn't matter.
		ctx, span := Tracer("farol/http").Start(ctx, r.Method+" "+r.URL.Path,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				semconv.HTTPRequestMethodKey.String(r.Method),
				semconv.URLPath(r.URL.Path),
				semconv.URLScheme(schemeOf(r)),
				semconv.UserAgentOriginal(r.UserAgent()),
				attribute.String("farol.request_id", reqID),
			),
		)
		defer span.End()

		ww := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		ww.Header().Set("X-Request-Id", reqID)

		defer func() {
			rec := recover()
			if rec != nil {
				// Truncate the stack to avoid blowing out span size.
				stack := string(debug.Stack())
				if len(stack) > 4096 {
					stack = stack[:4096] + "...(truncated)"
				}
				span.SetStatus(codes.Error, "panic")
				span.RecordError(fmt.Errorf("%v", rec),
					trace.WithAttributes(attribute.String("stack", stack)),
				)
				Logger().Error("http: panic recovered",
					slog.String("request_id", reqID),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.Any("panic", rec),
				)
				// Only write 500 if the response hasn't started yet.
				// Otherwise we'd lie about both the wire status and the
				// recorded telemetry.
				if !ww.wroteHeader {
					http.Error(ww, "internal server error", http.StatusInternalServerError)
				}
			}

			dur := time.Since(start).Seconds()
			attrs := []attribute.KeyValue{
				semconv.HTTPRequestMethodKey.String(r.Method),
				semconv.URLPath(r.URL.Path),
				semconv.HTTPResponseStatusCode(ww.status),
			}
			span.SetAttributes(semconv.HTTPResponseStatusCode(ww.status))
			if ww.status >= 500 {
				span.SetStatus(codes.Error, strconv.Itoa(ww.status))
			}
			if h := resolveHist(); h != nil {
				h.Record(ctx, dur, metric.WithAttributes(attrs...))
			}

			Logger().Info("http",
				slog.String("request_id", reqID),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", ww.status),
				slog.Float64("duration_s", dur),
			)
		}()

		next.ServeHTTP(ww, r.WithContext(ctx))
	})
}

// responseWriter wraps http.ResponseWriter to capture status and to
// forward Flusher/Hijacker calls so SSE, websockets, and streaming
// handlers keep working through the middleware.
type responseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *responseWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.status = code
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *responseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

// Flush forwards to the underlying writer if it supports it.
// Required for SSE; required for any handler that streams.
func (w *responseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack forwards to the underlying writer if it supports it.
// Required for websocket upgrades.
func (w *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("farol: underlying ResponseWriter does not support hijacking")
}

func schemeOf(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	if s := r.Header.Get("X-Forwarded-Proto"); s != "" {
		return s
	}
	return "http"
}

func newRequestID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
