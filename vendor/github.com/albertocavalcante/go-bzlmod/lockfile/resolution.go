package lockfile

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"slices"
)

// ModuleResolution represents a resolved module for lockfile generation.
type ModuleResolution struct {
	// Name is the module name.
	Name string

	// Version is the resolved version.
	Version string

	// RegistryURL is the registry that provided this module.
	RegistryURL string

	// ModuleFileContent is the raw MODULE.bazel content.
	ModuleFileContent []byte

	// IsYanked indicates if the module version was yanked.
	IsYanked bool

	// YankReason is the reason for yanking, if IsYanked is true.
	YankReason string
}

// FromResolution creates a lockfile from a set of resolved modules.
// This records the SHA256 hashes of all MODULE.bazel files that were fetched,
// enabling reproducible builds.
func FromResolution(modules []ModuleResolution) *Lockfile {
	lf := New()

	for _, m := range modules {
		if len(m.ModuleFileContent) == 0 {
			continue
		}

		// Build the registry URL for this module's MODULE.bazel
		url := buildModuleFileURL(m.RegistryURL, m.Name, m.Version)

		// Compute SHA256 hash of the content
		hash := computeSHA256(m.ModuleFileContent)
		lf.SetRegistryHash(url, hash)

		// Record yanked versions that were explicitly selected
		if m.IsYanked {
			lf.AllowYankedVersion(ModuleKey{Name: m.Name, Version: m.Version}, m.YankReason)
		}
	}

	return lf
}

// FromRegistryFileHashes creates a lockfile from an existing Bazel-style
// registry trace. Nil hash values are preserved as explicit "not found" entries.
func FromRegistryFileHashes(registryFileHashes map[string]*string) *Lockfile {
	lf := New()
	for url, hash := range registryFileHashes {
		if hash == nil {
			lf.SetMissingRegistryHash(url)
			continue
		}
		lf.SetRegistryHash(url, *hash)
	}
	return lf
}

// buildModuleFileURL constructs the full URL for a module's MODULE.bazel file.
func buildModuleFileURL(registryURL, moduleName, version string) string {
	// Normalize registry URL
	if registryURL == "" {
		registryURL = "https://bcr.bazel.build"
	}
	// Strip trailing slash if present (and URL is non-empty after default assignment)
	if len(registryURL) > 0 && registryURL[len(registryURL)-1] == '/' {
		registryURL = registryURL[:len(registryURL)-1]
	}

	return registryURL + "/modules/" + moduleName + "/" + version + "/MODULE.bazel"
}

// computeSHA256 computes the SHA256 hash of data and returns it as a hex string.
func computeSHA256(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// Diff compares two lockfiles and returns the differences.
// This is useful for determining if a lockfile needs updating.
type Diff struct {
	// Added contains URLs that are in the new lockfile but not the old.
	Added []string

	// Removed contains URLs that are in the old lockfile but not the new.
	Removed []string

	// Changed contains URLs where the hash differs between old and new.
	Changed []HashChange

	// YankedAdded contains yanked versions added in the new lockfile.
	YankedAdded []string

	// YankedRemoved contains yanked versions removed in the new lockfile.
	YankedRemoved []string
}

// HashChange represents a hash change for a URL.
type HashChange struct {
	URL     string
	OldHash *string
	NewHash *string
}

// IsEmpty returns true if there are no differences.
func (d *Diff) IsEmpty() bool {
	return len(d.Added) == 0 && len(d.Removed) == 0 && len(d.Changed) == 0 &&
		len(d.YankedAdded) == 0 && len(d.YankedRemoved) == 0
}

// Compare compares two lockfiles and returns the differences.
func Compare(old, new *Lockfile) *Diff {
	diff := &Diff{}

	// Compare registry file hashes
	oldURLs := make(map[string]*string)
	for url, hash := range old.RegistryFileHashes {
		oldURLs[url] = cloneStringPointer(hash)
	}

	for url, newHash := range new.RegistryFileHashes {
		if oldHash, exists := oldURLs[url]; exists {
			if !registryHashEqual(oldHash, newHash) {
				diff.Changed = append(diff.Changed, HashChange{
					URL:     url,
					OldHash: cloneStringPointer(oldHash),
					NewHash: cloneStringPointer(newHash),
				})
			}
			delete(oldURLs, url)
		} else {
			diff.Added = append(diff.Added, url)
		}
	}

	for url := range oldURLs {
		diff.Removed = append(diff.Removed, url)
	}

	// Compare yanked versions
	oldYanked := make(map[string]bool)
	for key := range old.SelectedYankedVersions {
		oldYanked[key] = true
	}

	for key := range new.SelectedYankedVersions {
		if oldYanked[key] {
			delete(oldYanked, key)
		} else {
			diff.YankedAdded = append(diff.YankedAdded, key)
		}
	}

	for key := range oldYanked {
		diff.YankedRemoved = append(diff.YankedRemoved, key)
	}

	// Sort for deterministic output
	slices.Sort(diff.Added)
	slices.Sort(diff.Removed)
	slices.Sort(diff.YankedAdded)
	slices.Sort(diff.YankedRemoved)
	slices.SortFunc(diff.Changed, func(a, b HashChange) int {
		return cmp.Compare(a.URL, b.URL)
	})

	return diff
}
