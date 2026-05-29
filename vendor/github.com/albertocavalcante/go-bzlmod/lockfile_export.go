package gobzlmod

import lockpkg "github.com/albertocavalcante/go-bzlmod/lockfile"

// ToLockfile converts a resolution result into a lockfile-compatible snapshot.
// It preserves Bazel-style registryFileHashes entries, including explicit nil
// "not found" markers, and records selected yanked versions.
//
// To populate registryFileHashes and Source metadata before calling this
// method, resolve with WithRegistryTrace().
func (r *ResolutionList) ToLockfile() *lockpkg.Lockfile {
	lf := lockpkg.FromRegistryFileHashes(nil)
	if r == nil {
		return lf
	}

	lf = lockpkg.FromRegistryFileHashes(r.RegistryFileHashes)
	for _, module := range r.Modules {
		if !module.Yanked {
			continue
		}
		lf.AllowYankedVersion(lockpkg.ModuleKey{Name: module.Name, Version: module.Version}, module.YankReason)
	}

	return lf
}
