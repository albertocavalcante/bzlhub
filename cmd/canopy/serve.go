package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/albertocavalcante/canopy/internal/api"
	"github.com/albertocavalcante/canopy/internal/backend"
	"github.com/albertocavalcante/canopy/internal/canopy"
	"github.com/albertocavalcante/canopy/internal/eventbus"
	"github.com/albertocavalcante/canopy/internal/featureflags"
	"github.com/albertocavalcante/canopy/internal/server"
	"github.com/albertocavalcante/canopy/internal/store"
	farol "github.com/albertocavalcante/farol/sdk"
)

func newServeCmd() *cobra.Command {
	var (
		rootDir       string
		dbPath        string
		addr          string
		mirrorBaseURL string
		upstreamURLs  []string
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve BCR HTTP and /api/* endpoints",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if rootDir == "" && dbPath == "" {
				return errors.New("at least one of --root (BCR substrate) or --db (canopy index) is required")
			}
			log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

			var bk backend.Backend
			if rootDir != "" {
				if _, err := os.Stat(rootDir); err != nil {
					return fmt.Errorf("registry root %q: %w", rootDir, err)
				}
				bk = backend.NewFile(rootDir)
			}

			// Federation (Plan 16). Flag overrides env; env is
			// comma-separated. Empty in both → no federation, bk stays
			// as-is.
			upstreams := upstreamURLs
			if len(upstreams) == 0 {
				if env := os.Getenv("CANOPY_UPSTREAMS"); env != "" {
					for _, u := range strings.Split(env, ",") {
						u = strings.TrimSpace(u)
						if u != "" {
							upstreams = append(upstreams, u)
						}
					}
				}
			}
			if len(upstreams) > 0 {
				if bk == nil {
					return errors.New("--upstream requires --root (federation cascades through a local primary)")
				}
				ups := make([]*backend.Upstream, 0, len(upstreams))
				for _, u := range upstreams {
					ups = append(ups, &backend.Upstream{URL: u})
				}
				// Plan 16 Layer C: response cache size. Empty env →
				// default (1000 entries per Plan 16). "0" or negative
				// → cache disabled (useful for ops who want to bypass
				// caching in early debugging).
				cacheCap := 0
				if v := os.Getenv("CANOPY_UPSTREAM_CACHE_SIZE"); v != "" {
					if n, parseErr := strconv.Atoi(v); parseErr == nil {
						cacheCap = n
					}
				}
				// Plan 16 Layer D opt-out: shadow detection on by
				// default (the Plan 16 collision-audit promise).
				// Operators serving expensive / rate-limited upstreams
				// can flip this off — siblings get canceled on
				// winner-detect, saving ~len(upstreams)-1 GETs per
				// cascade resolve at the cost of the collision audit
				// never seeing shadowed-200 rows.
				disableShadow := false
				if v := os.Getenv("CANOPY_DISABLE_SHADOW_DETECTION"); v != "" {
					if b, parseErr := strconv.ParseBool(v); parseErr == nil {
						disableShadow = b
					} else {
						log.Warn("ignoring invalid CANOPY_DISABLE_SHADOW_DETECTION",
							"value", v, "err", parseErr)
					}
				}
				cascade, err := backend.NewCascade(backend.CascadeConfig{
					Primary:                bk,
					Upstreams:              ups,
					Logger:                 log,
					CacheCapacity:          cacheCap,
					DisableShadowDetection: disableShadow,
				})
				if err != nil {
					return fmt.Errorf("federation: %w", err)
				}
				// Boot probe each upstream. 4xx / DNS / malformed URL
				// are hard-fails (config error). 5xx / timeout are
				// soft-fails — log a warning, start degraded.
				probeCtx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
				for _, up := range ups {
					if err := cascade.ProbeUpstream(probeCtx, up); err != nil {
						if backend.IsProbeTransient(err) {
							log.Warn("federation upstream unreachable at boot — starting degraded",
								"upstream", up.URL, "err", err)
							continue
						}
						cancel()
						return fmt.Errorf("federation upstream %q: %w (refusing to start; fix the URL or remove from --upstream)", up.URL, err)
					}
					log.Info("federation upstream reachable", "upstream", up.URL)
				}
				cancel()
				bk = cascade
			}

			var svc api.Canopy
			var storeRef *store.Store
			if dbPath != "" {
				s, err := store.Open(cmd.Context(), dbPath)
				if err != nil {
					return fmt.Errorf("open db: %w", err)
				}
				defer func() { _ = s.Close() }()
				storeRef = s
				cs := canopy.New(s)
				cs.MirrorRoot = rootDir
				// SourcesCacheDir lets canopy.Service.Summary find the
				// unpacked source tree for bazel-module-summary-go. Same
				// path the codenav handler uses; threaded here so the
				// MCP canopy_summary tool can reach the same fixtures.
				cs.SourcesCacheDir = defaultSourcesCacheDir()
				cs.Bus = eventbus.New()
				defer cs.Bus.Close()
				// One-shot reconcile of versions.has_source_index for
				// rows whose SCIP blobs predate the cached column.
				// Logged at INFO with a count so operators see the work
				// completed; non-fatal if it fails (the search hit
				// projection just shows false until the next ingest).
				if n, err := cs.BackfillSourceIndexFlags(cmd.Context()); err != nil {
					log.Warn("has_source_index backfill failed", "err", err)
				} else if n > 0 {
					log.Info("has_source_index backfill complete", "rows_updated", n)
				}
				// Drift summary backfill seam (Plan 28 C12). M1 is a
				// no-op walker that surfaces the count of rows with
				// unknown drift via slog INFO — actionable
				// observability that motivates wiring a drift source
				// (Plan 20 bcrmirror, Plan 21 sync-runner cache,
				// Plan 26 κ6 ModuleReport-in-AC). Non-fatal on error.
				if n, err := cs.BackfillDriftSummary(cmd.Context()); err != nil {
					log.Warn("drift_summary backfill failed", "err", err)
				} else if n > 0 {
					log.Info("drift_summary backfill complete", "rows_updated", n)
				}
				// AttrsInterpret is set after featureflags parse below.
				svc = cs
			}
			_ = storeRef

			// Plan 16 Layer F: cache promotion on serve. Opt-in via
			// env. When an upstream wins a (module, version) path,
			// async-Bump that coordinate into the local mirror so
			// future serves hit the primary instead. Default OFF —
			// auto-promoting everything Bazel asks for turns the
			// local mirror into "whatever traffic happened to flow",
			// which surprises operators who want a curated mirror.
			//
			// Wired AFTER svc construction so the hook captures svc.
			// Type-assert bk → *Cascade in case federation isn't
			// configured (then bk is the bare *File and the hook
			// would be moot — nothing to promote against).
			if svc != nil && os.Getenv("CANOPY_PROMOTE_ON_SERVE") == "true" {
				if cascade, ok := bk.(*backend.Cascade); ok {
					cascade.SetPromoteHook(func(module, version string) {
						// Detached context: the hook is fired from the
						// serve goroutine but should outlive the request.
						// Generous timeout because Bump downloads +
						// hashes the tarball.
						ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
						defer cancel()
						if _, err := svc.Bump(ctx, api.BumpOptions{
							Module:  module,
							Version: version,
							Source:  "promote-on-serve",
						}); err != nil {
							log.Warn("promote-on-serve: Bump failed",
								"module", module, "version", version, "err", err)
						}
					})
					log.Info("federation: promote-on-serve enabled (CANOPY_PROMOTE_ON_SERVE=true)")
				}
			}

			// Plan 16 Layer D: collision logger wires the cascade's
			// in-process collision-detect to the store-layer
			// module_sources audit table. Always-on when both a
			// federated backend AND a store exist — the audit row is
			// cheap (INSERT OR IGNORE, write-coalesced 5min in the
			// cascade) and the operator can query the table directly
			// for "what's coming from where."
			if storeRef != nil {
				if cascade, ok := bk.(*backend.Cascade); ok {
					cascade.SetCollisionLogger(func(module, version, sourceURL, kind string) {
						ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer cancel()
						if err := storeRef.LogModuleSource(
							ctx, module, version, sourceURL, store.ModuleSourceKind(kind),
						); err != nil {
							log.Warn("module_sources log failed",
								"module", module, "version", version, "source", sourceURL, "err", err)
						}
					})
				}
			}

			// Feature flags come from env vars (12-factor). Operators
			// tune behavior by editing compose env + `docker compose up
			// -d`, not by flipping a CLI flag. Parse errors are fatal
			// — a misspelled bool should not silently fall back to the
			// default and leave the operator wondering why a knob did
			// nothing.
			flags, err := featureflags.Parse()
			if err != nil {
				return fmt.Errorf("featureflags: %w", err)
			}
			log.Info("feature flags",
				"ingest_write_enabled", flags.IngestWriteEnabled,
				"registry_url", flags.RegistryURL,
				"ingest_allow_custom_upstream", flags.IngestAllowCustomUpstream,
				"ingest_rate_limit_per_min", flags.IngestRateLimitPerMin,
				"ingest_max_concurrent", flags.IngestMaxConcurrent,
				"ingest_rate_bypass_ips", len(flags.IngestRateBypassIPs),
				"attrs_interpret", flags.AttrsInterpret,
			)

			// Propagate the AttrsInterpret flag into the canopy service
			// so IngestDir + Bump run the Tier-3 attrs hydrator after
			// each ingest. Done after the flag parse so a misconfigured
			// env var stops the boot before we mutate svc.
			if cs, ok := svc.(*canopy.Service); ok {
				cs.AttrsInterpret = flags.AttrsInterpret
			}

			// Trusted-proxy CIDR list for the header-auth scaffold.
			// Empty (default) disables header trust — personal-canopy
			// stays anonymous. Corporate deployments set this to the
			// CIDR of their reverse-proxy / ingress.
			trustedProxyCIDRs, err := server.ParseTrustedProxyCIDRs(os.Getenv("CANOPY_TRUSTED_PROXY_CIDR"))
			if err != nil {
				return fmt.Errorf("CANOPY_TRUSTED_PROXY_CIDR: %w", err)
			}

			handler := server.NewWithOptions(bk, svc, log, server.Options{
				MirrorBaseURL:     mirrorBaseURL,
				MirrorRoot:        rootDir,
				SourcesCacheDir:   defaultSourcesCacheDir(),
				Flags:             flags,
				TrustedProxyCIDRs: trustedProxyCIDRs,
			})
			// Wrap the canopy handler with farol's HTTP middleware:
			// request-id, W3C trace propagation, RED-metric histogram
			// (http.server.request.duration), structured access log,
			// and panic recovery. When OTel isn't configured the
			// underlying providers are noop — zero per-request cost.
			srv := &http.Server{
				Addr:              addr,
				Handler:           farol.HTTPMiddleware(handler),
				ReadHeaderTimeout: 10 * time.Second,
			}
			log.Info("serving", "addr", addr, "root", rootDir, "db", dbPath, "mirror_base", mirrorBaseURL)

			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			// Plan 16 follow-up: background probe loop refreshes
			// upstream reachability so /api/v1/upstreams doesn't
			// show a frozen "last_probe" from boot forever. Interval
			// is env-tunable for ops who want a different freshness
			// vs. upstream-load tradeoff; ≤0 disables the loop.
			if cascade, ok := bk.(*backend.Cascade); ok {
				probeInterval := 60 * time.Second
				if v := os.Getenv("CANOPY_UPSTREAM_PROBE_INTERVAL"); v != "" {
					if d, parseErr := time.ParseDuration(v); parseErr == nil {
						probeInterval = d
					} else {
						log.Warn("ignoring invalid CANOPY_UPSTREAM_PROBE_INTERVAL",
							"value", v, "err", parseErr)
					}
				}
				go cascade.RunProbeLoop(ctx, probeInterval)
			}

			errCh := make(chan error, 1)
			go func() { errCh <- srv.ListenAndServe() }()
			select {
			case <-ctx.Done():
				shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				return srv.Shutdown(shutCtx)
			case err := <-errCh:
				if errors.Is(err, http.ErrServerClosed) {
					return nil
				}
				return err
			}
		},
	}
	cmd.Flags().StringVar(&rootDir, "root", "", "filesystem directory holding the BCR-shape registry tree (enables BCR endpoints)")
	cmd.Flags().StringVar(&dbPath, "db", "", "SQLite index path (enables /api/* endpoints)")
	cmd.Flags().StringVar(&addr, "addr", ":8080", "address to listen on")
	cmd.Flags().StringVar(&mirrorBaseURL, "mirror-base-url", "", "advertise canopy as a tarball mirror via bazel_registry.json.mirrors (e.g. http://canopy.local:8080/m/)")
	cmd.Flags().StringArrayVar(&upstreamURLs, "upstream", nil, "BCR-shape registry to cascade-fallback to on local miss (repeatable; env CANOPY_UPSTREAMS=url1,url2). Each URL must be the directory containing bazel_registry.json. Response cache (Plan 16 Layer C) is enabled by default at 1000 entries; tune via env CANOPY_UPSTREAM_CACHE_SIZE (set to a negative integer to disable). Cache promotion on serve (Plan 16 Layer F) — async-Bump every upstream-won (m, v) into the local mirror — is off by default; opt in via env CANOPY_PROMOTE_ON_SERVE=true (changes the mirror from curated to greedy).")
	return cmd
}
