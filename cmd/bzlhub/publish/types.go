package publish

import (
	"github.com/albertocavalcante/bigorna"
	"github.com/albertocavalcante/bzlhub/internal/publish"
)

// publishFlags is the parsed shape of the cobra flag tree. Each field
// reflects exactly one CLI knob; resolution (flag → env → default)
// happens in resolvePublishConfig.
type publishFlags struct {
	// Source (mutually exclusive group; cobra enforces "exactly one").
	from            string
	sourceURL       string
	sourceIntegrity string
	sourceJSON      string

	// Deployment config (flag override; env default).
	worktree   string
	forge      string
	repo       string
	baseURL    string
	tokenEnv   string
	baseBranch string
	botName    string
	botEmail   string

	// Mode.
	commit      bool
	allowDirect bool

	// Identity overrides.
	requesterName  string
	requesterEmail string

	// Other.
	labels  []string
	dryRun  bool
	jsonOut bool
	verbose bool
}

// publishConfig is the resolved configuration after env + flag layering.
type publishConfig struct {
	worktree   string
	forge      string // "github" | "gitlab" | "bitbucketdc" | "forgejo" (only relevant in PR mode)
	repo       bigorna.Repo
	baseURL    string
	tokenEnv   string
	token      string
	baseBranch string
	bot        publish.Identity
	requester  publish.Identity

	commitMode bool // false → PR; true → direct (gated by --allow-direct)
	dryRun     bool
	jsonOut    bool
	verbose    bool

	labels []string

	// forgeClient is populated by runPublish (not resolvePublishConfig)
	// after the forge.Health() pre-flight succeeds. Stays nil in commit
	// mode and dry-run mode.
	forgeClient bigorna.Forge
}

// publishSource discriminates which of the three source paths the
// command will follow.
type publishSource struct {
	from            string // upstream registry URL (Story A)
	directURL       string // tarball URL (Story B / C)
	directIntegrity string // SRI for directURL (Story B)
	sourceJSONPath  string // path to local source.json (Story C)
}

// publishResult is the structured success record. Emitted as one-line
// JSON on stdout when --json is set; rendered as text on stderr
// otherwise.
type publishResult struct {
	Module     string `json:"module"`
	Version    string `json:"version"`
	PRNumber   int    `json:"pr_number,omitempty"`
	PRURL      string `json:"pr_url,omitempty"`
	Commit     string `json:"commit,omitempty"`
	HeadBranch string `json:"head_branch,omitempty"`
	BaseBranch string `json:"base_branch,omitempty"`
	Strategy   string `json:"strategy"`
	DurationMs int64  `json:"duration_ms"`
	DryRun     bool   `json:"dry_run,omitempty"`
}
