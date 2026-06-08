package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/albertocavalcante/bzlhub/internal/store"
)

// seedEntry is one (module, version) pair the seed run inserts.
type seedEntry struct {
	Module  string
	Version string
}

// defaultSeedSet is the canonical 12-rule list a fresh corp.bzlhub.com
// gets so visitors see real content rather than an empty queue. Picked
// for breadth (multiple language ecosystems) and freshness (versions
// current as of the v0 demo cut — bump as the BCR evolves).
var defaultSeedSet = []seedEntry{
	{Module: "bazel_skylib", Version: "1.7.1"},
	{Module: "platforms", Version: "1.0.0"},
	{Module: "rules_cc", Version: "0.1.4"},
	{Module: "rules_java", Version: "8.13.0"},
	{Module: "rules_go", Version: "0.60.0"},
	{Module: "rules_python", Version: "1.5.0"},
	{Module: "rules_oci", Version: "2.2.6"},
	{Module: "rules_proto", Version: "7.1.0"},
	{Module: "protobuf", Version: "31.1"},
	{Module: "googletest", Version: "1.17.0"},
	{Module: "aspect_bazel_lib", Version: "2.21.0"},
	{Module: "abseil-cpp", Version: "20250127.0"},
}

// seedResult summarizes what one seed run did. Returned from
// seedRequests so the CLI can print a one-line summary and the
// tests can assert on counts.
type seedResult struct {
	Inserted        int
	Skipped         int
	IndexedDirectly int // populated only when AutoApprove=true
}

// seedOptions configures a seedRequests run.
type seedOptions struct {
	Submitter   string
	AutoApprove bool // also UpsertSeedVersion → /modules surface immediately
}

// newSeedCmd registers `bzlhub seed` — populate the procurement
// requests table with a canonical set of pending submissions so a
// fresh demo deployment isn't empty.
//
// Idempotent: skips any (module, version) that already has a row
// regardless of state.
func newSeedCmd() *cobra.Command {
	var (
		dbPath      string
		submitter   string
		modules     []string
		autoApprove bool
	)
	cmd := &cobra.Command{
		Use:   "seed",
		Short: "Seed procurement requests for a fresh deployment",
		Long: "Inserts a canonical set of `pending` procurement requests so a fresh " +
			"corp.bzlhub.com demo has visible content. Idempotent — re-runs skip " +
			"any (module, version) already present in the requests table.\n\n" +
			"Use --auto-approve (demo-only — requires BZLHUB_DEMO_MODE=true) to ALSO " +
			"populate the modules + versions index tables so /modules surfaces the " +
			"seeded rows immediately, bypassing the procurement state machine. Plan " +
			"76 §2.7's first-impression demo fix.",
		Example: `  # Default 12-rule set with seed-bot as submitter
  bzlhub seed --db=/var/bzlhub/bzlhub.db --submitter=seed-bot@example.com

  # Custom module list
  bzlhub seed --db=/var/bzlhub/bzlhub.db --submitter=seed-bot@example.com \
              --module=rules_go@0.60.0 --module=rules_python@1.5.0

  # Demo: also publish to /modules immediately
  BZLHUB_DEMO_MODE=true bzlhub seed --db=/var/bzlhub/bzlhub.db \
              --submitter=seed-bot@example.com --auto-approve`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dbPath == "" {
				return errors.New("--db is required (path to bzlhub.db)")
			}
			if submitter == "" {
				return errors.New("--submitter is required (email recorded as the request submitter)")
			}
			if autoApprove && os.Getenv("BZLHUB_DEMO_MODE") != "true" {
				return errors.New(
					"--auto-approve requires BZLHUB_DEMO_MODE=true " +
						"(demo flag, not for production)")
			}

			entries := defaultSeedSet
			if len(modules) > 0 {
				entries = make([]seedEntry, 0, len(modules))
				for _, m := range modules {
					e, err := parseSeedEntry(m)
					if err != nil {
						return fmt.Errorf("--module %q: %w", m, err)
					}
					entries = append(entries, e)
				}
			}

			s, err := store.Open(cmd.Context(), dbPath)
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer s.Close()

			res, err := seedRequestsWithOptions(cmd.Context(), s, entries,
				seedOptions{Submitter: submitter, AutoApprove: autoApprove})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"seed: inserted=%d skipped=%d indexed_directly=%d total=%d\n",
				res.Inserted, res.Skipped, res.IndexedDirectly, len(entries))
			return nil
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", "", "SQLite index path (bzlhub.db)")
	cmd.Flags().StringVar(&submitter, "submitter", "",
		"email recorded as submitter_sub + submitter_email (e.g. seed-bot@example.com)")
	cmd.Flags().StringArrayVar(&modules, "module", nil,
		"override the canonical set with one or more `module@version` entries (repeatable)")
	cmd.Flags().BoolVar(&autoApprove, "auto-approve", false,
		"ALSO insert directly into the modules+versions index so /modules shows entries "+
			"immediately. Demo-only — requires BZLHUB_DEMO_MODE=true. "+
			"Bypasses the procurement state machine; do not use in production.")
	return cmd
}

// seedRequests is the simple-API wrapper for the default no-flags
// path. Kept for callers that don't need the auto-approve gate.
func seedRequests(ctx context.Context, s *store.Store, entries []seedEntry, submitter string) (seedResult, error) {
	return seedRequestsWithOptions(ctx, s, entries, seedOptions{Submitter: submitter})
}

// seedRequestsWithOptions inserts every entry into the store unless a
// row for that (module, version) already exists. When opts.AutoApprove
// is true, additionally calls Store.UpsertSeedVersion so the entry
// appears in /modules immediately (Plan 76 §2.7 demo fix). Reports
// counts via seedResult; the caller surfaces them however it likes.
func seedRequestsWithOptions(ctx context.Context, s *store.Store, entries []seedEntry, opts seedOptions) (seedResult, error) {
	var res seedResult
	for _, e := range entries {
		exists, err := s.AnyRequestFor(ctx, e.Module, e.Version)
		if err != nil {
			return res, fmt.Errorf("probe %s@%s: %w", e.Module, e.Version, err)
		}
		if exists {
			res.Skipped++
		} else {
			if _, err := s.CreateRequest(ctx, store.Request{
				SubmitterSub:   opts.Submitter,
				SubmitterEmail: opts.Submitter,
				AuthMethod:     "seed",
				Module:         e.Module,
				Version:        e.Version,
				SubmitterNotes: "seeded by `bzlhub seed`",
			}); err != nil {
				return res, fmt.Errorf("create %s@%s: %w", e.Module, e.Version, err)
			}
			res.Inserted++
		}
		if opts.AutoApprove {
			if err := s.UpsertSeedVersion(ctx, e.Module, e.Version); err != nil {
				return res, fmt.Errorf("upsert seed version %s@%s: %w", e.Module, e.Version, err)
			}
			res.IndexedDirectly++
		}
	}
	return res, nil
}

// parseSeedEntry parses a "module@version" string. Empty fields or
// malformed input (no @, multiple @, missing side) return an error.
func parseSeedEntry(s string) (seedEntry, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return seedEntry{}, errors.New("empty entry")
	}
	parts := strings.Split(s, "@")
	if len(parts) != 2 {
		return seedEntry{}, errors.New(`want "module@version"`)
	}
	mod := strings.TrimSpace(parts[0])
	ver := strings.TrimSpace(parts[1])
	if mod == "" || ver == "" {
		return seedEntry{}, errors.New("module and version both required")
	}
	return seedEntry{Module: mod, Version: ver}, nil
}
