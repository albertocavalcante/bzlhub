// Types mirror the Go report.ModuleReport surface. Keep in sync with
// github.com/albertocavalcante/assay/blob/main/report/{report,hermeticity}.go
// and canopy's internal/api/canopy.go.

export type HermeticityClass =
  | 'pure-starlark'
  | 'prebuilt-binaries-pinned'
  | 'build-from-source'
  | 'network-fetch-pinned'
  | 'network-fetch-unpinned'
  | 'requires-system-tools'
  | 'repository-rule-arbitrary-code';

export interface Provenance {
  file: string;
  start_row?: number;
  start_col?: number;
  end_row?: number;
  end_col?: number;
}

export interface ModuleKey {
  name: string;
  version: string;
}

export interface AttrSpec {
  name: string;
  type?: string;
  doc?: string;
  default?: string;
  mandatory?: boolean;
  providers?: string[];
}

// Provenance tag for how a rule's attrs slice was produced. Empty
// string (or missing field) means "literal" for backward compat with
// reports emitted before AttrsExtractionMethod existed.
export type AttrsExtractionMethod =
  | 'literal'
  | 'symbol_fold'
  | 'load_resolve'
  | 'interpreted';

export interface RuleSpec {
  name: string;
  doc?: string;
  attrs?: AttrSpec[];
  attrs_extraction_method?: AttrsExtractionMethod;
  executable?: boolean;
  test?: boolean;
  private?: boolean;
  provenance: Provenance;
}

export interface ProviderSpec {
  name: string;
  fields?: string[];
  doc?: string;
  private?: boolean;
  provenance: Provenance;
}

export interface MacroSpec {
  name: string;
  doc?: string;
  params?: string[];
  provenance: Provenance;
}

export interface AspectSpec {
  name: string;
  doc?: string;
  attr_aspects?: string[];
  required_providers?: string[];
  private?: boolean;
  provenance: Provenance;
}

export interface ToolchainSpec {
  name: string;
  provenance: Provenance;
}

export interface RepoRuleSpec {
  name: string;
  doc?: string;
  attrs?: AttrSpec[];
  attrs_extraction_method?: AttrsExtractionMethod;
  local?: boolean;
  private?: boolean;
  provenance: Provenance;
}

export interface ModuleExtSpec {
  name: string;
  doc?: string;
  tag_classes?: string[];
  private?: boolean;
  provenance: Provenance;
}

export interface HermeticityFinding {
  class: HermeticityClass;
  symbol: string;
  reason: string;
  provenance: Provenance;
}

export interface HermeticityProfile {
  classes: HermeticityClass[];
  findings?: HermeticityFinding[];
}

// ModuleAssets is the human-facing supporting material that sits
// alongside the .bzl analysis: README, LICENSE, example directories.
// Populated by assay/assets during ingest. All fields optional —
// missing files just leave the field empty.
export interface ModuleAssets {
  readme?: string;
  readme_path?: string;
  license?: string;
  license_path?: string;
  /** SPDX-shaped name (Apache-2.0, MIT, ...) when detected; empty otherwise. */
  license_name?: string;
  /** Root-level example directories ("example", "examples", "e2e"). */
  example_dirs?: string[];
}

export interface FileEntry {
  path: string;
  size: number;
  kind: 'bzl' | 'build' | 'module' | 'other';
}

export interface ModuleReport {
  name: string;
  version?: string;
  compatibility_level?: number;
  bazel_compatibility?: string[];
  bazel_deps?: ModuleKey[];
  rules?: RuleSpec[];
  providers?: ProviderSpec[];
  macros?: MacroSpec[];
  aspects?: AspectSpec[];
  toolchains?: ToolchainSpec[];
  repository_rules?: RepoRuleSpec[];
  module_extensions?: ModuleExtSpec[];
  hermeticity: HermeticityProfile;
  file_inventory?: FileEntry[];
  assets?: ModuleAssets;
  // Per-symbol parsed-docstring map, keyed by symbol name. Populated
  // by canopy's API handler via starlark-doc-go on every read; absent
  // when no symbol on this module has a non-empty doc string.
  parsed_docs?: Record<string, ParsedDoc>;
  // Registry-level metadata lifted from the local mirror's
  // modules/<m>/metadata.json. Absent when the mirror hasn't yet
  // enriched the local file from upstream (re-bump to populate).
  metadata?: RegistryMetadata;
  // Cross-corpus popularity hint — same data as ModuleSummary
  // .usage_count on the listing, computed per-request by the
  // server's augmentation pass. 0 omitted by the server.
  usage_count?: number;
  // Compressed source-tarball size in bytes; populated at Bump
  // time, 0/missing for pre-migration ingests.
  tarball_size?: number;
  // GitHub social-signals payload (stars/forks/languages). Absent
  // when the module has no github.com identity, when GitHub-meta is
  // disabled, or when the refresher hasn't fetched this row yet.
  github_meta?: GitHubMeta;
  // Cheap BCR provenance (I4): the bazelbuild/bazel-central-registry
  // HEAD commit at Bump time. Absent for non-BCR upstreams or
  // pre-I4 ingests. The UI links to the GitHub tree at that SHA.
  provenance?: BumpProvenance;
}

// BumpProvenance is canopy's record of the upstream BCR git state
// at the moment a (module, version) was ingested. RecordedAt is
// RFC3339; URL is the GitHub UI link to that commit's tree view.
export interface BumpProvenance {
  bcr_head_sha: string;
  url?: string;
  recorded_at: string;
}

// GitHubMeta is canopy's projection of GitHub's repo + languages
// endpoints. Refreshed every 6h (or on Bump) by the server-side
// sweeper; UI renders fields directly.
export interface GitHubMeta {
  owner: string;
  repo: string;
  description?: string;
  default_branch?: string;
  primary_language?: string;
  stars: number;
  forks: number;
  watchers: number;
  open_issues?: number;
  // Per-language byte counts (GitHub's /languages endpoint). Keys
  // ordered by declining bytes already — UI renders in iteration
  // order for the languages bar.
  languages?: Record<string, number>;
  // RFC3339 timestamp of the last successful (200/304) fetch.
  fetched_at: string;
}

// RegistryMetadata mirrors bazel-module-summary-go's RegistryMetadata
// — the subset of BCR's modules/<m>/metadata.json that describes
// the module-as-a-whole (not a specific version). Populated by
// canopy's API handler from the local mirror; absent when the
// mirror hasn't yet enriched the metadata from upstream.
export interface RegistryMetadata {
  homepage?: string;
  maintainers?: Maintainer[];
  repository?: string[];
  yanked_versions?: Record<string, string>;
}

// Maintainer matches BCR's metadata.json maintainer entries.
// Either `email` or `github` may be present (or both) — both are
// optional contact channels.
export interface Maintainer {
  name: string;
  email?: string;
  github?: string;
}

// ParsedDoc is the presentation-ready doc-string shape produced by
// canopy's internal/docview package: starlark-doc-go's fields
// (Summary/Description/Args/Returns/...) plus bazel-doc-go Refs
// already augmented with resolved Hrefs, plus a deduplicated
// Chips list for the "referenced" footer row. Renderers consume
// these fields verbatim — no URL composition, no dedup, no
// edge-case filtering needed client-side.
export interface ParsedDoc {
  Summary?: string;
  Description?: string;
  Args?: ParsedParam[];
  Returns?: ParsedReturn;
  Yields?: ParsedReturn;
  Raises?: ParsedRaise[];
  Examples?: ParsedExample[];
  Deprecated?: string;
  Note?: string;
  Refs?: ParsedDocRef[];
  Chips?: ParsedDocChip[];
}

// ParsedDocRef is one Bazel-aware reference extracted from prose.
// When Href is non-empty, the UI splices [Text](Href) at Offset
// into the appropriate field's source.
export interface ParsedDocRef {
  Kind: 0 | 1;
  Text: string;
  Label?: ParsedDocLabel;
  XrefName?: string;
  Field: string;
  Offset: number;
  Href?: string;
  // True when the UI should splice [Text](Href) into the field's
  // Markdown source. False for refs inside code spans / fences /
  // existing link text — they still navigate via the Chips footer.
  Splice?: boolean;
}

export interface ParsedDocLabel {
  Repo: string;
  Package: string;
  Target: string;
  Raw: string;
}

// ParsedDocChip is one footer-row chip. All fields are display-
// ready; the UI just iterates and renders.
export interface ParsedDocChip {
  label: string;
  href: string;
  title: string;
}

export interface ParsedParam {
  Name: string;
  Type?: string;
  Doc?: string;
}

export interface ParsedReturn {
  Type?: string;
  Doc?: string;
}

export interface ParsedRaise {
  Type: string;
  Doc?: string;
}

export interface ParsedExample {
  Lang?: string;
  Code: string;
}

export interface Hit {
  module: string;
  version: string;
  snippet?: string;
  match_kind: 'module' | 'rule' | 'provider' | 'macro' | 'repository_rule';
  match_name?: string;
  // Source file (relative to module root) for the matched definition.
  // Disambiguates same-named symbols defined in multiple .bzl files
  // (rules_cc-style). Empty for module-kind hits and macros without
  // provenance.
  file?: string;
  hermeticity?: HermeticityClass[];
  // The attribute name when the hit came from an attribute search
  // (attr:NAME). Empty for free-text / hermeticity hits.
  attr?: string;
  // True when (module, version)'s SCIP blob contains at least one
  // indexed Starlark document. The UI gates the per-result "Code →"
  // link on this — modules with empty SCIP (C-library wrappers, etc.)
  // would otherwise land the user on a friendly 404.
  has_source_index?: boolean;
}

export interface SearchResults {
  hits: Hit[];
  total: number;
}

// Drift surface — mirrors internal/drift/drift.go.

export type DriftStatus =
  | 'in-sync'
  | 'behind'
  | 'yanked-upstream'
  | 'local-only'
  | 'upstream-error';

export interface ModuleDrift {
  name: string;
  status: DriftStatus;
  local_versions: string[];
  upstream_versions?: string[];
  local_latest?: string;
  upstream_latest?: string;
  newer_upstream?: string[];
  missing_locally?: string[];
  yanked_at_upstream?: string[];
  local_only_versions?: string[];
  error?: string;
}

export interface DriftSummary {
  total: number;
  in_sync: number;
  behind: number;
  yanked_upstream: number;
  local_only: number;
  upstream_error: number;
}

export interface DriftReport {
  upstream_url: string;
  mirror_root: string;
  modules: ModuleDrift[];
  summary: DriftSummary;
}

// Audit log — mirrors internal/store.AuditEvent.

export interface AuditEvent {
  id: number;
  timestamp: string;
  kind: string;
  source: string;
  module?: string;
  version?: string;
  ok: boolean;
  duration_ms?: number;
  error?: string;
  payload?: unknown;
}

export interface HistoryResult {
  events: AuditEvent[];
}

// Module diff — mirrors internal/modulediff/Report.

export interface DiffCompatChange {
  from: number;
  to: number;
}

export interface DiffHerm {
  added?: HermeticityClass[];
  removed?: HermeticityClass[];
}

export interface DiffChangedDep {
  name: string;
  from_version: string;
  to_version: string;
}

export interface DiffDeps {
  added?: ModuleKey[];
  removed?: ModuleKey[];
  changed?: DiffChangedDep[];
}

export interface DiffAttrChange {
  name: string;
  from_type?: string;
  to_type?: string;
  from_default?: string;
  to_default?: string;
  from_mandatory?: boolean;
  to_mandatory?: boolean;
  mandatory_flip?: boolean;
}

export interface DiffChangedRule {
  name: string;
  attrs_added?: AttrSpec[];
  attrs_removed?: AttrSpec[];
  attrs_changed?: DiffAttrChange[];
}

export interface DiffRules {
  added?: string[];
  removed?: string[];
  changed?: DiffChangedRule[];
}

export interface DiffChangedProvider {
  name: string;
  fields_added?: string[];
  fields_removed?: string[];
}

export interface DiffProviders {
  added?: string[];
  removed?: string[];
  changed?: DiffChangedProvider[];
}

export interface DiffNames {
  added?: string[];
  removed?: string[];
}

export interface DiffChangedModExt {
  name: string;
  tag_classes_added?: string[];
  tag_classes_removed?: string[];
}

export interface DiffModExts {
  added?: string[];
  removed?: string[];
  changed?: DiffChangedModExt[];
}

export type BreakingKind =
  | 'compat_level_shift'
  | 'rule_removed'
  | 'rule_attr_removed'
  | 'rule_attr_now_mandatory'
  | 'provider_removed'
  | 'provider_field_removed'
  | 'module_extension_removed'
  | 'module_extension_tag_class_removed'
  | 'repo_rule_removed'
  | 'repo_rule_attr_removed'
  | 'repo_rule_attr_now_mandatory';

export interface BreakingFinding {
  kind: BreakingKind;
  symbol: string;
  detail?: string;
  reason: string;
  // Actionable one-liner — what the consumer should DO to migrate
  // past this break. Reason explains the symptom; hint prescribes
  // the fix. Rendered as a "→" line under the finding.
  hint?: string;
}

export interface ModuleDiffReport {
  module: string;
  from: string;
  to: string;
  // "local" | "upstream" — describes how each side was obtained. Omitted
  // when not surfaced (e.g., diff served without ?upstream and both sides
  // were local).
  from_source?: string;
  to_source?: string;
  compatibility_level?: DiffCompatChange;
  hermeticity?: DiffHerm;
  bazel_deps: DiffDeps;
  rules: DiffRules;
  providers: DiffProviders;
  macros: DiffNames;
  aspects: DiffNames;
  toolchains: DiffNames;
  // repository_rules carries an attribute schema like normal rules
  // (http_archive, git_repository, etc. all have heavy attr sets), so
  // the diff has the same shape — added/removed/changed-with-attrs.
  repository_rules: DiffRules;
  module_extensions: DiffModExts;
  // Structural-break classification. Empty array (or omitted) when the
  // bump introduces no consumer-visible breakage.
  breaking?: BreakingFinding[];
}

// ----- closurediff types — mirror internal/closurediff/closurediff.go -----

export interface ChangedClosureDep {
  name: string;
  from_version: string;
  to_version: string;
}

export interface ClosureDepsDiff {
  added?: ModuleKey[];
  removed?: ModuleKey[];
  changed?: ChangedClosureDep[];
}

export interface ClosureDiffReport {
  module: string;
  from: string;
  to: string;
  from_closure_size: number;
  to_closure_size: number;
  closure_deps: ClosureDepsDiff;
  // Map of module name → full per-module diff for any module whose
  // version changed (root included). Omitted when empty.
  module_diffs?: Record<string, ModuleDiffReport>;
  errors_by_module?: Record<string, string>;
  closure_breaking_total: number;
  closure_breaking_by_module?: Record<string, number>;
}
