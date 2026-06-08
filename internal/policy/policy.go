// Package policy parses and evaluates canopy's `.canopy/policy.yml`.
//
// Every operator-visible behavior is a policy knob, not hardcoded.
// Handlers call Policy.Allow(identity, action) for global gates and
// Evaluator.AllowFor(ctx, identity, action, target) for per-target
// gates that need a maintainer-table lookup.
//
// Policies are loaded once into a *Policy. The package has no
// global state; hot-reload is the caller's concern.
package policy

// Gate is the access-control verdict attached to one auth.actions
// entry in policy.yml. Mirrors the strings the operator types
// verbatim ("any", "authenticated", "group:approver", …) so a
// round-trip from YAML → struct → string preserves intent.
//
// The string-typed enum (vs an iota) is deliberate: group gates
// carry the group name in the value itself ("group:approver"),
// which doesn't compress into an iota cleanly. Comparison against
// the constants below is the supported pattern; for group gates,
// use HasGroupPrefix + GroupName.
type Gate string

const (
	// GateAny lets every request through, including anonymous.
	GateAny Gate = "any"
	// GateAuthenticated requires an identified user (any source).
	GateAuthenticated Gate = "authenticated"
	// GateMaintainer requires the user to be in the module's
	// maintainer list. Per-target gate — Policy.Allow returns false;
	// use Evaluator.AllowFor with the module name as target.
	GateMaintainer Gate = "maintainer"
	// GateDeny rejects every request unconditionally.
	GateDeny Gate = "deny"

	// groupGatePrefix marks a group-membership gate. Full value is
	// "group:<name>" — the name follows the colon verbatim.
	groupGatePrefix = "group:"
)

// IsGroup reports whether g is a "group:<name>" gate.
func (g Gate) IsGroup() bool {
	return len(g) > len(groupGatePrefix) && string(g)[:len(groupGatePrefix)] == groupGatePrefix
}

// GroupName returns the group name portion of a group gate, or ""
// when g isn't a group gate. Callers should check IsGroup first;
// returning "" silently for non-group gates keeps call sites terse
// in the common-path (one switch over the gate constants).
func (g Gate) GroupName() string {
	if !g.IsGroup() {
		return ""
	}
	return string(g)[len(groupGatePrefix):]
}

// Snapshot is the type signature consumers use when they want a
// fresh snapshot of the policy on every read. The production
// binding closes over an atomic.Pointer that SIGHUP swaps; tests
// usually wrap a fixed *Policy via Static.
//
// Using a getter (rather than a *Holder type with methods) keeps
// the consumer-facing surface tiny: one type alias, no Allow
// passthrough method, no new package to learn.
type Snapshot = func() *Policy

// Static returns a Snapshot that always yields p. Useful for tests
// and for serve.go's first wiring before the SIGHUP atomic.Pointer
// is introduced.
func Static(p *Policy) Snapshot {
	return func() *Policy { return p }
}

// Policy is the parsed shape of .canopy/policy.yml after profile
// + override merging.
type Policy struct {
	Version     int          `yaml:"version"`
	Profile     string       `yaml:"profile"`
	Auth        Auth         `yaml:"auth"`
	Admission   Admission    `yaml:"admission"`
	Naming      Naming       `yaml:"naming"`
	Maintainers Maintainers  `yaml:"maintainers"`
	Audit       AuditSection `yaml:"audit"`
	MCP         MCP          `yaml:"mcp"`
	Git         Git          `yaml:"git"`
	Content     Content      `yaml:"content"`
	Seed        Seed         `yaml:"seed"`
}

// Auth holds the auth section. Actions is a map keyed by action
// name (matching the constants in Action*) → Gate.
type Auth struct {
	Actions          map[string]Gate `yaml:"actions"`
	AnonymousAbuse   AnonymousAbuse  `yaml:"anonymous_abuse"`
	PerUserRateLimit string          `yaml:"per_user_rate_limit"`
	MaxPendingPerUser int            `yaml:"max_pending_per_user"`
	PublicIDField    string          `yaml:"public_id_field"`
}

// AnonymousAbuse holds the anti-abuse knobs that apply when
// any actions are set to GateAny.
type AnonymousAbuse struct {
	Turnstile          string `yaml:"turnstile"`
	PerIPRateLimit     string `yaml:"per_ip_rate_limit"`
	FirstTimeHoldback  bool   `yaml:"first_time_holdback"`
}

// Admission holds the procurement admission gates consumed by
// the preflight runner and the procurement state machine.
type Admission struct {
	License       License           `yaml:"license"`
	Hermeticity   map[string]string `yaml:"hermeticity"`
	Source        Source            `yaml:"source"`
	Cost          Cost              `yaml:"cost"`
	Attestations  Attestations      `yaml:"attestations"`
	SmokeTest     SmokeTest         `yaml:"smoke_test"`
	Bazel         BazelSupport      `yaml:"bazel"`
	Review        Review            `yaml:"review"`
}

type License struct {
	RequireLicense bool     `yaml:"require_license"`
	Allowlist      []string `yaml:"allowlist"`
	Denylist       []string `yaml:"denylist"`
	UnknownVerdict string   `yaml:"unknown_verdict"`
}

type Source struct {
	RequireHTTPS    bool     `yaml:"require_https"`
	AllowedHosts    []string `yaml:"allowed_hosts"`
	DenylistedHosts []string `yaml:"denylisted_hosts"`
}

type Cost struct {
	MaxArchiveSizeBytes      int64 `yaml:"max_archive_size_bytes"`
	MaxClosureModules        int   `yaml:"max_closure_modules"`
	MaxTotalAdmissionBytes   int64 `yaml:"max_total_admission_bytes"`
	MaxPatchBytesPerVersion  int64 `yaml:"max_patch_bytes_per_version"`
}

type Attestations struct {
	Require    bool     `yaml:"require"`
	TrustRoots []string `yaml:"trust_roots"`
	EnforceFor []string `yaml:"enforce_for"`
}

type SmokeTest struct {
	Enabled   bool              `yaml:"enabled"`
	Matrix    map[string][]string `yaml:"matrix"`
	Timeout   string            `yaml:"timeout"`
	OnFailure string            `yaml:"on_failure"`
}

type BazelSupport struct {
	SupportedVersions      []string `yaml:"supported_versions"`
	IncludeToolsInClosure  bool     `yaml:"include_tools_in_closure"`
}

type Review struct {
	TimeoutDays                 int  `yaml:"timeout_days"`
	AutoPassOnAlreadyInUpstream bool `yaml:"auto_pass_on_already_in_upstream"`
}

type Naming struct {
	Mode      string            `yaml:"mode"`
	Suffix    string            `yaml:"suffix"`
	PerModule map[string]string `yaml:"per_module"`
}

type Maintainers struct {
	AutoGrantOnAdmission bool `yaml:"auto_grant_on_admission"`
	MinPerModule         int  `yaml:"min_per_module"`
}

type AuditSection struct {
	RetainDays             int      `yaml:"retain_days"`
	IdentityTaggedActions  []string `yaml:"identity_tagged_actions"`
	RedactInLogs           []string `yaml:"redact_in_logs"`
	WebhookURL             string   `yaml:"webhook_url"`
}

type MCP struct {
	HTTPEnabled       bool `yaml:"http_enabled"`
	WriteToolsEnabled bool `yaml:"write_tools_enabled"`
}

type Git struct {
	Remote               string `yaml:"remote"`
	BaseBranch           string `yaml:"base_branch"`
	Adapter              string `yaml:"adapter"`
	PRWorkflow           bool   `yaml:"pr_workflow"`
	AutoMergeOnAutoPass  bool   `yaml:"auto_merge_on_auto_pass"`
}

type Content struct {
	AboutMDPath string `yaml:"about_md_path"`
	Wordmark    string `yaml:"wordmark"`
}

type Seed struct {
	Modules        []string `yaml:"modules"`
	SampleRequests int      `yaml:"sample_requests"`
}

// Diagnostic is a non-fatal warning surfaced during Load (unknown
// profile fallback, deprecated knobs). Boot wiring logs each at
// WARN.
type Diagnostic struct {
	Path    string // dotted path, e.g. "auth.actions.bogus_action"
	Message string
}
