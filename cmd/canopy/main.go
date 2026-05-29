// Command canopy is the Bazel-first self-hosted registry.
package main

import (
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/albertocavalcante/canopy/cmd/canopy/diff"
	"github.com/albertocavalcante/canopy/cmd/canopy/publish"
	"github.com/albertocavalcante/canopy/cmd/canopy/watch"
	"github.com/albertocavalcante/canopy/internal/fetch"
	"github.com/albertocavalcante/canopy/internal/version"
	farol "github.com/albertocavalcante/farol/sdk"
)

const defaultDBPath = "canopy.db"

// defaultSourcesCacheDir returns where canopy unpacks per-(module,
// version) source tarballs for the code-nav handler. Resolution order:
//  1. $CANOPY_SOURCES_CACHE_DIR (explicit override — what ops use to
//     pin a volume mount in containers).
//  2. os.UserCacheDir()/canopy/sources (platform-correct on macOS,
//     Linux, Windows — works out of the box on a workstation).
//  3. /var/lib/canopy/sources as a last-resort fallback when neither
//     of the above is available.
//
// The old compile-time /var/lib/canopy default broke on workstations
// where that path is root-only or absent. Operators with a fixed
// /var/lib/canopy layout set CANOPY_SOURCES_CACHE_DIR explicitly.
func defaultSourcesCacheDir() string {
	if v := os.Getenv("CANOPY_SOURCES_CACHE_DIR"); v != "" {
		return v
	}
	if cache, err := os.UserCacheDir(); err == nil {
		return filepath.Join(cache, "canopy", "sources")
	}
	return "/var/lib/canopy/sources"
}

func main() {
	// Process-wide default for registry/source fetches. The fetch.Client
	// snapshots this on construction, so wire it before any subcommand can
	// create a client.
	fetch.SetDefaultAllowedHosts(fetch.ParseAllowedHosts(os.Getenv("CANOPY_ALLOWED_HOSTS")))

	root := &cobra.Command{
		Use:           "canopy",
		Short:         "Bazel-first self-hosted registry",
		SilenceUsage:  true, // errors print "Error: ..." only — no flag wall-of-text
		SilenceErrors: false,
		Version:       version.String(),
	}
	// Plain `canopy --version` line, no "canopy version" prefix from cobra.
	root.SetVersionTemplate("canopy {{.Version}}\n")
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
	root.AddCommand(diff.NewCmd())
	root.AddCommand(diff.NewClosureCmd())
	root.AddCommand(newVerifyCmd())
	root.AddCommand(newExportDocsCmd())
	root.AddCommand(newRefreshMetadataCmd())
	root.AddCommand(publish.NewCmd())
	root.AddCommand(watch.NewCmd())
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
