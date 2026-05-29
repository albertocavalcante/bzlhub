package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/albertocavalcante/assay/report"
	"github.com/albertocavalcante/canopy/internal/fetch"
	"github.com/albertocavalcante/canopy/internal/ingest"
	"github.com/albertocavalcante/canopy/internal/mirror"
	"github.com/albertocavalcante/canopy/internal/resolve"
	canopyscip "github.com/albertocavalcante/canopy/internal/scip"
	"github.com/albertocavalcante/canopy/internal/store"
)

func newIngestCmd() *cobra.Command {
	var (
		dbPath          string
		fromReg         string
		mirrorTo        string
		recursive       bool
		includeBzlTools bool
		bazelVersion    string
		workers         int
		nameOverride    string
		versionOverride string
	)
	cmd := &cobra.Command{
		Use:   "ingest <module-dir> | <module>@<version> --from <registry-url>",
		Short: "Analyze a Bazel module and index it (from local dir or a BCR-shape registry)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := store.Open(cmd.Context(), dbPath)
			if err != nil {
				return err
			}
			defer s.Close()

			arg := args[0]
			var r *report.ModuleReport

			if fromReg != "" {
				// Registry path: arg must be <module>@<version>.
				module, version, ok := splitModVer(arg)
				if !ok {
					return fmt.Errorf("with --from, arg must be <module>@<version>, got %q", arg)
				}

				// --recursive: walk the bazel_dep closure. The walker
				// mirrors as it goes but doesn't extract/assay each module
				// (that would be expensive for a large closure). The root
				// module still gets the full extract+assay treatment below.
				if recursive {
					var mw *mirror.Writer
					if mirrorTo != "" {
						mw, err = mirror.New(mirrorTo)
						if err != nil {
							return fmt.Errorf("init mirror %s: %w", mirrorTo, err)
						}
					}
					bvForTools := ""
					if includeBzlTools {
						bvForTools = bazelVersion
					}
					_, rerr := ingest.RecursiveFromRegistry(cmd.Context(), fromReg, module, version, ingest.RecursiveOptions{
						Mirror:               mw,
						BazelToolsForVersion: bvForTools,
						Workers:              workers,
						Reporter: func(ev ingest.RecursiveEvent) {
							switch ev.Kind {
							case "enter":
								fmt.Fprintf(os.Stderr, "  → %s@%s\n", ev.Module, ev.Version)
							case "done":
								fmt.Fprintf(os.Stderr, "    ✓ %s@%s (%s)\n", ev.Module, ev.Version, ev.Detail)
							case "skip":
								// quiet
							case "error":
								fmt.Fprintf(os.Stderr, "    ✗ %s@%s: %v\n", ev.Module, ev.Version, ev.Err)
							}
						},
					})
					if rerr != nil {
						return rerr
					}
					// Recursive walk doesn't return a single root report —
					// caller doesn't need one. Just print a closing line
					// and exit.
					fmt.Println("recursive ingest complete")
					return nil
				}

				// If mirroring, prepare the sink BEFORE fetching so the
				// tarball stream tees straight into it.
				var (
					mw   *mirror.Writer
					sink *mirror.BlobSink
				)
				opts := resolve.Options{}
				if mirrorTo != "" {
					mw, err = mirror.New(mirrorTo)
					if err != nil {
						return fmt.Errorf("init mirror %s: %w", mirrorTo, err)
					}
					if err := mw.EnsureRegistryJSON(); err != nil {
						return fmt.Errorf("write bazel_registry.json: %w", err)
					}
					// Probe source.json once to get the upstream URL so the
					// blob basename matches what mirror serves.
					srcProbe, perr := fetch.NewClient().GetSourceJSON(cmd.Context(), fromReg, module, version)
					if perr != nil {
						return fmt.Errorf("probe source.json: %w", perr)
					}
					sink, err = mw.BlobWriter(srcProbe.URL)
					if err != nil {
						return fmt.Errorf("open blob sink: %w", err)
					}
					opts.Tee = sink
					opts.CaptureBytes = true
				}

				m, err := resolve.FromRegistryWithClient(cmd.Context(), fetch.NewClient(), fromReg, module, version, opts)
				if err != nil {
					if sink != nil {
						sink.Abort()
					}
					return fmt.Errorf("resolve %s@%s from %s: %w", module, version, fromReg, err)
				}
				defer m.Cleanup()

				// Finalize the mirror only AFTER integrity verification
				// inside resolve has succeeded. At this point all bytes
				// flowing through the tee have been hash-checked.
				if mw != nil {
					if _, _, _, ferr := sink.Close(); ferr != nil {
						return fmt.Errorf("finalize mirror blob: %w", ferr)
					}
					if err := mw.WriteSource(module, version, m.SourceBytes); err != nil {
						return fmt.Errorf("mirror source.json: %w", err)
					}
					if len(m.ModuleBytes) > 0 {
						if err := mw.WriteModuleBazel(module, version, m.ModuleBytes); err != nil {
							return fmt.Errorf("mirror MODULE.bazel: %w", err)
						}
					}
					if err := mw.MergeMetadata(module, version); err != nil {
						return fmt.Errorf("mirror metadata.json: %w", err)
					}
				}

				r, err = ingest.Analyze(cmd.Context(), m.Dir)
				if err != nil {
					return err
				}
				// The tarball MODULE.bazel typically uses a placeholder
				// version (rules_python ships "0.0.0"); BCR's source.json
				// is the canonical coordinate.
				r.Name = module
				r.Version = version
				if err := s.WriteReport(cmd.Context(), r); err != nil {
					return fmt.Errorf("write report %s@%s: %w", module, version, err)
				}
				// Generate + persist a SCIP index alongside the
				// ModuleReport. Best-effort: failures get logged but
				// don't abort the ingest (the canonical report already
				// landed). Mirrors what canopy.Service.Bump does for
				// the API-driven path.
				if scipBlob, scipErr := canopyscip.Generate(m.Dir, module, version, r); scipErr == nil {
					if werr := s.WriteScipBlob(cmd.Context(), module, version, scipBlob); werr != nil {
						fmt.Fprintf(os.Stderr, "warn: scip blob write failed for %s@%s: %v\n", module, version, werr)
					}
				} else {
					fmt.Fprintf(os.Stderr, "warn: scip index generation failed for %s@%s: %v\n", module, version, scipErr)
				}
			} else {
				// Local-dir path.
				r, err = ingest.FromDir(cmd.Context(), s, arg)
				if err != nil {
					return err
				}
				if nameOverride != "" {
					r.Name = nameOverride
				}
				if versionOverride != "" {
					r.Version = versionOverride
				}
				// Same best-effort SCIP generation as the registry path.
				if scipBlob, scipErr := canopyscip.Generate(arg, r.Name, r.Version, r); scipErr == nil {
					if werr := s.WriteScipBlob(cmd.Context(), r.Name, r.Version, scipBlob); werr != nil {
						fmt.Fprintf(os.Stderr, "warn: scip blob write failed for %s@%s: %v\n", r.Name, r.Version, werr)
					}
				} else {
					fmt.Fprintf(os.Stderr, "warn: scip index generation failed for %s@%s: %v\n", r.Name, r.Version, scipErr)
				}
			}

			fmt.Printf("ingested %s@%s: %d rules, %d providers, %d macros, %d repo rules, hermeticity=%v\n",
				r.Name, r.Version,
				len(r.Rules), len(r.Providers), len(r.Macros), len(r.RepositoryRules),
				r.Hermeticity.Classes,
			)
			return nil
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", defaultDBPath, "SQLite index path")
	cmd.Flags().StringVar(&fromReg, "from", "", "BCR-shape registry URL to fetch the module from (e.g. http://localhost:8765)")
	cmd.Flags().StringVar(&mirrorTo, "mirror-to", "", "also write the fetched module + tarball into this BCR-shape directory (only with --from)")
	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "walk the bazel_dep closure (only with --from); skips assay extraction per-module (use --mirror-to to persist the mirror)")
	cmd.Flags().BoolVar(&includeBzlTools, "include-bazel-tools", false, "also seed the closure with Bazel's implicit MODULE.tools deps for --bazel-version; needed for a fully self-sufficient mirror")
	cmd.Flags().StringVar(&bazelVersion, "bazel-version", "9.1.0", "Bazel version to use when --include-bazel-tools is set (resolves to the closest supported version via go-bzlmod's bazeltools)")
	cmd.Flags().IntVar(&workers, "workers", 8, "concurrent upstream fetches during --recursive ingest")
	cmd.Flags().StringVar(&nameOverride, "name", "", "override module name (local-dir mode only)")
	cmd.Flags().StringVar(&versionOverride, "version", "", "override module version (local-dir mode only)")
	return cmd
}

// splitModVer parses "<module>@<version>".
func splitModVer(s string) (module, version string, ok bool) {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '@' {
			return s[:i], s[i+1:], i > 0 && i < len(s)-1
		}
	}
	return "", "", false
}
