package farol

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.27.0"
)

// shutdownTimeout caps how long farol.Cobra waits for the OTel
// shutdown to flush on post-run.
const shutdownTimeout = 5 * time.Second

// state holds the live providers so accessors can find them.
// Guarded by stateMu so concurrent Setup/Shutdown calls are safe.
var (
	stateMu sync.RWMutex
	state   *setupState
)

// setupState holds the live OTel providers + the matching shutdown
// closure. A non-nil *state means Setup has run and not been undone.
type setupState struct {
	cfg      *Config
	tp       *sdktrace.TracerProvider
	mp       *sdkmetric.MeterProvider
	lp       *sdklog.LoggerProvider
	logger   *slog.Logger
	shutdown func(context.Context) error
}

// Setup initializes OpenTelemetry with farol's defaults.
//
// It returns a shutdown function that flushes pending signals. The
// shutdown must be called before process exit.
//
// If OTEL_EXPORTER_OTLP_ENDPOINT is empty (and no WithEndpoint option
// was supplied), Setup installs noop providers and returns nil error —
// safe to call from unit tests without an OTel collector running.
//
// Calling Setup twice without an intervening shutdown is an error.
func Setup(ctx context.Context, opts ...Option) (shutdown func(context.Context) error, err error) {
	cfg := resolveConfig(opts)

	stateMu.Lock()
	defer stateMu.Unlock()

	if state != nil {
		return nil, errors.New("farol: Setup called twice without shutdown")
	}

	// Disabled / no-endpoint path: install noop providers so accessors
	// return non-nil objects but no I/O happens. Skip the global
	// propagator install too — a noop Setup should not mutate global
	// OTel state.
	if cfg.Disabled || cfg.Endpoint == "" {
		state = &setupState{
			cfg:    cfg,
			tp:     sdktrace.NewTracerProvider(),
			mp:     sdkmetric.NewMeterProvider(),
			lp:     sdklog.NewLoggerProvider(),
			logger: newFallbackLogger(),
		}
		state.shutdown = func(context.Context) error {
			stateMu.Lock()
			defer stateMu.Unlock()
			state = nil
			return nil
		}
		otel.SetTracerProvider(state.tp)
		otel.SetMeterProvider(state.mp)
		return state.shutdown, nil
	}

	// Live path: install the W3C composite propagator so traceparent
	// + baggage round-trip on incoming/outgoing requests.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// Only gRPC is wired in v0.1. Fail explicitly if http/protobuf was
	// requested so the caller knows their config isn't being silently
	// ignored. HTTP exporter support is a follow-up.
	if cfg.Protocol != ProtocolGRPC {
		return nil, errors.New("farol: only OTLP/gRPC is supported in v0.1; set OTEL_EXPORTER_OTLP_PROTOCOL=grpc")
	}

	res, err := buildResource(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("farol: build resource: %w", err)
	}

	// Each signal is wired conditionally. The disable flags are set
	// from OTEL_{TRACES,METRICS,LOGS}_EXPORTER=none (or the matching
	// With*Disabled options). A "disabled" signal installs a no-op
	// provider — accessors return non-nil objects so callsites
	// don't need nil checks, but no exporter is created, no batch
	// processor runs, no I/O happens, no retry-spam if the backend
	// would have rejected it.

	var (
		tp *sdktrace.TracerProvider
		mp *sdkmetric.MeterProvider
		lp *sdklog.LoggerProvider
	)

	if cfg.TracesDisabled {
		tp = sdktrace.NewTracerProvider()
	} else {
		traceOpts := []otlptracegrpc.Option{otlptracegrpc.WithEndpointURL(cfg.Endpoint)}
		if len(cfg.Headers) > 0 {
			traceOpts = append(traceOpts, otlptracegrpc.WithHeaders(cfg.Headers))
		}
		exp, err := otlptracegrpc.New(ctx, traceOpts...)
		if err != nil {
			return nil, fmt.Errorf("farol: otlp trace exporter: %w", err)
		}
		tp = sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(exp,
				sdktrace.WithBatchTimeout(cfg.BatchTimeout),
			),
			sdktrace.WithSampler(samplerFromConfig(cfg)),
			sdktrace.WithResource(res),
		)
	}

	if cfg.MetricsDisabled {
		mp = sdkmetric.NewMeterProvider()
	} else {
		metricOpts := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpointURL(cfg.Endpoint)}
		if len(cfg.Headers) > 0 {
			metricOpts = append(metricOpts, otlpmetricgrpc.WithHeaders(cfg.Headers))
		}
		exp, err := otlpmetricgrpc.New(ctx, metricOpts...)
		if err != nil {
			_ = tp.Shutdown(ctx)
			return nil, fmt.Errorf("farol: otlp metric exporter: %w", err)
		}
		mp = sdkmetric.NewMeterProvider(
			sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp,
				sdkmetric.WithInterval(15*time.Second),
			)),
			sdkmetric.WithResource(res),
		)
	}

	if cfg.LogsDisabled {
		lp = sdklog.NewLoggerProvider()
	} else {
		logOpts := []otlploggrpc.Option{otlploggrpc.WithEndpointURL(cfg.Endpoint)}
		if len(cfg.Headers) > 0 {
			logOpts = append(logOpts, otlploggrpc.WithHeaders(cfg.Headers))
		}
		exp, err := otlploggrpc.New(ctx, logOpts...)
		if err != nil {
			_ = tp.Shutdown(ctx)
			_ = mp.Shutdown(ctx)
			return nil, fmt.Errorf("farol: otlp log exporter: %w", err)
		}
		lp = sdklog.NewLoggerProvider(
			sdklog.WithProcessor(sdklog.NewBatchProcessor(exp,
				sdklog.WithExportTimeout(cfg.BatchTimeout),
			)),
			sdklog.WithResource(res),
		)
	}

	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	// Note: no global LoggerProvider in OTel today; we store it in state
	// and expose via farol.Logger().

	state = &setupState{
		cfg:    cfg,
		tp:     tp,
		mp:     mp,
		lp:     lp,
		logger: newOTelLogger(cfg, lp),
	}
	state.shutdown = func(ctx context.Context) error {
		stateMu.Lock()
		defer stateMu.Unlock()
		if state == nil {
			return nil
		}
		var errs []error
		if err := tp.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("trace: %w", err))
		}
		if err := mp.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("metric: %w", err))
		}
		if err := lp.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("log: %w", err))
		}
		state = nil
		return errors.Join(errs...)
	}
	return state.shutdown, nil
}

// MustSetup is the cobra-friendly version of Setup. It logs to stderr
// and calls os.Exit(1) on failure. Intended for use in main() of CLIs.
func MustSetup(ctx context.Context, opts ...Option) func(context.Context) error {
	shutdown, err := Setup(ctx, opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "farol.MustSetup: %v\n", err)
		os.Exit(1)
	}
	return shutdown
}

func buildResource(ctx context.Context, cfg *Config) (*sdkresource.Resource, error) {
	attrs := []attribute.KeyValue{
		semconv.ServiceName(cfg.ServiceName),
		semconv.DeploymentEnvironmentName(cfg.Environment),
	}
	if cfg.ServiceVersion != "" {
		attrs = append(attrs, semconv.ServiceVersion(cfg.ServiceVersion))
	}

	// Merge host + k8s detection, plus any user-supplied.
	combined := map[string]string{}
	mergeAttrs(combined, detectHostAttrs())
	mergeAttrs(combined, detectK8sAttrs())
	mergeAttrs(combined, cfg.ResourceAttributes)
	for k, v := range combined {
		attrs = append(attrs, attribute.String(k, v))
	}

	return sdkresource.New(ctx,
		sdkresource.WithAttributes(attrs...),
		sdkresource.WithProcess(),
		sdkresource.WithFromEnv(),
		sdkresource.WithHost(),
	)
}

func samplerFromConfig(cfg *Config) sdktrace.Sampler {
	switch cfg.SamplerKind {
	case "always_on":
		return sdktrace.AlwaysSample()
	case "always_off":
		return sdktrace.NeverSample()
	case "traceidratio":
		return sdktrace.TraceIDRatioBased(cfg.SamplerArg)
	case "parentbased_always_on":
		return sdktrace.ParentBased(sdktrace.AlwaysSample())
	case "parentbased_always_off":
		return sdktrace.ParentBased(sdktrace.NeverSample())
	case "parentbased_traceidratio":
		return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SamplerArg))
	default:
		return sdktrace.ParentBased(sdktrace.AlwaysSample())
	}
}
