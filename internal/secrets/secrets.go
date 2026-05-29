// Package secrets reads sensitive values from files at runtime
// rather than from env vars directly.
//
// Convention: env vars *point at* file paths via the
// `<NAME>_FILE` suffix; the file's contents are the secret.
// Falls back to the literal `<NAME>` env value when no `_FILE` is
// set — useful for personal quick-start installs, not corporate.
//
// Why files over env vars for secrets:
//
//  1. File mounts compose cleanly with k8s Secrets, docker secrets,
//     bind-mounted host files. Env-var injection is the second-class
//     citizen on those platforms.
//  2. Rotation: replace the file, SIGHUP. No restart, no downtime.
//     Env vars can't be rotated without restarting the process.
//  3. Logging: even verbose canopy logs never include file *contents*
//     — only file *paths*. Env values can leak via `env`,
//     `docker inspect`, /proc/<pid>/environ, etc.
//  4. Backups: env config can be in version control. Secret files
//     never are.
//
// Documented in docs/plans/08-corporate-security.md.
package secrets

import (
	"log/slog"
	"os"
	"strings"
)

// Read returns the trimmed contents of the file pointed at by
// $<envName>_FILE, falling back to $<envName> as a literal value
// when no _FILE is set. Returns "" when neither is set or the
// file is unreadable.
//
// Pattern:
//
//	GITHUB_TOKEN_FILE=/run/secrets/github-token  (preferred, corporate)
//	GITHUB_TOKEN=ghp_XXX                          (fallback, personal)
//
// The function never returns the path or any partial state — only
// the secret value or empty string. A read failure (file missing,
// permissions, etc.) is logged at WARN with the env name + path so
// the operator can debug "why is my token empty?" without us leaking
// the file contents.
func Read(envName string) string {
	if path := os.Getenv(envName + "_FILE"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			slog.Warn("secret file unreadable; treating as empty",
				"env", envName+"_FILE", "path", path, "err", err)
			return ""
		}
		return strings.TrimSpace(string(b))
	}
	return strings.TrimSpace(os.Getenv(envName))
}

// LazyRead returns a closure that re-reads the secret on every call.
// Used by long-running consumers (token-refresh loops, scheduled
// jobs) that want to pick up rotated secrets without restarting.
//
// No caching at this layer — callers add their own if hot-path
// performance matters. Re-reading a small file every call is
// microseconds; tune only when measured.
func LazyRead(envName string) func() string {
	return func() string { return Read(envName) }
}
