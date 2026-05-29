// Package lockfile provides types and operations for Bazel's MODULE.bazel.lock file.
//
// The lockfile captures the resolved state of module dependencies, ensuring
// reproducible builds across machines and time. This package implements
// Bazel's lockfile format (version 26+) for reading, writing, and manipulation.
//
// # Lockfile Structure
//
// A Bazel lockfile contains:
//   - lockFileVersion: Schema version for format compatibility
//   - registryFileHashes: Integrity hashes for registry files fetched
//   - selectedYankedVersions: Explicitly allowed yanked versions
//   - moduleExtensions: Cached results of module extension evaluations
//   - facts: Additional facts about module extensions
//
// registryFileHashes values are raw SHA-256 hex digests. A null value means a
// registry file URL was probed but not found, which Bazel uses to record
// fallback misses in higher-priority registries.
//
// # Usage
//
// Read an existing lockfile:
//
//	lf, err := lockfile.ReadFile("MODULE.bazel.lock")
//	if err != nil {
//	    log.Fatal(err)
//	}
//	fmt.Printf("Lockfile version: %d\n", lf.Version)
//
// Create a new lockfile:
//
//	lf := lockfile.New()
//	lf.SetRegistryHash("https://bcr.bazel.build/modules/rules_go/0.50.1/MODULE.bazel", hash)
//	if err := lf.WriteFile("MODULE.bazel.lock"); err != nil {
//	    log.Fatal(err)
//	}
//
// Create a lockfile from an existing registry trace:
//
//	trace := map[string]*string{
//	    "https://bcr.bazel.build/modules/rules_go/0.50.1/MODULE.bazel": &hash,
//	    "https://bcr.bazel.build/modules/missing/1.0.0/MODULE.bazel":   nil,
//	}
//	lf := lockfile.FromRegistryFileHashes(trace)
//
// # Compatibility
//
// This package targets lockfile version 26 (Bazel 7.x/8.x). Older versions
// may have different schemas and are not fully supported.
package lockfile
