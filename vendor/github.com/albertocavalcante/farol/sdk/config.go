package farol

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// resolveConfig builds the effective Config from defaults, environment
// variables, and the supplied options (in that order — options win).
//
// Env vars honored:
//   - OTEL_EXPORTER_OTLP_ENDPOINT
//   - OTEL_EXPORTER_OTLP_PROTOCOL   ("grpc" | "http/protobuf")
//   - OTEL_EXPORTER_OTLP_HEADERS    ("k1=v1,k2=v2")
//   - OTEL_SERVICE_NAME
//   - OTEL_RESOURCE_ATTRIBUTES      ("k1=v1,k2=v2")
//   - OTEL_SDK_DISABLED             ("true" disables all signals)
//   - OTEL_TRACES_EXPORTER          ("none" disables trace export; otherwise OTLP)
//   - OTEL_METRICS_EXPORTER         ("none" disables metric export; otherwise OTLP)
//   - OTEL_LOGS_EXPORTER            ("none" disables log export; otherwise OTLP)
//   - OTEL_TRACES_SAMPLER           ("always_on" | "always_off" | "traceidratio" | "parentbased")
//   - OTEL_TRACES_SAMPLER_ARG       float, used by traceidratio
//   - FAROL_ENVIRONMENT             ("dev" | "staging" | "prod" | custom)
//   - FAROL_SERVICE_VERSION         overrides build-info version
//   - FAROL_STDOUT_FALLBACK         ("true" enables)
//   - KUBERNETES_NAMESPACE          used to infer environment if FAROL_ENVIRONMENT unset
func resolveConfig(opts []Option) *Config {
	c := &Config{
		ServiceName:        defaultServiceName(),
		ServiceVersion:     defaultServiceVersion(),
		Environment:        defaultEnvironment(),
		Endpoint:           os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
		Protocol:           parseProtocol(os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL")),
		Headers:            parseKV(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS")),
		ResourceAttributes: parseKV(os.Getenv("OTEL_RESOURCE_ATTRIBUTES")),
		SamplerKind:        envOr("OTEL_TRACES_SAMPLER", ""),
		SamplerArg:         parseFloat(os.Getenv("OTEL_TRACES_SAMPLER_ARG"), 0),
		StdoutFallback:     parseBool(os.Getenv("FAROL_STDOUT_FALLBACK"), false),
		Disabled:           parseBool(os.Getenv("OTEL_SDK_DISABLED"), false),
		TracesDisabled:     strings.EqualFold(os.Getenv("OTEL_TRACES_EXPORTER"), "none"),
		MetricsDisabled:    strings.EqualFold(os.Getenv("OTEL_METRICS_EXPORTER"), "none"),
		LogsDisabled:       strings.EqualFold(os.Getenv("OTEL_LOGS_EXPORTER"), "none"),
		BatchTimeout:       5 * time.Second,
	}

	for _, opt := range opts {
		opt(c)
	}

	// If sampler still unset, pick a sensible default based on environment.
	if c.SamplerKind == "" {
		if c.Environment == "prod" {
			c.SamplerKind = "parentbased_traceidratio"
			if c.SamplerArg == 0 {
				c.SamplerArg = 0.1
			}
		} else {
			c.SamplerKind = "parentbased_always_on"
		}
	}

	return c
}

func defaultServiceName() string {
	if v := os.Getenv("OTEL_SERVICE_NAME"); v != "" {
		return v
	}
	if len(os.Args) > 0 {
		return filepath.Base(os.Args[0])
	}
	return "unknown_service"
}

func defaultServiceVersion() string {
	if v := os.Getenv("FAROL_SERVICE_VERSION"); v != "" {
		return v
	}
	return readBuildVersion()
}

func defaultEnvironment() string {
	if v := os.Getenv("FAROL_ENVIRONMENT"); v != "" {
		return v
	}
	if ns := os.Getenv("KUBERNETES_NAMESPACE"); ns != "" {
		return ns
	}
	return "dev"
}

func parseProtocol(s string) Protocol {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "http/protobuf", "http":
		return ProtocolHTTPProtobuf
	default:
		return ProtocolGRPC
	}
}

func parseKV(s string) map[string]string {
	if s == "" {
		return nil
	}
	out := make(map[string]string)
	for _, pair := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok || k == "" {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func parseBool(s string, fallback bool) bool {
	if s == "" {
		return fallback
	}
	v, err := strconv.ParseBool(s)
	if err != nil {
		return fallback
	}
	return v
}

func parseFloat(s string, fallback float64) float64 {
	if s == "" {
		return fallback
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fallback
	}
	return v
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
