module github.com/albertocavalcante/canopy

go 1.26.2

require (
	github.com/albertocavalcante/bazel-doc-go v0.0.0-00010101000000-000000000000
	github.com/albertocavalcante/bigorna v0.0.0-00010101000000-000000000000
	github.com/albertocavalcante/farol v0.0.0-20260521100058-45d811180afb
	github.com/albertocavalcante/go-bzlmod v0.0.0
	github.com/albertocavalcante/scip-bazel v0.2.0
	github.com/albertocavalcante/stardoc-go v0.0.0-00010101000000-000000000000
	github.com/albertocavalcante/starlark-doc-go v0.0.0
	github.com/albertocavalcante/starlark-go-bazel v0.0.0-00010101000000-000000000000
	github.com/albertocavalcante/understory v0.3.2
	github.com/go-chi/chi/v5 v5.2.1
	github.com/mark3labs/mcp-go v0.53.0
	github.com/scip-code/scip/bindings/go/scip v0.7.1
	github.com/spf13/cobra v1.10.2
	go.starlark.net v0.0.0-20260326113308-fadfc96def35
	golang.org/x/image v0.41.0
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/albertocavalcante/bazel-highlight-go v0.1.1 // indirect
	github.com/albertocavalcante/scip-starlark v0.2.0 // indirect
	github.com/albertocavalcante/starlark-highlight-go v0.2.0 // indirect
	github.com/albertocavalcante/starlark-syntax-go v0.20260208.0 // indirect
	github.com/bazelbuild/buildtools v0.0.0-20260527145659-eb0c58a06830 // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/jsonschema-go v0.4.2 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.28.0 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	github.com/sourcegraph/beaut v0.0.0-20240611013027-627e4c25335a // indirect
	github.com/spf13/cast v1.7.1 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/bridges/otelslog v0.18.0 // indirect
	go.opentelemetry.io/otel v1.43.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc v0.19.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc v1.43.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.43.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.43.0 // indirect
	go.opentelemetry.io/otel/log v0.19.0 // indirect
	go.opentelemetry.io/otel/metric v1.43.0 // indirect
	go.opentelemetry.io/otel/sdk v1.43.0 // indirect
	go.opentelemetry.io/otel/sdk/log v0.19.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.43.0 // indirect
	go.opentelemetry.io/otel/trace v1.43.0 // indirect
	go.opentelemetry.io/proto/otlp v1.10.0 // indirect
	golang.org/x/net v0.52.0 // indirect
	golang.org/x/text v0.37.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260401024825-9d38bb4040a9 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260401024825-9d38bb4040a9 // indirect
	google.golang.org/grpc v1.80.0 // indirect
)

require (
	github.com/albertocavalcante/assay v0.0.0
	github.com/albertocavalcante/bazel-module-summary-go v0.0.0
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	golang.org/x/sys v0.42.0 // indirect
	modernc.org/libc v1.72.3 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
	modernc.org/sqlite v1.50.1
)

replace github.com/albertocavalcante/assay => ../assay

replace github.com/albertocavalcante/go-bzlmod => ../go-bzlmod

replace github.com/albertocavalcante/understory => ../understory

// understory now imports starlark-highlight-go for /api/highlight.
// Pin to local during dev — drop once highlight is tagged.
replace github.com/albertocavalcante/starlark-highlight-go => ../starlark-highlight-go

replace github.com/albertocavalcante/starlark-syntax-go => ../starlark-syntax-go

replace github.com/albertocavalcante/starlark-go-bazel => ../starlark-go-bazel

replace github.com/albertocavalcante/bazel-module-summary-go => ../bazel-module-summary-go

replace github.com/albertocavalcante/starlark-doc-go => ../starlark-doc-go

replace github.com/albertocavalcante/stardoc-go => ../stardoc-go

replace github.com/albertocavalcante/bazel-doc-go => ../bazel-doc-go

replace github.com/albertocavalcante/farol => ../farol

replace github.com/albertocavalcante/bigorna => ../bigorna
