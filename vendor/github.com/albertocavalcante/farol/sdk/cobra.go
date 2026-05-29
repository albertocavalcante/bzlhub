package farol

import (
	"context"
	"os"
	"time"

	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// Cobra adds --otel-* flags to the given root cobra.Command, wires
// MustSetup into PersistentPreRunE / shutdown into PersistentPostRunE,
// AND auto-traces every subcommand's RunE with one root span per
// invocation.
//
// Pass the root command of your CLI. Flags propagate to subcommands.
// Adds:
//   - --otel-endpoint     (OTLP endpoint; falls back to OTEL_EXPORTER_OTLP_ENDPOINT)
//   - --otel-service-name (defaults to OTEL_SERVICE_NAME / executable basename)
//   - --otel-environment  ("dev" | "staging" | "prod" | custom; falls back to FAROL_ENVIRONMENT)
//   - --otel-disabled     (boolean; sets OTEL_SDK_DISABLED)
//
// Auto-tracing parity with HTTPMiddleware: every subcommand that has
// a RunE gets wrapped so an "argv0 sub..." span covers its execution.
// The span captures the command path, returns code (via error), and
// any panic. cron-style binaries (gasto, dumpkit) get one span per
// invocation for free — without manual tracer.Start calls in app code.
//
// On root.Execute(), Setup runs before any subcommand RunE; on completion,
// shutdown runs (5s timeout). Subcommands keep their own PreRunE/PostRunE.
func Cobra(root *cobra.Command) {
	var (
		flagEndpoint    string
		flagServiceName string
		flagEnvironment string
		flagDisabled    bool
	)

	root.PersistentFlags().StringVar(&flagEndpoint, "otel-endpoint",
		os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
		"OTLP exporter endpoint (env: OTEL_EXPORTER_OTLP_ENDPOINT)")
	root.PersistentFlags().StringVar(&flagServiceName, "otel-service-name",
		"",
		"OTel service name override (env: OTEL_SERVICE_NAME; default: argv[0] basename)")
	root.PersistentFlags().StringVar(&flagEnvironment, "otel-environment",
		os.Getenv("FAROL_ENVIRONMENT"),
		"Deployment environment (env: FAROL_ENVIRONMENT)")
	root.PersistentFlags().BoolVar(&flagDisabled, "otel-disabled",
		false,
		"Disable OTel exporter (env: OTEL_SDK_DISABLED)")

	// Chain into PersistentPreRunE / PersistentPostRunE without clobbering
	// user-supplied hooks.
	prevPre := root.PersistentPreRunE
	prevPost := root.PersistentPostRunE

	var shutdown func(context.Context) error

	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		var opts []Option
		if flagEndpoint != "" {
			opts = append(opts, WithEndpoint(flagEndpoint))
		}
		if flagServiceName != "" {
			opts = append(opts, WithServiceName(flagServiceName))
		}
		if flagEnvironment != "" {
			opts = append(opts, WithEnvironment(flagEnvironment))
		}
		if flagDisabled {
			opts = append(opts, WithDisabled())
		}
		s, err := Setup(cmd.Context(), opts...)
		if err != nil {
			return err
		}
		shutdown = s
		if prevPre != nil {
			if err := prevPre(cmd, args); err != nil {
				// Setup succeeded but a downstream PreRunE failed. Cobra
				// will skip RunE and PostRunE, so PostRunE-based shutdown
				// would leak exporter goroutines. Tear down here.
				ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
				defer cancel()
				_ = shutdown(ctx)
				shutdown = nil
				return err
			}
		}
		return nil
	}

	root.PersistentPostRunE = func(cmd *cobra.Command, args []string) error {
		var prevErr error
		if prevPost != nil {
			prevErr = prevPost(cmd, args)
		}
		if shutdown != nil {
			// Bounded shutdown; if the user's ctx is already cancelled
			// (e.g. SIGINT), still give the exporter 5s to flush.
			ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
			if err := shutdown(ctx); err != nil && prevErr == nil {
				return err
			}
		}
		return prevErr
	}

	// Auto-trace every subcommand RunE. Walks the full tree so
	// nested subcommands (e.g. `app db migrate`) also get a root
	// span. Idempotent: only wraps once per command via a marker.
	traceCommandTree(root)
}

// commandTraceMarker is set on a cobra.Command's Annotations after
// its RunE has been wrapped. Lets traceCommandTree skip already-wrapped
// commands when Cobra() is called twice (e.g. via tests or
// dynamic subcommand registration after Cobra has already run).
const commandTraceMarker = "farol.traced"

// traceCommandTree walks the cobra subtree from c and wraps each
// command's RunE with a span. Idempotent.
func traceCommandTree(c *cobra.Command) {
	wrapCommandRun(c)
	for _, sub := range c.Commands() {
		traceCommandTree(sub)
	}
}

func wrapCommandRun(c *cobra.Command) {
	if c.RunE == nil && c.Run == nil {
		return
	}
	if c.Annotations[commandTraceMarker] == "1" {
		return
	}

	// Convert Run → RunE so we can return an error from the wrapper.
	// (cobra dispatches RunE in preference to Run if both are set.)
	if c.RunE == nil {
		origRun := c.Run
		c.RunE = func(cmd *cobra.Command, args []string) error {
			origRun(cmd, args)
			return nil
		}
	}
	orig := c.RunE
	cmdPath := c.CommandPath() // e.g. "gasto pull"

	c.RunE = func(cmd *cobra.Command, args []string) error {
		tracer := Tracer("farol/cobra")
		meter := Meter("farol/cobra")
		// Auto-RED histogram for cobra invocations — parity with
		// HTTPMiddleware's http.server.request.duration. Each
		// subcommand emits one observation per execution with
		// command name + ok|error status attributes. Cron-style
		// binaries (gasto, dumpkit) get cycle-duration metrics
		// without manually wiring an instrument.
		hist, _ := meter.Float64Histogram(
			"cobra.command.duration",
			metric.WithUnit("s"),
			metric.WithDescription("Duration of a cobra command's RunE."),
		)

		start := time.Now()
		ctx, span := tracer.Start(cmd.Context(), cmdPath,
			trace.WithSpanKind(trace.SpanKindInternal),
		)
		defer span.End()
		cmd.SetContext(ctx)

		err := orig(cmd, args)

		status := "ok"
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			status = "error"
		}
		if hist != nil {
			hist.Record(ctx, time.Since(start).Seconds(),
				metric.WithAttributes(
					attribute.String("command", cmdPath),
					attribute.String("status", status),
				),
			)
		}
		return err
	}

	if c.Annotations == nil {
		c.Annotations = map[string]string{}
	}
	c.Annotations[commandTraceMarker] = "1"
}
