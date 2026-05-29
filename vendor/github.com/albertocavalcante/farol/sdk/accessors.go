package farol

import (
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// Logger returns a slog.Logger that emits via the OTel logs bridge.
// Safe to call before Setup — returns a stderr-only logger until Setup
// has run.
func Logger() *slog.Logger {
	stateMu.RLock()
	defer stateMu.RUnlock()
	if state == nil || state.logger == nil {
		return newFallbackLogger()
	}
	return state.logger
}

// Tracer returns a named OTel tracer. Equivalent to otel.Tracer(name)
// but routes through farol's TracerProvider (which is the global one
// after Setup).
func Tracer(name string) trace.Tracer {
	return otel.Tracer(name)
}

// Meter returns a named OTel meter.
func Meter(name string) metric.Meter {
	return otel.Meter(name)
}
