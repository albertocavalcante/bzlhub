package external

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/albertocavalcante/go-bzlmod/lockfile"

	"github.com/albertocavalcante/starlark-go-bazel/taint"
)

// minSupportedLockfileVersion is the oldest schema version we know how
// to read. Pre-v11 lockfiles use a meaningfully different
// generatedRepoSpecs shape; trying to parse them silently could miss
// attributes we need for URL extraction. Skip with a warning rather
// than mis-parse.
const minSupportedLockfileVersion = 11

// LockfileResult is what readModuleLockfile returns: the parsed
// version + the captured refs. Version=0 + nil refs means no
// lockfile was present.
type LockfileResult struct {
	Version int
	Refs    []Ref
	// Warning is non-empty when the lockfile version isn't in
	// go-bzlmod's KnownLockfileVersions() list — we parse best-effort
	// and warn so callers can surface it.
	Warning string
}

// readModuleLockfile reads workspaceRoot/MODULE.bazel.lock if present
// and emits one Ref per URL discovered in resolved repository specs.
// Refs from the lockfile carry File="MODULE.bazel.lock", Tainted=false
// (lockfile is the deterministic source of truth) — orthogonal to the
// interpreted-eval refs.
//
// Uses go-bzlmod/lockfile for parsing — that package handles every
// known schema version (1, 3, 6, 11, 13, 16, 18, 24, 26) including
// the v13/v14-vs-v18+ RepoRuleID normalization. We restrict actual
// URL extraction to v11+ regardless, since pre-v11 schemas predate
// the stable generatedRepoSpecs.attributes shape.
//
// Returns nil result + nil error when no lockfile is present (the
// common case for BCR producer-ruleset tarballs which usually don't
// ship a lockfile).
func readModuleLockfile(workspaceRoot string) (*LockfileResult, error) {
	path := filepath.Join(workspaceRoot, "MODULE.bazel.lock")
	lf, err := lockfile.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read MODULE.bazel.lock: %w", err)
	}
	if lf.Version < minSupportedLockfileVersion {
		return &LockfileResult{
			Version: lf.Version,
			Warning: fmt.Sprintf("lockfile version %d is older than minimum supported %d; skipped", lf.Version, minSupportedLockfileVersion),
		}, nil
	}

	result := &LockfileResult{Version: lf.Version}
	if info := lockfile.GetVersionInfo(lf.Version); info == nil {
		known := lockfile.KnownLockfileVersions()
		result.Warning = fmt.Sprintf("lockfile version %d not in go-bzlmod's known set %v; parsed best-effort", lf.Version, known)
	}

	for _, entry := range lf.ModuleExtensions {
		// Sort scope keys for deterministic output.
		scopes := make([]string, 0, len(entry))
		for k := range entry {
			scopes = append(scopes, k)
		}
		sort.Strings(scopes)
		for _, scope := range scopes {
			gen := entry[scope]
			// Sort repo names for deterministic output.
			repoNames := make([]string, 0, len(gen.GeneratedRepoSpecs))
			for k := range gen.GeneratedRepoSpecs {
				repoNames = append(repoNames, k)
			}
			sort.Strings(repoNames)
			for _, repoName := range repoNames {
				spec := gen.GeneratedRepoSpecs[repoName]
				urls := lockfileURLs(spec.Attributes)
				if len(urls) == 0 {
					continue
				}
				sha256, _ := spec.Attributes["sha256"].(string)
				integrity, _ := spec.Attributes["integrity"].(string)
				stripPrefix, _ := spec.Attributes["strip_prefix"].(string)
				platform := lockfilePlatform(spec.Attributes, scope)
				ruleName := lockfileRuleName(spec.RepoRuleID)
				for _, u := range urls {
					host := extractHost(u)
					capt := taint.CapturedURL{
						URL:         u,
						SHA256:      sha256,
						Integrity:   integrity,
						StripPrefix: stripPrefix,
					}
					result.Refs = append(result.Refs, Ref{
						URL:        u,
						Host:       host,
						Class:      classifyHost(host, u),
						Mutability: classifyMutability(capt, host, u),
						SHA256:     sha256,
						Integrity:  integrity,
						APIName:    "lockfile",
						RuleName:   ruleName,
						Platform:   platform,
						Tainted:    false,
						File:       "MODULE.bazel.lock",
					})
				}
			}
		}
	}
	return result, nil
}

// lockfileURLs handles both `urls = [...]` and `url = "..."` attr forms.
// `urls` is the list form (typical http_archive), `url` is the singleton.
func lockfileURLs(attrs map[string]any) []string {
	if raw, ok := attrs["urls"]; ok {
		switch v := raw.(type) {
		case []any:
			out := make([]string, 0, len(v))
			for _, item := range v {
				if s, ok := item.(string); ok && s != "" {
					out = append(out, s)
				}
			}
			return out
		case string:
			if v != "" {
				return []string{v}
			}
		}
	}
	if s, ok := attrs["url"].(string); ok && s != "" {
		return []string{s}
	}
	return nil
}

// lockfilePlatform derives a Platform string from the lockfile entry.
// Two paths:
//   - scope key encodes the platform (e.g., "linux_amd64") when the
//     extension was os_dependent / arch_dependent.
//   - attributes.platform attr on the repo rule (e.g., rules_python's
//     `platform = "aarch64-apple-darwin"`).
//
// Falls back to "any" when neither applies.
func lockfilePlatform(attrs map[string]any, scope string) string {
	if scope != "" && scope != "general" {
		return scope
	}
	if p, ok := attrs["platform"].(string); ok && p != "" {
		return p
	}
	return "any"
}

// lockfileRuleName extracts the rule name from a repoRuleId of the
// form "@@<repo>~//<path>:<file>.bzl%<rule_name>".
func lockfileRuleName(repoRuleID string) string {
	if i := strings.LastIndex(repoRuleID, "%"); i >= 0 {
		return repoRuleID[i+1:]
	}
	return repoRuleID
}

