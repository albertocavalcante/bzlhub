package publish

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// publishOutput formats human-readable progress on stderr and the
// structured machine result on stdout (when --json is set).
type publishOutput struct {
	json    bool
	verbose bool
	stderr  io.Writer
	stdout  io.Writer
}

func newPublishOutput(jsonOut, verbose bool) *publishOutput {
	return &publishOutput{
		json:    jsonOut,
		verbose: verbose,
		stderr:  os.Stderr,
		stdout:  os.Stdout,
	}
}

func (o *publishOutput) step(format string, args ...any) {
	if o.json {
		return
	}
	fmt.Fprintf(o.stderr, "→ "+format+"\n", args...)
}

func (o *publishOutput) verboseLog(format string, args ...any) {
	if !o.verbose || o.json {
		return
	}
	fmt.Fprintf(o.stderr, "  "+format+"\n", args...)
}

func (o *publishOutput) showConfig(cfg publishConfig, src publishSource, module, ver string) {
	if o.json {
		return
	}
	fmt.Fprintln(o.stderr, "Configuration:")
	fmt.Fprintf(o.stderr, "  Worktree:  %s\n", cfg.worktree)
	if !cfg.commitMode {
		fmt.Fprintf(o.stderr, "  Forge:     %s @ %s\n", cfg.forge, firstNonEmpty(cfg.baseURL, "https://api.github.com"))
		fmt.Fprintf(o.stderr, "  Repo:      %s/%s\n", cfg.repo.Owner, cfg.repo.Name)
		fmt.Fprintf(o.stderr, "  Token:     $%s (set; redacted)\n", cfg.tokenEnv)
	}
	fmt.Fprintf(o.stderr, "  Bot:       %s\n", cfg.bot)
	fmt.Fprintf(o.stderr, "  Requester: %s\n", cfg.requester)
	fmt.Fprintf(o.stderr, "  Branch:    %s\n", cfg.baseBranch)
	mode := "pr"
	if cfg.commitMode {
		mode = "commit"
	}
	fmt.Fprintf(o.stderr, "  Mode:      %s\n", mode)
	switch {
	case src.from != "":
		fmt.Fprintf(o.stderr, "  Source:    %s (--from)\n", src.from)
	case src.directURL != "":
		fmt.Fprintf(o.stderr, "  Source:    %s (--source-url)\n", src.directURL)
	case src.sourceJSONPath != "":
		fmt.Fprintf(o.stderr, "  Source:    %s (--source-json)\n", src.sourceJSONPath)
	}
	fmt.Fprintf(o.stderr, "  Module:    %s@%s\n", module, ver)
	fmt.Fprintln(o.stderr)
}

func (o *publishOutput) dryRunSummary(commitMode bool, baseBranch, featureBranch, module, ver string) {
	if o.json {
		return
	}
	if commitMode {
		fmt.Fprintf(o.stderr, "\n[dry-run] would commit + push to %s\n", baseBranch)
		fmt.Fprintf(o.stderr, "[dry-run] commit subject: %q\n", fmt.Sprintf("feat(%s): add version %s", module, ver))
		return
	}
	fmt.Fprintf(o.stderr, "\n[dry-run] would push branch %s\n", featureBranch)
	fmt.Fprintf(o.stderr, "[dry-run] would open PR titled %q\n", fmt.Sprintf("Add %s@%s", module, ver))
}

func (o *publishOutput) emitResult(r publishResult) error {
	if o.json {
		enc := json.NewEncoder(o.stdout)
		return enc.Encode(r)
	}
	if r.DryRun {
		fmt.Fprintf(o.stderr, "[dry-run] complete (%d ms)\n", r.DurationMs)
		return nil
	}
	fmt.Fprintf(o.stderr, "\n✓ %s@%s published\n", r.Module, r.Version)
	switch {
	case r.PRURL != "":
		// PR mode — PR URL is the headline.
		fmt.Fprintf(o.stderr, "  %s\n", r.PRURL)
		if r.Commit != "" {
			fmt.Fprintf(o.stderr, "  commit %s\n", short(r.Commit))
		}
	case r.Commit != "":
		// Commit mode — show the destination + commit SHA.
		fmt.Fprintf(o.stderr, "  pushed to %s @ %s\n", r.BaseBranch, short(r.Commit))
	}
	return nil
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
