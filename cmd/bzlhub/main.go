// Command canopy is the Bazel-first self-hosted registry.
package main

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/albertocavalcante/bzlhub/cmd/bzlhub/diff"
	"github.com/albertocavalcante/bzlhub/cmd/bzlhub/publish"
	"github.com/albertocavalcante/bzlhub/cmd/bzlhub/watch"
	"github.com/albertocavalcante/bzlhub/internal/fetch"
	"github.com/albertocavalcante/bzlhub/internal/version"
	farol "github.com/albertocavalcante/farol/sdk"
)

const defaultDBPath = "bzlhub.db"

// defaultSourcesCacheDir returns where bzlhub unpacks per-(module,
// version) source tarballs for the code-nav handler. Resolution order:
//  1. $BZLHUB_SOURCES_CACHE_DIR (explicit override — what ops use to
//     pin a volume mount in containers).
//  2. os.UserCacheDir()/bzlhub/sources (platform-correct on macOS,
//     Linux, Windows — works out of the box on a workstation).
//  3. /var/lib/bzlhub/sources as a last-resort fallback when neither
//     of the above is available.
//
// The old compile-time /var/lib path broke on workstations where it
// is root-only or absent. Operators with a fixed /var/lib layout set
// BZLHUB_SOURCES_CACHE_DIR explicitly.
func defaultSourcesCacheDir() string {
	if v := os.Getenv("BZLHUB_SOURCES_CACHE_DIR"); v != "" {
		return v
	}
	if cache, err := os.UserCacheDir(); err == nil {
		return filepath.Join(cache, "bzlhub", "sources")
	}
	return "/var/lib/bzlhub/sources"
}

func main() {
	// Process-wide default for registry/source fetches. The fetch.Client
	// snapshots this on construction, so wire it before any subcommand can
	// create a client.
	fetch.SetDefaultAllowedHosts(fetch.ParseAllowedHosts(os.Getenv("BZLHUB_ALLOWED_HOSTS")))

	root := &cobra.Command{
		Use:           "bzlhub",
		Short:         "Bazel-first self-hosted registry",
		SilenceUsage:  true, // errors print "Error: ..." only — no flag wall-of-text
		SilenceErrors: false,
		Version:       version.String(),
	}
	// Plain `bzlhub --version` line, no "bzlhub version" prefix from cobra.
	root.SetVersionTemplate("bzlhub {{.Version}}\n")
	// Wire farol's OTel SDK: PersistentPreRunE installs OTel providers
	// before any subcommand runs; PersistentPostRunE flushes with a 5s
	// budget on exit. Enabled only when OTEL_EXPORTER_OTLP_ENDPOINT (or
	// --otel-endpoint) is set; otherwise noop, zero cost.
	farol.Cobra(root)
	root.AddCommand(newServeCmd())
	root.AddCommand(newIngestCmd())
	root.AddCommand(newSearchCmd())
	root.AddCommand(newShowCmd())
	root.AddCommand(newMCPCmd())
	root.AddCommand(newDriftCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newSyncCmd())
	root.AddCommand(newSeedCmd())
	root.AddCommand(diff.NewCmd())
	root.AddCommand(diff.NewClosureCmd())
	root.AddCommand(newVerifyCmd())
	root.AddCommand(newExportDocsCmd())
	root.AddCommand(newRefreshMetadataCmd())
	root.AddCommand(publish.NewCmd())
	root.AddCommand(watch.NewCmd())
	// Signal-cancelled root context so long-running subcommands
	// (`bzlhub serve`, `bzlhub sync run --interval`) shut down
	// cleanly on Ctrl-C / SIGTERM. SIGTERM matches what systemd
	// sends on `systemctl stop`. Without this, the daemon's
	// defer-release on the bcrmirror lock — and Bus / store
	// Close — wouldn't fire.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := root.ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}
