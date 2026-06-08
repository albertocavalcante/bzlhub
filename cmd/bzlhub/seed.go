package main

import (
	"context"
	"errors"
	"fmt"
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
	Inserted int
	Skipped  int
}

// newSeedCmd registers `bzlhub seed` — populate the procurement
// requests table with a canonical set of pending submissions so a
// fresh demo deployment isn't empty.
//
// Idempotent: skips any (module, version) that already has a row
// regardless of state.
func newSeedCmd() *cobra.Command {
	var (
		dbPath    string
		submitter string
		modules   []string
	)
	cmd := &cobra.Command{
		Use:   "seed",
		Short: "Seed procurement requests for a fresh deployment",
		Long: "Inserts a canonical set of `pending` procurement requests so a fresh " +
			"corp.bzlhub.com demo has visible content. Idempotent — re-runs skip " +
			"any (module, version) already present in the requests table.",
		Example: `  # Default 12-rule set with seed-bot as submitter
  bzlhub seed --db=/var/bzlhub/bzlhub.db --submitter=seed-bot@example.com

  # Custom module list
  bzlhub seed --db=/var/bzlhub/bzlhub.db --submitter=seed-bot@example.com \
              --module=rules_go@0.60.0 --module=rules_python@1.5.0`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dbPath == "" {
				return errors.New("--db is required (path to bzlhub.db)")
			}
			if submitter == "" {
				return errors.New("--submitter is required (email recorded as the request submitter)")
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

			res, err := seedRequests(cmd.Context(), s, entries, submitter)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"seed: inserted=%d skipped=%d total=%d\n",
				res.Inserted, res.Skipped, len(entries))
			return nil
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", "", "SQLite index path (bzlhub.db)")
	cmd.Flags().StringVar(&submitter, "submitter", "",
		"email recorded as submitter_sub + submitter_email (e.g. seed-bot@example.com)")
	cmd.Flags().StringArrayVar(&modules, "module", nil,
		"override the canonical set with one or more `module@version` entries (repeatable)")
	return cmd
}

// seedRequests inserts every entry into the store unless a row for
// that (module, version) already exists. Reports counts via
// seedResult; the caller surfaces them however it likes.
func seedRequests(ctx context.Context, s *store.Store, entries []seedEntry, submitter string) (seedResult, error) {
	var res seedResult
	for _, e := range entries {
		exists, err := s.AnyRequestFor(ctx, e.Module, e.Version)
		if err != nil {
			return res, fmt.Errorf("probe %s@%s: %w", e.Module, e.Version, err)
		}
		if exists {
			res.Skipped++
			continue
		}
		if _, err := s.CreateRequest(ctx, store.Request{
			SubmitterSub:   submitter,
			SubmitterEmail: submitter,
			AuthMethod:     "seed",
			Module:         e.Module,
			Version:        e.Version,
			SubmitterNotes: "seeded by `bzlhub seed`",
		}); err != nil {
			return res, fmt.Errorf("create %s@%s: %w", e.Module, e.Version, err)
		}
		res.Inserted++
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
