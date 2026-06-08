package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/albertocavalcante/assay/report"
	"github.com/albertocavalcante/bzlhub/internal/api"
	"github.com/albertocavalcante/bzlhub/internal/store"
)

func newSearchCmd() *cobra.Command {
	var (
		dbPath      string
		hermeticity []string
		limit       int
	)
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search the index for modules / rules / providers / macros",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := store.Open(cmd.Context(), dbPath)
			if err != nil {
				return err
			}
			defer s.Close()
			q := api.Query{Text: args[0], Limit: limit}
			for _, h := range hermeticity {
				q.Hermeticity = append(q.Hermeticity, report.HermeticityClass(h))
			}
			results, err := s.Search(cmd.Context(), q)
			if err != nil {
				return err
			}
			if len(results.Hits) == 0 {
				fmt.Println("no hits")
				return nil
			}
			for _, h := range results.Hits {
				fmt.Printf("[%s] %s@%s :: %s — %s\n", h.MatchKind, h.Module, h.Version, h.MatchName, h.Snippet)
			}
			fmt.Printf("\n%d hit(s)\n", results.Total)
			return nil
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", defaultDBPath, "SQLite index path")
	cmd.Flags().StringSliceVar(&hermeticity, "hermeticity", nil, "filter by hermeticity class (repeatable; e.g., pure-starlark)")
	cmd.Flags().IntVar(&limit, "limit", 50, "max hits")
	return cmd
}

func newShowCmd() *cobra.Command {
	var dbPath string
	cmd := &cobra.Command{
		Use:   "show <module>@<version>",
		Short: "Print the full ModuleReport for a stored module-version",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, version, ok := splitModuleVersion(args[0])
			if !ok {
				return fmt.Errorf("expected <module>@<version>, got %q", args[0])
			}
			s, err := store.Open(cmd.Context(), dbPath)
			if err != nil {
				return err
			}
			defer s.Close()
			r, err := s.GetReport(cmd.Context(), name, version)
			if err != nil {
				return err
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(r)
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", defaultDBPath, "SQLite index path")
	return cmd
}
