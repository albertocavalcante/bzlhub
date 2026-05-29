package registry

import "github.com/albertocavalcante/go-bzlmod/internal/compat"

// Metadata represents the metadata.json file for a module in the registry.
// This matches the BCR metadata.schema.json specification.
type Metadata struct {
	// Homepage is the URL to the project's homepage.
	Homepage string `json:"homepage"`

	// Maintainers lists individuals who can be notified about the module.
	Maintainers []Maintainer `json:"maintainers"`

	// Repository is an allowlist of source URLs.
	// Format: "github:org/repo" or a URL prefix like "https://example.com/".
	Repository []string `json:"repository"`

	// Versions lists all available versions in the registry.
	Versions []string `json:"versions"`

	// YankedVersions maps version strings to yank reasons.
	// Yanked versions should not be used in new projects.
	YankedVersions map[string]string `json:"yanked_versions,omitempty"`

	// Deprecated explains why the module should not be used.
	// If set, the latest version may be yanked.
	Deprecated string `json:"deprecated,omitempty"`
}

// Maintainer represents a module maintainer in metadata.json.
type Maintainer struct {
	// GitHub is the maintainer's GitHub username.
	GitHub string `json:"github,omitempty"`

	// GitHubUserID is the maintainer's numeric GitHub user ID.
	// Used to verify identity across username changes.
	GitHubUserID int64 `json:"github_user_id,omitempty"`

	// Email is the maintainer's email address (informational only).
	Email string `json:"email,omitempty"`

	// Name is the maintainer's display name (informational only).
	Name string `json:"name,omitempty"`

	// DoNotNotify prevents @-mentions in PRs while preserving approval rights.
	DoNotNotify bool `json:"do_not_notify,omitempty"`
}

// Source represents the source.json file specifying how to fetch module source.
// The Type field determines which other fields are relevant.
//
// Reference: IndexRegistry.java lines 264-295
// See: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/IndexRegistry.java
type Source struct {
	// Type is the source type: "archive" (default), "git_repository", or "local_path".
	Type string `json:"type,omitempty"`

	// --- Archive fields (Type == "" or "archive") ---

	// URL is the download URL for the archive.
	URL string `json:"url,omitempty"`

	// Integrity is the SRI hash (e.g., "sha256-...") for the archive.
	Integrity string `json:"integrity,omitempty"`

	// StripPrefix is the directory prefix to strip from the archive.
	StripPrefix string `json:"strip_prefix,omitempty"`

	// Patches lists patch files to apply after extraction.
	Patches map[string]string `json:"patches,omitempty"`

	// PatchStrip is the number of leading path components to strip from patch paths.
	PatchStrip int `json:"patch_strip,omitempty"`

	// Overlay maps destination paths to source paths for overlay files.
	Overlay map[string]string `json:"overlay,omitempty"`

	// MirrorURLs lists backup URLs for source archive.
	// These are tried in order if the primary URL fails.
	// Requires Bazel 7.7.0+.
	//
	// Reference: https://github.com/bazelbuild/bazel/issues/17829
	MirrorURLs []string `json:"mirror_urls,omitempty"`

	// --- Git repository fields (Type == "git_repository") ---

	// Remote is the Git repository URL.
	Remote string `json:"remote,omitempty"`

	// Commit is the Git commit hash to checkout.
	Commit string `json:"commit,omitempty"`

	// ShallowSince enables shallow clone from this date (YYYY-MM-DD).
	ShallowSince string `json:"shallow_since,omitempty"`

	// Tag is the Git tag to checkout (alternative to Commit).
	Tag string `json:"tag,omitempty"`

	// InitSubmodules enables recursive submodule initialization.
	InitSubmodules bool `json:"init_submodules,omitempty"`

	// VerboseVersion enables verbose version output during fetch.
	VerboseVersion bool `json:"verbose_version,omitempty"`

	// StripPrefixGit is the directory prefix to strip (for git_repository).
	// Note: JSON field is same as StripPrefix, handled by Type context.

	// --- Local path fields (Type == "local_path") ---

	// Path is the local filesystem path for local_path sources.
	// This path is relative to the registry root or absolute.
	Path string `json:"path,omitempty"`

	// --- Common optional fields ---

	// DocsURL points to documentation for the module.
	DocsURL string `json:"docs_url,omitempty"`
}

// RegistryConfig represents the bazel_registry.json file at the registry root.
type RegistryConfig struct {
	// Mirrors lists alternative URLs to try when the primary URL fails.
	Mirrors []string `json:"mirrors,omitempty"`

	// ModuleBasePath is the path prefix for module files within the registry.
	// Default is "modules" if not specified.
	ModuleBasePath string `json:"module_base_path,omitempty"`
}

// IsArchive returns true if this source is an archive type.
func (s *Source) IsArchive() bool {
	return s.Type == "" || s.Type == "archive"
}

// IsGitRepository returns true if this source is a git_repository type.
func (s *Source) IsGitRepository() bool {
	return s.Type == "git_repository"
}

// IsLocalPath returns true if this source is a local_path type.
func (s *Source) IsLocalPath() bool {
	return s.Type == "local_path"
}

// ValidateForBazelVersion checks if all fields used in this Source are supported
// by the specified Bazel version. Returns a list of warning messages for any
// unsupported fields.
//
// Returns nil if bazelVersion is empty or all fields are supported.
func (s *Source) ValidateForBazelVersion(bazelVersion string) []string {
	if bazelVersion == "" {
		return nil
	}

	var warnings []string

	// Check mirror_urls (requires Bazel 7.7.0+)
	if len(s.MirrorURLs) > 0 {
		if w := compat.CheckField(bazelVersion, "mirror_urls"); w != nil {
			warnings = append(warnings, w.String())
		}
	}

	return warnings
}

// LatestVersion returns the most recent version from the metadata.
// Returns empty string if no versions are available.
func (m *Metadata) LatestVersion() string {
	if len(m.Versions) == 0 {
		return ""
	}
	return m.Versions[len(m.Versions)-1]
}

// IsYanked returns true if the given version is yanked.
func (m *Metadata) IsYanked(version string) bool {
	_, ok := m.YankedVersions[version]
	return ok
}

// YankReason returns the reason why a version was yanked.
// Returns empty string if not yanked.
func (m *Metadata) YankReason(version string) string {
	return m.YankedVersions[version]
}

// IsDeprecated returns true if the module is deprecated.
func (m *Metadata) IsDeprecated() bool {
	return m.Deprecated != ""
}

// HasVersion returns true if the given version exists in the registry.
func (m *Metadata) HasVersion(version string) bool {
	for _, v := range m.Versions {
		if v == version {
			return true
		}
	}
	return false
}

// NonYankedVersions returns all versions that are not yanked, in order.
func (m *Metadata) NonYankedVersions() []string {
	result := make([]string, 0, len(m.Versions))
	for _, v := range m.Versions {
		if !m.IsYanked(v) {
			result = append(result, v)
		}
	}
	return result
}
