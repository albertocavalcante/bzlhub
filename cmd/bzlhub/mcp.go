package main

import (
	"github.com/spf13/cobra"

	"github.com/albertocavalcante/bzlhub/internal/bzlhub"
	"github.com/albertocavalcante/bzlhub/internal/mcpsrv"
	"github.com/albertocavalcante/bzlhub/internal/store"
)

func newMCPCmd() *cobra.Command {
	var (
		dbPath   string
		rootDir  string
		upstream string
	)
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve canopy as an MCP server over stdio for coding agents",
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := store.Open(cmd.Context(), dbPath)
			if err != nil {
				return err
			}
			defer s.Close()
			svc := bzlhub.New(s)
			svc.MirrorRoot = rootDir
			// Same SourcesCacheDir as serve — required for the
			// bzlhub_summary MCP tool to find the unpacked tarball
			// tree per (module, version).
			svc.SourcesCacheDir = defaultSourcesCacheDir()
			if upstream != "" {
				svc.DefaultUpstream = upstream
			}
			// svc satisfies both api.Canopy and mcpsrv.Verifier; one
			// concrete implementation, two separate interfaces (see the
			// Verifier doc-comment in mcpsrv for why they don't fuse).
			return mcpsrv.Serve(cmd.Context(), svc, svc, "0.0.0")
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", defaultDBPath, "SQLite index path")
	cmd.Flags().StringVar(&rootDir, "root", "", "filesystem path of the BCR-shape mirror (required for bzlhub_drift / bzlhub_bump tools)")
	cmd.Flags().StringVar(&upstream, "upstream", "", "default upstream registry URL for bzlhub_drift / bzlhub_bump (defaults to https://bcr.bazel.build)")
	return cmd
}
