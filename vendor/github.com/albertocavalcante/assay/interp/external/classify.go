package external

import (
	"strings"

	"github.com/albertocavalcante/starlark-go-bazel/taint"
)

// Class constants for the host taxonomy (canopy plan 11). Stable
// strings consumers can pattern-match against. Add new classes via
// classifyHost; never rename existing ones (canopy SQLite schema
// stores them).
const (
	ClassBCR           = "bcr"
	ClassMaven         = "maven"
	ClassPyPICanonical = "pypi-canonical"
	ClassPyPIExtra     = "pypi-extra"
	ClassNPM           = "npm"
	ClassGoProxy       = "go-proxy"
	ClassGitHubRelease = "github-release"
	ClassGitHubArchive = "github-archive"
	ClassGitHubOther   = "github-other"
	ClassGitLabRelease = "gitlab-release"
	ClassGitLabArchive = "gitlab-archive"
	ClassGitLabOther   = "gitlab-other"
	ClassOCI           = "oci"
	ClassCloudStorage  = "cloud-storage"
	ClassVendorHTTP    = "vendor-http"
	ClassUnknown       = "unknown"
)

// Mutability constants.
const (
	MutabilityImmutable   = "immutable"    // hash-pinned or OCI digest
	MutabilityMutableHost = "mutable-host" // host serves mutable content (e.g. github-archive of a branch)
	MutabilityUnknown     = "unknown"      // taint or insufficient info
)

// classifyHost maps a (lowercased host, full URL) pair to one of the
// classes above. Falls through to ClassUnknown — that's a UI signal
// for "the airgap mirror will need a manual rule for this host."
func classifyHost(host, fullURL string) string {
	h := strings.ToLower(host)
	urlLower := strings.ToLower(fullURL)

	switch {
	case h == "bcr.bazel.build":
		return ClassBCR
	case h == "repo1.maven.org", h == "repo.maven.apache.org", h == "maven.google.com":
		return ClassMaven
	case strings.Contains(h, ".jfrog.io") && strings.Contains(urlLower, "/maven/"):
		return ClassMaven
	case strings.Contains(h, "nexus") && strings.Contains(urlLower, "/maven"):
		return ClassMaven
	case h == "pypi.org", h == "files.pythonhosted.org":
		return ClassPyPICanonical
	case strings.HasSuffix(urlLower, ".whl"),
		strings.Contains(urlLower, ".tar.gz") && strings.Contains(h, "pytorch"),
		strings.Contains(h, "fbaipublicfiles") || strings.Contains(h, "data.pyg.org"):
		return ClassPyPIExtra
	case h == "registry.npmjs.org", strings.HasSuffix(h, ".npmmirror.com"):
		return ClassNPM
	case h == "proxy.golang.org", h == "sum.golang.org", strings.HasPrefix(h, "goproxy."):
		return ClassGoProxy
	case h == "github.com", h == "codeload.github.com":
		switch {
		case strings.Contains(urlLower, "/releases/download/"):
			return ClassGitHubRelease
		case strings.Contains(urlLower, "/archive/"):
			return ClassGitHubArchive
		default:
			return ClassGitHubOther
		}
	case h == "gitlab.com":
		switch {
		case strings.Contains(urlLower, "/-/releases/"),
			strings.Contains(urlLower, "/-/package_files/"):
			return ClassGitLabRelease
		case strings.Contains(urlLower, "/-/archive/"):
			return ClassGitLabArchive
		default:
			return ClassGitLabOther
		}
	case isOCIHost(h):
		return ClassOCI
	case isCloudStorageHost(h):
		return ClassCloudStorage
	case isKnownVendorHost(h):
		return ClassVendorHTTP
	}

	return ClassUnknown
}

func isOCIHost(h string) bool {
	return h == "docker.io" || strings.HasSuffix(h, ".docker.io") ||
		h == "gcr.io" || h == "ghcr.io" || h == "quay.io" ||
		h == "public.ecr.aws" || strings.HasSuffix(h, ".dkr.ecr.aws.amazonaws.com") ||
		strings.HasSuffix(h, ".azurecr.io") || strings.HasPrefix(h, "registry-")
}

func isCloudStorageHost(h string) bool {
	return h == "storage.googleapis.com" ||
		strings.HasSuffix(h, ".s3.amazonaws.com") ||
		strings.HasSuffix(h, ".r2.dev") ||
		strings.HasSuffix(h, ".r2.cloudflarestorage.com") ||
		strings.HasSuffix(h, ".blob.core.windows.net")
}

func isKnownVendorHost(h string) bool {
	switch h {
	case "download.eclipse.org", "archive.apache.org", "nodejs.org",
		"dl.k8s.io", "releases.hashicorp.com", "dl.google.com",
		"go.dev", "ftp.gnu.org", "static.crates.io", "crates.io":
		return true
	}
	return false
}

// classifyMutability decides if a URL points at immutable content.
// Rules:
//   - Tainted → unknown (we don't even know the real URL).
//   - SHA256 or Integrity pinned → immutable.
//   - github-archive / gitlab-archive of a branch ref → mutable-host.
//   - Otherwise unknown (conservative; airgap mirror must verify).
func classifyMutability(u taint.CapturedURL, host, fullURL string) string {
	if u.Tainted {
		return MutabilityUnknown
	}
	if u.SHA256 != "" || u.Integrity != "" {
		return MutabilityImmutable
	}
	switch classifyHost(host, fullURL) {
	case ClassGitHubArchive, ClassGitLabArchive:
		return MutabilityMutableHost
	}
	return MutabilityUnknown
}
