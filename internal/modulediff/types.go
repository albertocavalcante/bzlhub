package modulediff

import "github.com/albertocavalcante/assay/report"

// Report is the wire shape returned to UI/MCP. The fields are sorted
// alphabetically inside their respective slices so the rendered output
// is deterministic and diff-friendly.
type Report struct {
	Module string `json:"module"`
	From   string `json:"from"`
	To     string `json:"to"`

	// FromSource / ToSource describe how each side was obtained.
	// Set by the service layer after Compute; not populated by Compute itself.
	//   ""         (omitted): default — typically "local"
	//   "local":   served from the canopy index
	//   "upstream": fetched + analyzed on-the-fly from an upstream registry
	//               because the version isn't in the local index yet
	FromSource string `json:"from_source,omitempty"`
	ToSource   string `json:"to_source,omitempty"`

	CompatibilityLevel *CompatChange `json:"compatibility_level,omitempty"`
	Hermeticity        *HermDiff     `json:"hermeticity,omitempty"`
	BazelDeps          DepsDiff      `json:"bazel_deps"`
	Rules              RulesDiff     `json:"rules"`
	Providers          ProvidersDiff `json:"providers"`
	Macros             NamesDiff     `json:"macros"`
	Aspects            NamesDiff     `json:"aspects"`
	Toolchains         NamesDiff     `json:"toolchains"`
	// RepositoryRules uses RulesDiff (not NamesDiff) because repo rules
	// carry an attribute schema like normal rules — http_archive,
	// new_git_repository, etc. have heavy attr surfaces and consumers
	// care about per-attr deltas (type, default, mandatory flip).
	RepositoryRules  RulesDiff   `json:"repository_rules"`
	ModuleExtensions ModExtsDiff `json:"module_extensions"`

	// Breaking is the structural-break classification computed from the
	// rest of the report. Populated by Compute; never empty on the wire
	// when there are findings, omitted when there are none. Lets every
	// consumer (UI banner, MCP advice, CLI gate, REST clients) treat
	// "is this a breaking bump?" as a single field lookup rather than
	// re-classifying. Order is deterministic.
	Breaking []BreakingFinding `json:"breaking,omitempty"`
}

// ModExtsDiff carries names-level adds/removes plus per-extension
// tag_class deltas — the user-facing surface of a Bzlmod module
// extension is the set of tag_classes it exposes.
type ModExtsDiff struct {
	Added   []string        `json:"added,omitempty"`
	Removed []string        `json:"removed,omitempty"`
	Changed []ChangedModExt `json:"changed,omitempty"`
}

// ChangedModExt records tag_class additions/removals on an extension
// whose name persisted across versions.
type ChangedModExt struct {
	Name              string   `json:"name"`
	TagClassesAdded   []string `json:"tag_classes_added,omitempty"`
	TagClassesRemoved []string `json:"tag_classes_removed,omitempty"`
}

// CompatChange surfaces a numeric change in compatibility_level. A nil
// pointer in Report means "unchanged".
type CompatChange struct {
	From int `json:"from"`
	To   int `json:"to"`
}

// HermDiff is the symmetric difference between the two hermeticity
// class sets. Membership change matters more than ordering.
type HermDiff struct {
	Added   []report.HermeticityClass `json:"added,omitempty"`
	Removed []report.HermeticityClass `json:"removed,omitempty"`
}

// DepsDiff groups dependency changes by category.
type DepsDiff struct {
	Added   []report.ModuleKey `json:"added,omitempty"`
	Removed []report.ModuleKey `json:"removed,omitempty"`
	Changed []ChangedDep       `json:"changed,omitempty"`
}

// ChangedDep is a dep whose version moved (but name stayed the same).
type ChangedDep struct {
	Name        string `json:"name"`
	FromVersion string `json:"from_version"`
	ToVersion   string `json:"to_version"`
}

// RulesDiff is the rule-level breakdown. The most interesting subtle
// changes are inside ChangedRules.AttrDiff — attribute schema deltas
// drive migration plans.
type RulesDiff struct {
	Added   []string      `json:"added,omitempty"`
	Removed []string      `json:"removed,omitempty"`
	Changed []ChangedRule `json:"changed,omitempty"`
}

// ChangedRule describes attribute schema changes between two
// definitions of the same rule name.
type ChangedRule struct {
	Name     string            `json:"name"`
	AttrsAdd []report.AttrSpec `json:"attrs_added,omitempty"`
	AttrsRem []report.AttrSpec `json:"attrs_removed,omitempty"`
	AttrsChg []AttrChange      `json:"attrs_changed,omitempty"`
}

// AttrChange surfaces a refinement to an existing attribute: type
// change, default change, or mandatory flip.
type AttrChange struct {
	Name          string `json:"name"`
	FromType      string `json:"from_type,omitempty"`
	ToType        string `json:"to_type,omitempty"`
	FromDefault   string `json:"from_default,omitempty"`
	ToDefault     string `json:"to_default,omitempty"`
	FromMandatory bool   `json:"from_mandatory,omitempty"`
	ToMandatory   bool   `json:"to_mandatory,omitempty"`
	MandatoryFlip bool   `json:"mandatory_flip,omitempty"`
}

// ProvidersDiff: providers that arrived, disappeared, or changed their
// declared field set.
type ProvidersDiff struct {
	Added   []string          `json:"added,omitempty"`
	Removed []string          `json:"removed,omitempty"`
	Changed []ChangedProvider `json:"changed,omitempty"`
}

// ChangedProvider surfaces field-set deltas (the structural change
// rule authors care about between provider versions).
type ChangedProvider struct {
	Name          string   `json:"name"`
	FieldsAdded   []string `json:"fields_added,omitempty"`
	FieldsRemoved []string `json:"fields_removed,omitempty"`
}

// NamesDiff is the simplest shape — just added/removed name sets.
// Used for macros where the public-surface change is "did this name
// appear / disappear?"; attribute-level inspection isn't useful.
type NamesDiff struct {
	Added   []string `json:"added,omitempty"`
	Removed []string `json:"removed,omitempty"`
}
