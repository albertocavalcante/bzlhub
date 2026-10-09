package main

import (
	"context"
	"fmt"
	"github.com/albertocavalcante/bzlhub/internal/backend"
	bcrmirror "github.com/albertocavalcante/go-bcr-mirror"
	"io"
	"os"
	"strings"

	"github.com/albertocavalcante/scip-bazel/pkg/bzlmod"
	"github.com/albertocavalcante/understory/pkg/understory"
	"github.com/spf13/cobra"

	"github.com/albertocavalcante/assay/report"
	"github.com/albertocavalcante/bzlhub/internal/fetch"
	"github.com/albertocavalcante/bzlhub/internal/ingest"
	"github.com/albertocavalcante/bzlhub/internal/mirror"
	"github.com/albertocavalcante/bzlhub/internal/resolve"
	bzlhubscip "github.com/albertocavalcante/bzlhub/internal/scip"
	"github.com/albertocavalcante/bzlhub/internal/store"
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

			// Where the SCIP closure gets its dependency answers: the store
			// first (an ingested report is already parsed), then the mirror if
			// one is being written. The mirror is what makes ingest ORDER
			// irrelevant -- a dependency's MODULE.bazel does not care whether
			// bzlhub has ingested it, and a --recursive run has already written
			// every MODULE.bazel there. Both are local reads, so indexing adds
			// no outbound traffic.
			scipDeps := bzlhubscip.DepsFunc(bzlhubscip.DepsFromStore(s))
			if mirrorTo != "" {
				scipDeps = bzlhubscip.FirstOf(scipDeps,
					bzlhubscip.DepsFromModuleBazel(backend.NewBCRMirror(bcrmirror.New(mirrorTo, ""))))
			}

			arg := args[0]
			var r *report.ModuleReport

			if fromReg != "" {
				// Registry path: arg must be <module>@<version>.
				module, version, ok := splitModVer(arg)
				if !ok {
					return fmt.Errorf("with --from, arg must be <module>@<version>, got %q", arg)
				}

				// --recursive: walk the bazel_dep closure. The walker only
				// MIRRORS the dependencies (it does not extract/assay them --
				// that would be expensive for a large closure). The root
				// module is then fully indexed by the steps below (resolve,
				// analyze, report, SCIP), exactly as in a non-recursive run;
				// those steps fetch the root's source themselves, so they do
				// not depend on the mirror the walk just populated.
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
					// Fall through: the root still needs its report and index.
					fmt.Println("recursive mirror walk complete")
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
				// landed). Mirrors what bzlhub.Service.Bump does for
				// the API-driven path.
				// Transitive closure: read from the store, then from the mirror
				// (populated by the recursive walk when --mirror-to is set), so
				// a load() reaching a dep-of-a-dep resolves.
				scipClosure, closureErr := bzlhubscip.TransitiveClosure(cmd.Context(), scipDeps, r)
				if closureErr != nil {
					fmt.Fprintf(os.Stderr, "warn: scip closure walk failed for %s@%s, using direct deps: %v\n",
						module, version, closureErr)
					scipClosure = bzlhubscip.DirectClosure(r)
				}
				if err := indexModule(cmd.Context(), s, m.Dir, module, version, scipClosure, os.Stderr); err != nil {
					return err
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
				// Local-dir path: the tree has no registry coordinate of its own,
				// but its bazel_deps still name real modules, so the walk
				// resolves whatever the store already holds.
				scipClosure, closureErr := bzlhubscip.TransitiveClosure(cmd.Context(), scipDeps, r)
				if closureErr != nil {
					fmt.Fprintf(os.Stderr, "warn: scip closure walk failed for %s@%s, using direct deps: %v\n",
						r.Name, r.Version, closureErr)
					scipClosure = bzlhubscip.DirectClosure(r)
				}
				if err := indexModule(cmd.Context(), s, arg, r.Name, r.Version, scipClosure, os.Stderr); err != nil {
					return err
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

// indexModule generates the SCIP index for the module at dir, persists it, and
// keeps the cached has_source_index flag in step (the flag decides whether the
// UI offers code navigation at all, so a stored blob without it is invisible).
// It follows Service.generateAndStoreScip: generation, blob-write and flag
// failures are best-effort warnings (the canonical report already landed), but
// an index that cannot be read back is an error. Repos the index could not
// resolve are reported on warn as a single line.
func indexModule(ctx context.Context, s *store.Store, dir, module, version string, closure bzlmod.Closure, warn io.Writer) error {
	blob, err := bzlhubscip.Generate(dir, module, version, closure)
	if err != nil {
		fmt.Fprintf(warn, "warn: scip index generation failed for %s@%s: %v\n", module, version, err)
		return nil
	}
	unresolved, err := bzlhubscip.UnresolvedRepos(blob)
	if err != nil {
		return fmt.Errorf("inspect generated index: %w", err)
	}
	if len(unresolved) > 0 {
		fmt.Fprintf(warn, "warn: %s@%s has unresolved references to repos: %s\n",
			module, version, strings.Join(unresolved, ", "))
	}
	if err := s.WriteScipBlob(ctx, module, version, blob); err != nil {
		fmt.Fprintf(warn, "warn: scip blob write failed for %s@%s: %v\n", module, version, err)
		return nil
	}
	if err := s.SetHasSourceIndex(ctx, module, version, blobHasFiles(blob)); err != nil {
		fmt.Fprintf(warn, "warn: set has_source_index failed for %s@%s: %v\n", module, version, err)
	}
	return nil
}

// blobHasFiles reports whether a SCIP blob indexes at least one file; same
// semantics as the unexported scipBlobHasFiles in internal/bzlhub.
func blobHasFiles(blob []byte) bool {
	idx, err := understory.OpenBytes(blob)
	if err != nil {
		return false
	}
	return len(idx.Files()) > 0
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
