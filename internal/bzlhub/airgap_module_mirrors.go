package bzlhub

import (
	"context"
	"fmt"
	"strings"

	"github.com/albertocavalcante/bzlhub/internal/api"
)

// defaultRegistry is the Bazel Central Registry — canopy's default
// assumption for modules whose ingestion source wasn't a custom
// registry override. Used as the LHS of the per-registry
// --module_mirrors form (Bazel >= 8.5).
const defaultRegistry = "https://bcr.bazel.build/"

// AirgapModuleMirrors renders the .bazelrc snippet for the
// --module_mirrors flag. Sibling to AirgapDownloaderConfig; see
// internal/api.ModuleMirrors and the airgap_config.go doc for the
// trade-off between the two artifacts.
//
// (module, version) is currently only used for naming the rendered
// artifact — the flag itself is registry-scoped. Future work could
// scope per-module if canopy starts tracking each module's source
// registry.
func (s *Service) AirgapModuleMirrors(ctx context.Context, name, version string, opts api.ModuleMirrorsOptions) (*api.ModuleMirrors, error) {
	// Refuse to template a snippet citing a module canopy has never
	// indexed — a typo in the URL would otherwise produce a happy 200
	// with a misleading artifact.
	exists, err := s.store.VersionExists(ctx, name, version)
	if err != nil {
		return nil, fmt.Errorf("version exists: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("%s@%s: not found", name, version)
	}

	mirror, err := validateBazelrcURL("mirror", opts.MirrorBase)
	if err != nil {
		return nil, err
	}
	if mirror == "" {
		mirror = "http://mirror.internal/"
	}
	if !strings.HasSuffix(mirror, "/") {
		mirror += "/"
	}

	registry, err := validateBazelrcURL("registry", opts.Registry)
	if err != nil {
		return nil, err
	}
	if registry == "" {
		registry = defaultRegistry
	}

	var out strings.Builder
	fmt.Fprintf(&out, "# canopy airgap --module_mirrors snippet\n")
	fmt.Fprintf(&out, "# Module:      %s@%s\n", name, version)
	fmt.Fprintf(&out, "# Mirror:      %s\n", mirror)
	fmt.Fprintf(&out, "# Registry:    %s\n", registry)
	fmt.Fprintf(&out, "#\n")
	fmt.Fprintf(&out, "# --module_mirrors covers source URLs provided by modules\n")
	fmt.Fprintf(&out, "# obtained from a Bazel registry. It does NOT cover URLs\n")
	fmt.Fprintf(&out, "# fetched by repo_rule / module_extension calls — for those,\n")
	fmt.Fprintf(&out, "# pair this with --downloader_config (see canopy's\n")
	fmt.Fprintf(&out, "# airgap-downloader-config endpoint, which is the superset).\n")
	fmt.Fprintf(&out, "#\n")
	fmt.Fprintf(&out, "# Bazel version compatibility:\n")
	fmt.Fprintf(&out, "#   >= 8.5:  per-registry syntax (preferred — the line below).\n")
	fmt.Fprintf(&out, "#   >= 8.4:  unscoped syntax — drop the \"<registry>=\" prefix:\n")
	fmt.Fprintf(&out, "#             common --module_mirrors=%s\n", mirror)
	fmt.Fprintf(&out, "#   <  8.4:  flag is not available; use --downloader_config instead.\n")
	fmt.Fprintf(&out, "\n")
	fmt.Fprintf(&out, "common --module_mirrors=%s=%s\n", registry, mirror)

	return &api.ModuleMirrors{
		Module:     name,
		Version:    version,
		MirrorBase: mirror,
		Registry:   registry,
		Text:       out.String(),
	}, nil
}
