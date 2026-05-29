// Package farol is the observability SDK shared across the fleet of
// Go projects under ~/dev/ws/. It wraps OpenTelemetry with sane
// defaults so any consumer can wire traces, metrics, and structured
// logs in two lines of code.
//
//	shutdown := farol.MustSetup(ctx, farol.WithServiceName("dumpkit"))
//	defer shutdown(context.Background())
//
// See plans/02-sdk-design.md in the farol repo for the full design
// rationale.
package farol
