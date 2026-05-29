package understory

// Build metadata. These variables are populated at link time via
//
//	go build -ldflags "-X github.com/albertocavalcante/understory/pkg/understory.Version=v0.4.0 \
//	                   -X github.com/albertocavalcante/understory/pkg/understory.Commit=abc1234 \
//	                   -X github.com/albertocavalcante/understory/pkg/understory.BuiltAt=2026-05-15T12:00:00Z"
//
// The defaults below are deliberate sentinels for binaries built without
// the flag (e.g. `go install`, `go test`). The /api/version endpoint and
// the CLI's --version flag both read from these.
var (
	Version = "dev"
	Commit  = "unknown"
	BuiltAt = "unknown"
)
