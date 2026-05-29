package lockfile

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// MergeStrategy defines how to handle conflicts when merging lockfiles.
type MergeStrategy int

const (
	// MergePreferExisting keeps existing values on conflict.
	MergePreferExisting MergeStrategy = iota

	// MergePreferNew overwrites with new values on conflict.
	MergePreferNew

	// MergeErrorOnConflict returns an error if values differ.
	MergeErrorOnConflict
)

// MergeOptions configures lockfile merge behavior.
type MergeOptions struct {
	// Strategy determines how conflicts are resolved.
	Strategy MergeStrategy

	// VerifyHashes checks that overlapping hashes match.
	VerifyHashes bool
}

// DefaultMergeOptions returns sensible defaults for merging.
func DefaultMergeOptions() MergeOptions {
	return MergeOptions{
		Strategy:     MergePreferNew,
		VerifyHashes: true,
	}
}

// Merge combines another lockfile into this one.
// Registry hashes and yanked versions are merged according to the strategy.
// Module extensions are merged by extension ID.
func (l *Lockfile) Merge(other *Lockfile, opts MergeOptions) error {
	if other == nil {
		return nil
	}

	// Merge registry file hashes
	if err := l.mergeRegistryHashes(other, opts); err != nil {
		return fmt.Errorf("failed to merge registry hashes: %w", err)
	}

	// Merge yanked versions
	if err := l.mergeYankedVersions(other, opts); err != nil {
		return fmt.Errorf("failed to merge yanked versions: %w", err)
	}

	// Merge module extensions
	l.mergeExtensions(other, opts)

	// Merge facts
	l.mergeFacts(other, opts)

	return nil
}

func (l *Lockfile) mergeRegistryHashes(other *Lockfile, opts MergeOptions) error {
	for url, newHash := range other.RegistryFileHashes {
		existing, exists := l.RegistryFileHashes[url]
		if !exists {
			l.RegistryFileHashes[url] = cloneStringPointer(newHash)
			continue
		}

		if registryHashEqual(existing, newHash) {
			continue
		}

		// Conflict handling
		switch opts.Strategy {
		case MergePreferExisting:
			// Keep existing
		case MergePreferNew:
			l.RegistryFileHashes[url] = cloneStringPointer(newHash)
		case MergeErrorOnConflict:
			return fmt.Errorf("hash conflict for %s: existing=%s, new=%s", url, registryHashString(existing), registryHashString(newHash))
		}
	}
	return nil
}

func (l *Lockfile) mergeYankedVersions(other *Lockfile, opts MergeOptions) error {
	for key, newReason := range other.SelectedYankedVersions {
		existing, exists := l.SelectedYankedVersions[key]
		if !exists {
			l.SelectedYankedVersions[key] = newReason
			continue
		}

		if existing == newReason {
			continue
		}

		switch opts.Strategy {
		case MergePreferExisting:
			// Keep existing
		case MergePreferNew:
			l.SelectedYankedVersions[key] = newReason
		case MergeErrorOnConflict:
			return fmt.Errorf("yanked version conflict for %s: existing=%s, new=%s", key, existing, newReason)
		}
	}
	return nil
}

func (l *Lockfile) mergeExtensions(other *Lockfile, opts MergeOptions) {
	for extID, otherEntry := range other.ModuleExtensions {
		existing, exists := l.ModuleExtensions[extID]
		if !exists {
			l.ModuleExtensions[extID] = otherEntry
			continue
		}

		// Merge entries within the extension
		for factors, data := range otherEntry {
			_, factorsExist := existing[factors]
			if !factorsExist || opts.Strategy == MergePreferNew {
				existing[factors] = data
			}
		}
		l.ModuleExtensions[extID] = existing
	}
}

func (l *Lockfile) mergeFacts(other *Lockfile, opts MergeOptions) {
	for factID, otherFact := range other.Facts {
		_, exists := l.Facts[factID]
		if !exists || opts.Strategy == MergePreferNew {
			l.Facts[factID] = otherFact
		}
	}
}

// Diff returns the differences between two lockfiles.
func (l *Lockfile) Diff(other *Lockfile) *LockfileDiff {
	diff := &LockfileDiff{
		AddedHashes:   make(map[string]*string),
		RemovedHashes: make(map[string]*string),
		ChangedHashes: make(map[string][2]*string),
	}

	// Compare registry hashes
	for url, hash := range other.RegistryFileHashes {
		existing, exists := l.RegistryFileHashes[url]
		if !exists {
			diff.AddedHashes[url] = cloneStringPointer(hash)
		} else if !registryHashEqual(existing, hash) {
			diff.ChangedHashes[url] = [2]*string{cloneStringPointer(existing), cloneStringPointer(hash)}
		}
	}
	for url, hash := range l.RegistryFileHashes {
		if _, exists := other.RegistryFileHashes[url]; !exists {
			diff.RemovedHashes[url] = cloneStringPointer(hash)
		}
	}

	// Compare version
	if l.Version != other.Version {
		diff.VersionChanged = true
		diff.OldVersion = l.Version
		diff.NewVersion = other.Version
	}

	return diff
}

// LockfileDiff describes differences between two lockfiles.
type LockfileDiff struct {
	VersionChanged bool
	OldVersion     int
	NewVersion     int

	AddedHashes   map[string]*string
	RemovedHashes map[string]*string
	ChangedHashes map[string][2]*string // [old, new]
}

// IsEmpty returns true if there are no differences.
func (d *LockfileDiff) IsEmpty() bool {
	return !d.VersionChanged &&
		len(d.AddedHashes) == 0 &&
		len(d.RemovedHashes) == 0 &&
		len(d.ChangedHashes) == 0
}

// Summary returns a human-readable summary of the differences.
func (d *LockfileDiff) Summary() string {
	if d.IsEmpty() {
		return "no changes"
	}

	var result string
	if d.VersionChanged {
		result += fmt.Sprintf("version: %d -> %d\n", d.OldVersion, d.NewVersion)
	}
	if len(d.AddedHashes) > 0 {
		result += fmt.Sprintf("added: %d registry hashes\n", len(d.AddedHashes))
	}
	if len(d.RemovedHashes) > 0 {
		result += fmt.Sprintf("removed: %d registry hashes\n", len(d.RemovedHashes))
	}
	if len(d.ChangedHashes) > 0 {
		result += fmt.Sprintf("changed: %d registry hashes\n", len(d.ChangedHashes))
	}
	return result
}

// HashContent computes a SHA256 hash of content for use in lockfiles.
func HashContent(content []byte) string {
	hash := sha256.Sum256(content)
	return hex.EncodeToString(hash[:])
}

func registryHashEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func registryHashString(hash *string) string {
	if hash == nil {
		return "<missing>"
	}
	return *hash
}

// VerifyHash checks if content matches the expected hash.
func VerifyHash(content []byte, expectedHash string) bool {
	return HashContent(content) == expectedHash
}
