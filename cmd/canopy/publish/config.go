package publish

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/albertocavalcante/bigorna"
	"github.com/albertocavalcante/canopy/cmd/canopy/forge"
	"github.com/albertocavalcante/canopy/internal/publish"
)

// resolvePublishConfig applies the env → flag layering.
func resolvePublishConfig(f publishFlags) (publishConfig, error) {
	cfg := publishConfig{
		worktree:   firstNonEmpty(f.worktree, os.Getenv("CANOPY_REGISTRY_WORKTREE")),
		forge:      firstNonEmpty(f.forge, os.Getenv("CANOPY_FORGE_KIND"), "github"),
		baseURL:    firstNonEmpty(f.baseURL, os.Getenv("CANOPY_FORGE_BASE_URL")),
		baseBranch: firstNonEmpty(f.baseBranch, os.Getenv("CANOPY_REGISTRY_BASE_BRANCH"), "main"),
		commitMode: f.commit,
		dryRun:     f.dryRun,
		jsonOut:    f.jsonOut,
		verbose:    f.verbose,
		labels:     f.labels,
	}

	// --commit requires --allow-direct gate. Check this FIRST so a user
	// who typed --commit by mistake sees the deliberate-bypass error
	// immediately, before chasing down secondary config errors.
	if cfg.commitMode && !f.allowDirect {
		return cfg, errors.New(
			"canopy publish: --commit (direct-push) requires --allow-direct to acknowledge bypassing PR review")
	}

	// Worktree required for both modes.
	if cfg.worktree == "" {
		return cfg, errors.New("canopy publish: --worktree is required (or set $CANOPY_REGISTRY_WORKTREE)")
	}

	// Forge config (only needed in PR mode; commit mode pushes via the
	// worktree's git remote directly, no API calls).
	if !cfg.commitMode {
		switch cfg.forge {
		case "github":
			// OK; baseURL is optional (defaults to api.github.com).
		case "gitlab":
			// OK; baseURL is optional (defaults to gitlab.com).
		case "bitbucketdc":
			// BB-DC is self-hosted; no default base URL exists.
			if cfg.baseURL == "" {
				return cfg, errors.New(
					"canopy publish: --base-url is required when --forge=bitbucketdc " +
						"(no default; set $CANOPY_FORGE_BASE_URL or pass --base-url)")
			}
		case "forgejo":
			// Forgejo has no canonical instance; --base-url is required.
			if cfg.baseURL == "" {
				return cfg, errors.New(
					"canopy publish: --base-url is required when --forge=forgejo " +
						"(no canonical instance; set $CANOPY_FORGE_BASE_URL or pass --base-url, e.g. https://codeberg.org)")
			}
		default:
			return cfg, fmt.Errorf(
				"canopy publish: --forge=%q not supported (valid: github | gitlab | bitbucketdc | forgejo)",
				cfg.forge)
		}

		// Repo: parse `<owner>/<name>` (or `<project>/<slug>` for DC).
		repoStr := firstNonEmpty(f.repo, os.Getenv("CANOPY_FORGE_REPO"))
		if repoStr == "" {
			return cfg, errors.New("canopy publish: --repo is required in PR mode (or set $CANOPY_FORGE_REPO)")
		}
		parts := strings.SplitN(repoStr, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return cfg, fmt.Errorf("canopy publish: --repo must be <owner>/<name>, got %q", repoStr)
		}
		cfg.repo = bigorna.Repo{Owner: parts[0], Name: parts[1]}

		// Token: read from named env var. Default differs per-forge so
		// an operator with one forge isn't required to set the other's
		// env.
		defaultTokenEnv := "CANOPY_GITHUB_TOKEN"
		switch cfg.forge {
		case "bitbucketdc":
			defaultTokenEnv = "CANOPY_BITBUCKET_TOKEN"
		case "gitlab":
			defaultTokenEnv = "CANOPY_GITLAB_TOKEN"
		case "forgejo":
			defaultTokenEnv = "CANOPY_FORGEJO_TOKEN"
		}
		cfg.tokenEnv = firstNonEmpty(f.tokenEnv, os.Getenv("CANOPY_FORGE_TOKEN_ENV"), defaultTokenEnv)
		cfg.token = os.Getenv(cfg.tokenEnv)
		if cfg.token == "" {
			return cfg, fmt.Errorf(
				"canopy publish: $%s not set (or empty)\n  Set it to a %s PAT with write access to %s/%s,\n  or override the env var name with --token-env.",
				cfg.tokenEnv, forge.DisplayName(cfg.forge), cfg.repo.Owner, cfg.repo.Name)
		}
	}

	// Bot identity (always required — Committer on the canopy commit).
	hostname, _ := os.Hostname()
	cfg.bot = publish.Identity{
		Name:  firstNonEmpty(f.botName, os.Getenv("CANOPY_BOT_NAME"), "canopy"),
		Email: firstNonEmpty(f.botEmail, os.Getenv("CANOPY_BOT_EMAIL"), "canopy@"+hostname),
	}

	// Requester identity: flag → git config → error.
	cfg.requester = publish.Identity{
		Name:  f.requesterName,
		Email: f.requesterEmail,
	}
	if cfg.requester.Name == "" {
		if name, err := gitConfigValue("user.name"); err == nil {
			cfg.requester.Name = name
		}
	}
	if cfg.requester.Email == "" {
		if email, err := gitConfigValue("user.email"); err == nil {
			cfg.requester.Email = email
		}
	}
	if cfg.requester.IsZero() {
		return cfg, errors.New(
			"canopy publish: requester identity not configured\n" +
				"  Set 'git config user.name' and 'git config user.email',\n" +
				"  or pass --requester-name and --requester-email.")
	}

	return cfg, nil
}

// splitModuleAtVersion parses "module@version" into its parts.
func splitModuleAtVersion(s string) (string, string, error) {
	if strings.Count(s, "@") != 1 {
		return "", "", fmt.Errorf("canopy publish: argument must be <module>@<version>, got %q", s)
	}
	parts := strings.SplitN(s, "@", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("canopy publish: argument must be <module>@<version>, got %q", s)
	}
	return parts[0], parts[1], nil
}

// gitConfigValue shells to git to read a config key. Returns empty
// string + nil error when the key is unset (we want fallback behavior,
// not a hard error).
func gitConfigValue(key string) (string, error) {
	out, err := exec.Command("git", "config", "--get", key).Output()
	if err != nil {
		// git exits non-zero when the key is unset.
		return "", nil
	}
	return strings.TrimSpace(string(out)), nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
