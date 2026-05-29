package farol

import (
	"time"
)

// Option configures Setup. See WithServiceName, WithEndpoint, etc.
type Option func(*Config)

// Protocol selects the OTLP transport.
type Protocol int

const (
	// ProtocolGRPC sends OTLP over gRPC (default).
	ProtocolGRPC Protocol = iota
	// ProtocolHTTPProtobuf sends OTLP over HTTP with protobuf payloads.
	ProtocolHTTPProtobuf
)

// Config is the resolved SDK configuration. Built by Option closures.
// Exported so it can be inspected in tests; do not construct directly —
// use options.
type Config struct {
	ServiceName    string
	ServiceVersion string
	Environment    string

	Endpoint string
	Protocol Protocol
	Headers  map[string]string

	ResourceAttributes map[string]string

	SamplerKind string // "always_on" | "always_off" | "traceidratio" | "parentbased"
	SamplerArg  float64

	StdoutFallback bool
	Disabled       bool

	// Per-signal disable flags, populated from
	// OTEL_TRACES_EXPORTER=none / OTEL_METRICS_EXPORTER=none /
	// OTEL_LOGS_EXPORTER=none — the canonical OTel env-var convention
	// for "this signal isn't supported here, don't bother sending."
	//
	// LogsDisabled is the most commonly set in practice: many
	// collectors accept traces and metrics over OTLP but not logs
	// (Grafana Alloy, until the otelcol→loki bridge is wired). The
	// short-circuit avoids retry-then-noise spam in those setups.
	TracesDisabled  bool
	MetricsDisabled bool
	LogsDisabled    bool

	BatchTimeout time.Duration
}

// WithServiceName sets the logical service name. Overrides
// OTEL_SERVICE_NAME and the basename of os.Args[0].
func WithServiceName(name string) Option {
	return func(c *Config) { c.ServiceName = name }
}

// WithServiceVersion overrides the version detected from build info.
func WithServiceVersion(v string) Option {
	return func(c *Config) { c.ServiceVersion = v }
}

// WithEnvironment sets the deployment environment.
// Typical values: "dev", "staging", "prod".
func WithEnvironment(env string) Option {
	return func(c *Config) { c.Environment = env }
}

// WithEndpoint sets the OTLP exporter endpoint URL.
// Overrides OTEL_EXPORTER_OTLP_ENDPOINT.
func WithEndpoint(url string) Option {
	return func(c *Config) { c.Endpoint = url }
}

// WithProtocol selects the OTLP transport.
func WithProtocol(p Protocol) Option {
	return func(c *Config) { c.Protocol = p }
}

// WithHeaders sets additional HTTP headers for the exporter.
// Useful for bearer-token-protected endpoints (e.g. CF Access).
func WithHeaders(h map[string]string) Option {
	return func(c *Config) {
		if c.Headers == nil {
			c.Headers = map[string]string{}
		}
		for k, v := range h {
			c.Headers[k] = v
		}
	}
}

// WithResourceAttributes adds attributes to the OTel Resource.
// These appear on every span, metric, and log.
func WithResourceAttributes(kv map[string]string) Option {
	return func(c *Config) {
		if c.ResourceAttributes == nil {
			c.ResourceAttributes = map[string]string{}
		}
		for k, v := range kv {
			c.ResourceAttributes[k] = v
		}
	}
}

// WithSampler overrides the default sampler.
//
// kind must be one of: "always_on", "always_off", "traceidratio",
// "parentbased". For "traceidratio", arg is the ratio in [0,1].
func WithSampler(kind string, arg float64) Option {
	return func(c *Config) {
		c.SamplerKind = kind
		c.SamplerArg = arg
	}
}

// WithStdoutFallback is a no-op since logs always also write to
// stderr — kept for backward compatibility with v0.0.x callers.
//
// Deprecated: stderr emission is the unconditional default as of
// the slog multihandler change. Use WithLogsDisabled to disable
// the OTel bridge instead.
func WithStdoutFallback() Option {
	return func(c *Config) { c.StdoutFallback = true }
}

// WithDisabled installs noop providers for ALL signals. Equivalent
// to OTEL_SDK_DISABLED=true.
func WithDisabled() Option {
	return func(c *Config) { c.Disabled = true }
}

// WithTracesDisabled installs a noop tracer provider while leaving
// metrics + logs active. Equivalent to OTEL_TRACES_EXPORTER=none.
func WithTracesDisabled() Option {
	return func(c *Config) { c.TracesDisabled = true }
}

// WithMetricsDisabled installs a noop meter provider while leaving
// traces + logs active. Equivalent to OTEL_METRICS_EXPORTER=none.
func WithMetricsDisabled() Option {
	return func(c *Config) { c.MetricsDisabled = true }
}

// WithLogsDisabled installs a noop logger provider while leaving
// traces + metrics active. Equivalent to OTEL_LOGS_EXPORTER=none.
// Use this when the collector ahead of you doesn't accept OTLP logs
// — apps that depend on stdout-via-Loki still work, but the SDK
// stops retry-spamming on the unsupported endpoint.
func WithLogsDisabled() Option {
	return func(c *Config) { c.LogsDisabled = true }
}

// WithBatchTimeout sets the OTLP exporter batch timeout. Default: 5s.
func WithBatchTimeout(d time.Duration) Option {
	return func(c *Config) { c.BatchTimeout = d }
}
