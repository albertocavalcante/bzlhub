package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/albertocavalcante/bzlhub/internal/api"
	"github.com/albertocavalcante/bzlhub/internal/auth"
	"github.com/albertocavalcante/bzlhub/internal/backend"
	"github.com/albertocavalcante/bzlhub/internal/bzlhub"
	"github.com/albertocavalcante/bzlhub/internal/egress"
	"github.com/albertocavalcante/bzlhub/internal/eventbus"
	"github.com/albertocavalcante/bzlhub/internal/featureflags"
	"github.com/albertocavalcante/bzlhub/internal/server"
	"github.com/albertocavalcante/bzlhub/internal/admit"
	"github.com/albertocavalcante/bzlhub/internal/audit"
	"github.com/albertocavalcante/bzlhub/internal/fetch"
	"github.com/albertocavalcante/bzlhub/internal/policy"
	"github.com/albertocavalcante/bzlhub/internal/preflight"
	"github.com/albertocavalcante/bzlhub/internal/publish"
	"github.com/albertocavalcante/bzlhub/internal/purge"
	canopyruntime "github.com/albertocavalcante/bzlhub/internal/runtime"
	"github.com/albertocavalcante/bzlhub/internal/store"
	"github.com/albertocavalcante/bzlhub/internal/version"
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
			httpStoreCfg := backend.LoadHTTPStoreConfig()
			if rootDir == "" && dbPath == "" && !httpStoreCfg.Set() {
				return errors.New("at least one of --root (BCR substrate), --db (bzlhub index), or BZLHUB_BACKEND_KIND (HTTP store / Artifactory) is required")
			}
			if rootDir != "" && httpStoreCfg.Set() {
				return errors.New("--root and BZLHUB_BACKEND_KIND are mutually exclusive (pick one primary substrate)")
			}
			log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

			var bk backend.Backend
			switch {
			case httpStoreCfg.Set():
				// HTTP store / Artifactory primary (Plan 23 η3 / η4).
				// Auth and Layout are resolved from BZLHUB_* env vars
				// before any network call; misconfiguration fails boot
				// rather than first-request.
				//
				// HTTP client comes from egress so backend reads are
				// gated by the same policy that fronts cascade /
				// webhook / forge (feedback_corporate_security_first
				// — egress is the only sanctioned http.Client factory).
				httpClient := egress.NewHTTPClient(egress.Policy{})
				hs, err := httpStoreCfg.Build(httpClient)
				if err != nil {
					return fmt.Errorf("backend config: %w", err)
				}
				log.Info("backend: HTTP store",
					"kind", httpStoreCfg.Kind,
					"base_url", httpStoreCfg.BaseURL,
					"auth", hs.Store().AuthName())
				bk = hs
			case rootDir != "":
				// NewFromRoot picks File for a plain dir and
				// BCRMirror for a git clone — operators get the
				// drift-aware backend transparently once the registry
				// is sync'd via `bzlhub sync` (PR8).
				b, err := backend.NewFromRoot(cmd.Context(), rootDir)
				if err != nil {
					return fmt.Errorf("registry root %q: %w", rootDir, err)
				}
				bk = b
			}

			// Federation (Plan 16). Flag overrides env; env is
			// comma-separated. Empty in both → no federation, bk stays
			// as-is.
			upstreams := upstreamURLs
			if len(upstreams) == 0 {
				if env := os.Getenv("BZLHUB_UPSTREAMS"); env != "" {
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
				if v := os.Getenv("BZLHUB_UPSTREAM_CACHE_SIZE"); v != "" {
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
				if v := os.Getenv("BZLHUB_DISABLE_SHADOW_DETECTION"); v != "" {
					if b, parseErr := strconv.ParseBool(v); parseErr == nil {
						disableShadow = b
					} else {
						log.Warn("ignoring invalid BZLHUB_DISABLE_SHADOW_DETECTION",
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
			// verifier carries the concrete *bzlhub.Service so the
			// /mcp transport (when BZLHUB_MCP_HTTP_ENABLED) can wire
			// bzlhub_verify alongside the read-side tools. Same
			// concrete also satisfies api.Canopy; the split here is
			// purely to keep server.Options' Verifier field typed
			// against mcpsrv.Verifier without an unsafe assertion.
			var verifier *bzlhub.Service
			var storeRef *store.Store
			if dbPath != "" {
				s, err := store.Open(cmd.Context(), dbPath)
				if err != nil {
					return fmt.Errorf("open db: %w", err)
				}
				defer func() { _ = s.Close() }()
				storeRef = s
				cs := bzlhub.New(s)
				cs.MirrorRoot = rootDir
				// When the auto-detected backend is the git-aware
				// BCRMirror, thread the underlying Mirror to Service
				// so BackfillDriftSummary (PR7) can compute drift
				// from upstream metadata. On the File path bk is
				// *backend.File and the type-assertion is a no-op —
				// drift stays at the default unknown state and
				// surfaces in the boot log.
				attachMirror(cs, bk, log)
				// SourcesCacheDir lets bzlhub.Service.Summary find the
				// unpacked source tree for bazel-module-summary-go. Same
				// path the codenav handler uses; threaded here so the
				// MCP bzlhub_summary tool can reach the same fixtures.
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
				verifier = cs
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
			if svc != nil && os.Getenv("BZLHUB_PROMOTE_ON_SERVE") == "true" {
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
					log.Info("federation: promote-on-serve enabled (BZLHUB_PROMOTE_ON_SERVE=true)")
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
			if cs, ok := svc.(*bzlhub.Service); ok {
				cs.AttrsInterpret = flags.AttrsInterpret
			}

			// Trusted-proxy CIDR list for the header-auth scaffold.
			// Empty (default) disables header trust — personal-canopy
			// stays anonymous. Corporate deployments set this to the
			// CIDR of their reverse-proxy / ingress.
			trustedProxyCIDRs, err := server.ParseTrustedProxyCIDRs(os.Getenv("BZLHUB_TRUSTED_PROXY_CIDR"))
			if err != nil {
				return fmt.Errorf("BZLHUB_TRUSTED_PROXY_CIDR: %w", err)
			}

			// Safety gate: refuse to boot in a direct-exposure
			// ingest-write configuration unless the operator has
			// explicitly opted out via BZLHUB_REQUIRE_FRONT_PROXY=false.
			// Closes the SECURITY-TODO that ratelimit.go + featureflags.go
			// flag — write surface must be authn-gated by SOMETHING
			// (CF Access, mTLS, OIDC reverse proxy) before the box is
			// reachable.
			if err := flags.CheckSafeStartup(len(trustedProxyCIDRs) > 0); err != nil {
				return err
			}

			// Bearer-token identity registry (Plan 72 §C3). Missing
			// file → no bearer auth (anonymous-only continues fine);
			// malformed file → fail fast at boot. Operators wanting
			// bearer auth set BZLHUB_IDENTITY_FILE; everyone else
			// runs as today with header-auth only.
			var bearerRegistry *auth.IdentityRegistry
			identityPath := strings.TrimSpace(os.Getenv("BZLHUB_IDENTITY_FILE"))
			if identityPath != "" {
				warnIfIdentityFileWorldReadable(log, identityPath)
				reg, err := auth.LoadIdentityFile(identityPath)
				switch {
				case err == nil:
					bearerRegistry = reg
					log.Info("bearer identity registry loaded", "path", identityPath, "tokens", reg.Size())
				case errors.Is(err, auth.ErrIdentityFileMissing):
					return fmt.Errorf("BZLHUB_IDENTITY_FILE=%s: file not found (unset the env var to disable bearer auth)", identityPath)
				default:
					return fmt.Errorf("BZLHUB_IDENTITY_FILE=%s: %w", identityPath, err)
				}
			}

			// Policy (.canopy/policy.yml — Plan 71, chunk 6). Missing
			// file → no policy, procurement routes not registered;
			// malformed file → fail fast. Operators wanting policy
			// gates set BZLHUB_POLICY_FILE. Diagnostics emitted as
			// WARN per Plan 71 Q71.3 (permissive forward-compat).
			var pol *policy.Policy
			policyPath := strings.TrimSpace(os.Getenv("BZLHUB_POLICY_FILE"))
			if policyPath != "" {
				p, diags, err := policy.LoadFile(policyPath)
				switch {
				case err == nil:
					pol = p
					log.Info("policy loaded", "path", policyPath, "profile", p.Profile)
					for _, d := range diags {
						log.Warn("policy diagnostic", "path", d.Path, "msg", d.Message)
					}
				case errors.Is(err, policy.ErrPolicyFileMissing):
					return fmt.Errorf("BZLHUB_POLICY_FILE=%s: file not found (unset the env var to disable policy gates)", policyPath)
				default:
					return fmt.Errorf("BZLHUB_POLICY_FILE=%s: %w", policyPath, err)
				}
			}

			// SIGHUP-reloadable policy. Handlers + checker call the
			// snapshot getter on every read so policySwap() takes
			// effect on the next request without rebuilding routes.
			// Daemons (retention, webhook) snapshot at construction —
			// changing their intervals still requires a restart.
			var policyAtom atomic.Pointer[policy.Policy]
			if pol != nil {
				policyAtom.Store(pol)
			}
			policySnap := policy.Snapshot(func() *policy.Policy { return policyAtom.Load() })

			handler := server.NewWithOptions(bk, svc, log, server.Options{
				MirrorBaseURL:     mirrorBaseURL,
				MirrorRoot:        rootDir,
				SourcesCacheDir:   defaultSourcesCacheDir(),
				Flags:             flags,
				TrustedProxyCIDRs: trustedProxyCIDRs,
				BearerRegistry:    bearerRegistry,
				RequestStore:      storeRef,
				Policy:            policySnap,
				// verifier is the concrete *bzlhub.Service (nil when
				// --db is unset). The /mcp transport gates further
				// on flags.MCPHTTPEnabled.
				Verifier: verifier,
				Version:  version.Version,
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

			// SIGHUP reloads the bearer-identity registry from
			// BZLHUB_IDENTITY_FILE + BZLHUB_POLICY_FILE. Operators
			// rotating tokens or policy edit the file(s) then send
			// SIGHUP; no container restart needed. Parse failure on
			// either reload keeps the current value — never accept
			// malformed state.
			//
			// Daemons (audit retention, webhook) snapshot policy at
			// construction; their intervals don't change on SIGHUP.
			// HTTP gates and the preflight checker re-read on every
			// request via the policySnap getter, so policy reload
			// effects are immediate for the user-visible path.
			reloader := canopyruntime.NewReloader(log)
			if bearerRegistry != nil {
				reloader.Register("identity", func(context.Context) error {
					newReg, err := auth.LoadIdentityFile(identityPath)
					if err != nil {
						return err
					}
					bearerRegistry.Replace(newReg)
					log.Info("SIGHUP: identity registry reloaded",
						"path", identityPath, "tokens", bearerRegistry.Size())
					return nil
				})
			}
			if policyPath != "" {
				reloader.Register("policy", func(context.Context) error {
					newPol, diags, err := policy.LoadFile(policyPath)
					if err != nil {
						return err
					}
					policyAtom.Store(newPol)
					log.Info("SIGHUP: policy reloaded",
						"path", policyPath, "profile", newPol.Profile)
					for _, d := range diags {
						log.Warn("policy diagnostic", "path", d.Path, "msg", d.Message)
					}
					return nil
				})
			}
			if reloader.HasReloaders() {
				go reloader.Run(ctx, canopyruntime.SIGHUPTrigger(ctx))
			}

			// Procurement preflight runner (Plan 67, chunk 4 §C7).
			// Pending requests are picked up by a worker pool, run
			// through the default checker (v0: source URL validation
			// + needs_review default), and transitioned to
			// auto_pass / needs_review / denied. Only wired when
			// both store + policy are configured — same gate as
			// the HTTP procurement endpoints. Workers + poll
			// interval env-tunable.
			if storeRef != nil && pol != nil {
				workers := 2
				if v := strings.TrimSpace(os.Getenv("BZLHUB_PREFLIGHT_WORKERS")); v != "" {
					if n, parseErr := strconv.Atoi(v); parseErr == nil && n > 0 {
						workers = n
					} else {
						log.Warn("ignoring invalid BZLHUB_PREFLIGHT_WORKERS",
							"value", v, "err", parseErr)
					}
				}
				pollEvery := 5 * time.Second
				if v := strings.TrimSpace(os.Getenv("BZLHUB_PREFLIGHT_POLL_EVERY")); v != "" {
					if d, parseErr := time.ParseDuration(v); parseErr == nil && d > 0 {
						pollEvery = d
					} else {
						log.Warn("ignoring invalid BZLHUB_PREFLIGHT_POLL_EVERY",
							"value", v, "err", parseErr)
					}
				}
				checker := preflight.NewDefaultChecker(policySnap)
				// Wire the cascade short-circuit when the operator has
				// declared an upstream BCR via BZLHUB_UPSTREAMS. The
				// first comma-separated entry becomes the cascade
				// target — typically https://bcr.bazel.build. Policy
				// `admission.review.auto_pass_on_already_in_upstream`
				// still gates whether the verdict actually fires.
				if up := firstUpstream(); up != "" {
					checker.Cascade = preflight.NewBCRProbe(up, fetch.NewClient().HTTP)
					log.Info("preflight cascade probe wired", "upstream", up)
				}
				runner := preflight.New(preflight.Options{
					Store:     storeRef,
					Checker:   checker,
					Workers:   workers,
					PollEvery: pollEvery,
					Log:       log,
				})
				go runner.Run(ctx)

				// Admit runner (Plan 73 slice 1B). Picks up
				// auto_pass + approved requests, fetches the source
				// archive, materializes the BCR-shape entry via
				// the publish package, and transitions to indexed
				// (success) or denied (failure with error captured
				// in denial_reason).
				//
				// Wired only when BZLHUB_REGISTRY_WORKTREE points
				// at a git working clone of the registry repo
				// (defaults to BZLHUB_ROOT when that IS a git
				// clone). FilesystemPublisher used as the safe
				// fallback when no git clone is present — the
				// pipeline still materializes BCR-shape files; just
				// no commit / push.
				// Audit retention daemon. Reads policy.audit.retain_days
				// and sweeps audit_events older than the cutoff on the
				// configured interval (default 1h). RetainDays=0 leaves
				// the daemon inert.
				retentionDaemon := audit.NewRetentionDaemon(storeRef, audit.RetentionOptions{
					RetainDays: pol.Audit.RetainDays,
					Log:        log,
				})
				go retentionDaemon.Run(ctx)

				// Audit webhook delivery — POSTs each new audit event
				// to policy.audit.webhook_url. Best-effort-once: a
				// failed POST advances the watermark so we don't spam
				// the endpoint when it's hard-down. Boot watermark
				// stamps in the constructor so events recorded between
				// startup and the first sweep aren't missed.
				webhookDaemon := audit.NewWebhookDaemon(storeRef, audit.WebhookOptions{
					URL: pol.Audit.WebhookURL,
					Log: log,
				})
				go webhookDaemon.Run(ctx)

				if publisher, perr := buildAdmitPublisher(rootDir, log); perr != nil {
					return fmt.Errorf("admit publisher: %w", perr)
				} else if publisher != nil {
					purger, cdnBase := buildPurger(log)
					admitRunner := admit.New(admit.Options{
						Store:     storeRef,
						Publisher: publisher,
						Fetcher:   admit.NewHTTPFetcher(fetch.NewClient().HTTP, admitArchiveCap(pol)),
						BotIdent: publish.Identity{
							Name:  "bzlhub-bot",
							Email: envOr("BZLHUB_BOT_EMAIL", "bzlhub-bot@localhost"),
						},
						Workers:    1,
						PollEvery:  pollEvery,
						Log:        log,
						Purger:     purger,
						CDNBaseURL: cdnBase,
					})
					go admitRunner.Run(ctx)
				}
			}

			// Plan 16 follow-up: background probe loop refreshes
			// upstream reachability so /api/v1/upstreams doesn't
			// show a frozen "last_probe" from boot forever. Interval
			// is env-tunable for ops who want a different freshness
			// vs. upstream-load tradeoff; ≤0 disables the loop.
			if cascade, ok := bk.(*backend.Cascade); ok {
				probeInterval := 60 * time.Second
				if v := os.Getenv("BZLHUB_UPSTREAM_PROBE_INTERVAL"); v != "" {
					if d, parseErr := time.ParseDuration(v); parseErr == nil {
						probeInterval = d
					} else {
						log.Warn("ignoring invalid BZLHUB_UPSTREAM_PROBE_INTERVAL",
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
	cmd.Flags().StringArrayVar(&upstreamURLs, "upstream", nil, "BCR-shape registry to cascade-fallback to on local miss (repeatable; env BZLHUB_UPSTREAMS=url1,url2). Each URL must be the directory containing bazel_registry.json. Response cache (Plan 16 Layer C) is enabled by default at 1000 entries; tune via env BZLHUB_UPSTREAM_CACHE_SIZE (set to a negative integer to disable). Cache promotion on serve (Plan 16 Layer F) — async-Bump every upstream-won (m, v) into the local mirror — is off by default; opt in via env BZLHUB_PROMOTE_ON_SERVE=true (changes the mirror from curated to greedy).")
	return cmd
}

// buildAdmitPublisher selects the publish.Publisher implementation
// used by the admit runner.
//
// Selection rules:
//   - BZLHUB_REGISTRY_WORKTREE set + points at a git clone →
//     GitDirectPublisher (commits + pushes via the configured
//     remote).
//   - BZLHUB_REGISTRY_WORKTREE set but NOT a git clone →
//     boot error (operator misconfigured).
//   - Unset, rootDir IS a git clone → GitDirectPublisher rooted at
//     rootDir.
//   - Unset, rootDir is not a git clone → FilesystemPublisher
//     (writes BCR-shape files locally; no commit / push). Useful
//     for personal-canopy installs that don't keep their registry
//     in git.
//
// Returns (nil, nil) when rootDir is empty — admit can't run
// without a destination; caller skips wiring the runner.
func buildAdmitPublisher(rootDir string, log *slog.Logger) (publish.Publisher, error) {
	worktree := strings.TrimSpace(os.Getenv("BZLHUB_REGISTRY_WORKTREE"))
	if worktree == "" {
		if rootDir == "" {
			return nil, nil
		}
		worktree = rootDir
	}

	bot := publish.Identity{
		Name:  "bzlhub-bot",
		Email: envOr("BZLHUB_BOT_EMAIL", "bzlhub-bot@localhost"),
	}
	if _, err := os.Stat(filepath.Join(worktree, ".git")); err == nil {
		gp, perr := publish.NewGitDirect(publish.GitDirectConfig{
			WorkTree:    worktree,
			BotIdentity: bot,
		})
		if perr != nil {
			return nil, fmt.Errorf("git-direct %s: %w", worktree, perr)
		}
		log.Info("admit publisher: git-direct", "worktree", worktree)
		return gp, nil
	}
	if envSet("BZLHUB_REGISTRY_WORKTREE") {
		return nil, fmt.Errorf("BZLHUB_REGISTRY_WORKTREE=%s is not a git working tree (no .git dir)", worktree)
	}
	fp, perr := publish.NewFilesystem(worktree)
	if perr != nil {
		return nil, fmt.Errorf("filesystem publisher %s: %w", worktree, perr)
	}
	log.Info("admit publisher: filesystem (no git push)", "root", worktree)
	return fp, nil
}

// admitArchiveCap returns the max source-archive size the admit
// pipeline will accept, sourced from policy.admission.cost.
// Defaults to 500 MiB when policy doesn't set one.
func admitArchiveCap(pol *policy.Policy) int64 {
	const defaultCap int64 = 500 << 20
	if pol == nil || pol.Admission.Cost.MaxArchiveSizeBytes <= 0 {
		return defaultCap
	}
	return pol.Admission.Cost.MaxArchiveSizeBytes
}

func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

// buildPurger constructs the CDN purge provider from env vars +
// returns the canopy CDN-public origin used to compute purge URLs.
//
// Env vars (all optional):
//
//	BZLHUB_CDN_VENDOR     "noop" | "cloudflare" | "fastly" (default: noop)
//	BZLHUB_CDN_BASE_URL   public origin reachable through the CDN
//	                      (e.g. "https://bcr.bzlhub.com")
//	BZLHUB_CF_API_TOKEN   Cloudflare API token (vendor=cloudflare)
//	BZLHUB_CF_ZONE_ID     Cloudflare zone ID (vendor=cloudflare)
//	BZLHUB_FASTLY_API_TOKEN   Fastly API token (vendor=fastly)
//	BZLHUB_FASTLY_SERVICE_ID  Fastly service ID (vendor=fastly)
//
// Misconfiguration (vendor=cloudflare with no token, etc.) logs a
// Warn and falls back to NoOp so canopy still serves traffic — the
// CDN just doesn't get invalidated.
func buildPurger(log *slog.Logger) (purge.Provider, string) {
	vendor := strings.TrimSpace(os.Getenv("BZLHUB_CDN_VENDOR"))
	baseURL := strings.TrimSpace(os.Getenv("BZLHUB_CDN_BASE_URL"))
	cfg := purge.Config{
		Vendor:             vendor,
		CloudflareAPIToken: os.Getenv("BZLHUB_CF_API_TOKEN"),
		CloudflareZoneID:   os.Getenv("BZLHUB_CF_ZONE_ID"),
		FastlyAPIToken:     os.Getenv("BZLHUB_FASTLY_API_TOKEN"),
		FastlyServiceID:    os.Getenv("BZLHUB_FASTLY_SERVICE_ID"),
		Log:                log,
	}
	p, err := purge.Build(cfg)
	if err != nil {
		log.Warn("cdn purger misconfigured, falling back to noop",
			"vendor", vendor, "err", err)
	}
	if p.Name() != "noop" {
		log.Info("cdn purger configured",
			"vendor", p.Name(),
			"base_url", baseURL)
	}
	return p, baseURL
}

func envSet(name string) bool {
	_, ok := os.LookupEnv(name)
	return ok
}

// firstUpstream returns the first comma-separated entry from
// BZLHUB_UPSTREAMS, trimmed. The preflight cascade probe targets
// this single upstream — federation across multiple upstreams is
// not in scope for v0 cascade.
func firstUpstream() string {
	raw := strings.TrimSpace(os.Getenv("BZLHUB_UPSTREAMS"))
	if raw == "" {
		return ""
	}
	for _, s := range strings.Split(raw, ",") {
		if u := strings.TrimSpace(s); u != "" {
			return u
		}
	}
	return ""
}

// warnIfIdentityFileWorldReadable emits a single WARN at boot when
// the identity file mode allows group or world read. Bearer tokens
// hashed into the file are credential material — the file should be
// 0600 (or 0640 with a dedicated canopy group). Doesn't refuse to
// start; some operators legitimately stage permissive permissions
// during bring-up. SSH-style "refuse mode-644 keys" would block
// docker-compose's typical bind-mount default (0644) and harm
// dogfooding ergonomics. WARN once at boot is the right pressure.
//
// Best-effort: any stat error is silently ignored (the subsequent
// LoadIdentityFile call will surface the real problem).
func warnIfIdentityFileWorldReadable(log *slog.Logger, path string) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	mode := info.Mode().Perm()
	if mode&0o077 != 0 {
		log.Warn("identity file is group- or world-readable; bearer tokens are credential material — tighten with `chmod 600`",
			"path", path,
			"mode", fmt.Sprintf("%#o", mode))
	}
}
