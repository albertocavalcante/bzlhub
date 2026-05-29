package main

import (
	"github.com/spf13/cobra"

	"github.com/albertocavalcante/canopy/internal/canopy"
	"github.com/albertocavalcante/canopy/internal/mcpsrv"
	"github.com/albertocavalcante/canopy/internal/store"
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
			svc := canopy.New(s)
			svc.MirrorRoot = rootDir
			// Same SourcesCacheDir as serve — required for the
			// canopy_summary MCP tool to find the unpacked tarball
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
	cmd.Flags().StringVar(&rootDir, "root", "", "filesystem path of the BCR-shape mirror (required for canopy_drift / canopy_bump tools)")
	cmd.Flags().StringVar(&upstream, "upstream", "", "default upstream registry URL for canopy_drift / canopy_bump (defaults to https://bcr.bazel.build)")
	return cmd
}
