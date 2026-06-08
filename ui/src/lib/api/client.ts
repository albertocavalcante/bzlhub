import type {
  Hit,
  ModuleReport,
  SearchResults,
  HermeticityClass,
  DriftReport,
  AuditEvent,
  HistoryResult,
  ModuleDiffReport,
  ClosureDiffReport,
} from './types';
import { paths } from './paths';
export { renderSnippet } from '../search/snippet';

/**
 * API client for bzlhub's /api/* endpoints. Path strings live in
 * ./paths (mirror of internal/api/paths/paths.go); this client wraps
 * them with fetch + JSON handling. Every method takes an optional
 * AbortSignal so the caller can cancel in-flight requests on prop
 * changes — critical for the search-as-you-type flow.
 */

const BASE = '';

export interface SearchParams {
  q: string;
  limit?: number;
  hermeticity?: HermeticityClass[];
  // Attribute-name search: cross-corpus filter to rules whose
  // attrs include this exact name. The UI typically derives this
  // from `attr:NAME` syntax in the search box.
  attr?: string;
  // Symbol-kind search: when set, q is interpreted as the exact
  // symbol name to match within this kind. Derived from
  // rule:NAME / provider:NAME / macro:NAME / repo_rule:NAME /
  // module_extension:NAME prefixes in the search box.
  kind?: 'rule' | 'provider' | 'macro' | 'repo_rule' | 'module_extension';
}

export async function search(
  params: SearchParams,
  signal?: AbortSignal,
): Promise<SearchResults> {
  const url = new URL(paths.search(), window.location.origin);
  url.searchParams.set('q', params.q);
  if (params.limit) url.searchParams.set('limit', String(params.limit));
  if (params.attr) url.searchParams.set('attr', params.attr);
  if (params.kind) url.searchParams.set('kind', params.kind);
  for (const h of params.hermeticity ?? []) {
    url.searchParams.append('hermeticity', h);
  }
  const res = await fetch(url, { signal });
  if (!res.ok) throw new Error(`/api/v1/search: HTTP ${res.status}`);
  return res.json();
}

export interface VersionEntry {
  version: string;
  href: string;
  code_nav_href: string;
  diff_href?: string;
  diff_from_version?: string;
  is_stub?: boolean;
  // RFC3339 ingest timestamp.
  ingested_at?: string;
  // "+3d", "+2.4mo" compact label of the gap between this row and
  // the immediately-next-older row. Server-derived (v0.2 semantic
  // uses ingested_at, not upstream publish date — the tooltip on
  // the rendered label clarifies).
  cadence_label?: string;
  // Declared compatibility_level for this version. 0/missing →
  // "no compat declared" (BCR convention); non-zero values
  // indicate compat-cohort membership.
  compat_level?: number;
  // Compressed source tarball size in bytes. 0/missing for
  // pre-migration ingests.
  tarball_size?: number;
  // Upstream-declared yank reason. Non-empty → the version was
  // yanked by the publisher; UI badges it accordingly.
  yanked_reason?: string;
  // Cross-corpus pinning signals — populated by the server when it
  // can compute "how many other indexed modules pin this exact
  // (module, version)". Not yet wired on the Go side; UI handles
  // undefined gracefully (the badge is hidden when 0/absent).
  pin_count?: number;
  pin_pct?: number;
}

// ClosureGraphResponse mirrors the server's
// /api/v1/modules/{m}/versions/{v}/closure/graph payload. Used by the deps card
// (transitive view) and the ClosureGraph component (mermaid render).
export interface ClosureNode {
  name: string;
  version: string;
  external?: boolean;
}

export interface ClosureEdge {
  from: string;
  to: string;
}

export interface ClosureGraphResponse {
  root: string;
  nodes: ClosureNode[];
  edges: ClosureEdge[];
  max_depth_reached?: boolean;
}

export async function getClosureGraph(
  module: string,
  version: string,
  signal?: AbortSignal,
): Promise<ClosureGraphResponse> {
  const url = `${BASE}${paths.closure.graph(module, version)}`;
  const res = await fetch(url, { signal });
  if (!res.ok) throw new Error(`/api/v1/modules/${module}/versions/${version}/closure/graph: HTTP ${res.status}`);
  return res.json();
}

export async function listVersions(
  module: string,
  signal?: AbortSignal,
): Promise<{ module: string; versions: string[]; entries?: VersionEntry[] }> {
  const res = await fetch(`${BASE}${paths.moduleVersions(module)}`, { signal });
  if (!res.ok) throw new Error(`/api/v1/modules/${module}/versions: HTTP ${res.status}`);
  return res.json();
}

/**
 * DriftStatus is the closed enum of drift outcomes for a module's
 * latest version. Mirrors internal/api/drift.go. The DriftChip
 * component switches presentation on this token exclusively.
 *
 * unknown          — no source configured / not yet computed (chip silent).
 * in-sync          — local latest == upstream latest (chip silent).
 * behind           — upstream has versions newer than local latest (↑N).
 * yanked-upstream  — local latest in upstream's yanked_versions (⚠ yanked).
 * local-only       — module not present upstream (local chip).
 * upstream-error   — drift compute failed (chip silent; logged server-side).
 */
export type DriftStatus =
  | 'unknown'
  | 'in-sync'
  | 'behind'
  | 'yanked-upstream'
  | 'local-only'
  | 'upstream-error';

/**
 * DriftSummary mirrors internal/api/DriftSummary. Carried on every
 * ModuleSummary so listing pages render drift badges without a
 * separate /api/v1/drift roundtrip. Empty object {} is the canonical
 * "unknown" payload; the UI treats absent .status and 'unknown' the
 * same (silent chip).
 *
 * computed_at: when the verdict was last evaluated.
 * synced_at: when the upstream snapshot used for the verdict was
 * fetched. Distinct from computed_at — a drift refresh between
 * syncs advances computed_at but leaves synced_at older.
 */
export interface DriftSummary {
  status?: DriftStatus;
  behind?: number;
  latest_upstream?: string;
  computed_at?: string;
  upstream_sha?: string;
  synced_at?: string;
}

/**
 * Corpus-overview API: one summary per indexed module. Used by the
 * /modules SvelteKit route to render a browse surface for users who
 * land on bzlhub without knowing what to search for.
 */
export interface ModuleSummary {
  name: string;
  latest_version: string;
  version_count: number;
  // True when the latest version's SCIP index contains at least one
  // Starlark document. Modules whose source tarballs ship no
  // Starlark (zlib, abseil-cpp, …) have non-empty SCIP blobs but
  // zero documents — the UI hides "Code →" affordances for those
  // to keep users out of a misleading empty file tree.
  has_source_index: boolean;
  // Registry metadata lifted from mirror/modules/<m>/metadata.json
  // by bzlhub.Service.ListModules. Counts (not full lists) keep the
  // listing payload flat across hundreds of modules.
  homepage?: string;
  maintainer_count?: number;
  // Short "owner/repo" display label for the source repository
  // (when bzlhub could extract one from metadata.repository or a
  // github.com homepage URL). Preferred over the raw hostname for
  // listing-card display because "github.com" is identical for
  // nearly every BCR module.
  repo_label?: string;
  // RFC3339 timestamp when the latest version was first written
  // to this bzlhub's index. Drives the "X ago" freshness badge.
  latest_ingested_at?: string;
  // Server-computed: first-version-only module ingested in the
  // last 7 days. Drives the "NEW" badge on cards.
  is_new?: boolean;
  // Rough popularity: number of OTHER indexed modules whose
  // bazel_deps reference this one (by name, any version).
  // 0 omitted by the server when there's nothing to report.
  usage_count?: number;
  // Pre-shaped URL for the structured (latest-1, latest) diff link
  // on the module card. Server emits the v0.2 path when the prior
  // version is indexed; omitted otherwise.
  latest_diff_href?: string;
  // Drift signal for the latest version. Always present (Go backend
  // serves {} when unknown). Drives the inline DriftChip — see
  // $lib/components/DriftChip.svelte.
  drift: DriftSummary;
}

export interface CorpusStats {
  modules: number;
  versions: number;
  documented_symbols: number;
}

export async function listModules(
  signal?: AbortSignal,
): Promise<{ modules: ModuleSummary[]; corpus_stats?: CorpusStats }> {
  const res = await fetch(`${BASE}${paths.modulesIndex()}`, { signal });
  if (!res.ok) throw new Error(`/api/v1/modules: HTTP ${res.status}`);
  return res.json();
}

/**
 * getModuleSummary fetches the single-module slice of listModules
 * for the cross-module HoverCard. Same ModuleSummary shape one
 * entry of listModules would return.
 *
 * Throws ModuleSummaryNotFoundError on HTTP 404 (module not
 * indexed in this bzlhub). Other transport failures throw a
 * generic Error.
 */
export async function getModuleSummary(
  module: string,
  signal?: AbortSignal,
): Promise<ModuleSummary> {
  const res = await fetch(`${BASE}${paths.moduleDetail(module)}`, { signal });
  if (res.status === 404) throw new ModuleSummaryNotFoundError(module);
  if (!res.ok) throw new Error(`/api/v1/modules/${module}: HTTP ${res.status}`);
  return res.json();
}

export class ModuleSummaryNotFoundError extends Error {
  constructor(public module: string) {
    super(`module ${module} not indexed in this bzlhub`);
    this.name = 'ModuleSummaryNotFoundError';
  }
}

export async function getModule(
  module: string,
  version: string,
  signal?: AbortSignal,
): Promise<ModuleReport> {
  const res = await fetch(
    `${BASE}${paths.moduleVersion(module, version)}`,
    { signal },
  );
  if (res.status === 404) throw new ModuleNotFoundError(module, version);
  if (!res.ok) throw new Error(`/api/v1/modules/${module}/versions/${version}: HTTP ${res.status}`);
  return res.json();
}

export class ModuleNotFoundError extends Error {
  constructor(public module: string, public version: string) {
    super(`module ${module}@${version} not found`);
    this.name = 'ModuleNotFoundError';
  }
}

export interface DriftParams {
  upstream?: string;
  module?: string;
  workers?: number;
}

export class DriftNotAvailableError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'DriftNotAvailableError';
  }
}

export async function getDrift(
  params: DriftParams = {},
  signal?: AbortSignal,
): Promise<DriftReport> {
  const url = new URL(paths.drift(), window.location.origin);
  if (params.upstream) url.searchParams.set('upstream', params.upstream);
  if (params.module) url.searchParams.set('module', params.module);
  if (params.workers) url.searchParams.set('workers', String(params.workers));
  const res = await fetch(url, { signal });
  if (res.status === 409) {
    throw new DriftNotAvailableError(await res.text());
  }
  if (!res.ok) throw new Error(`/api/v1/drift: HTTP ${res.status}`);
  return res.json();
}

export interface BumpParams {
  module: string;
  version: string;
  upstream?: string;
}

export class BumpUpstreamError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'BumpUpstreamError';
  }
}

/**
 * Thrown when an ingest-class endpoint (currently /api/v1/actions/bump and
 * /api/v1/actions/ingest/recursive) returns 503 with the IngestWriteEnabled flag
 * off. Distinct from server-overload 503: this names the env var the
 * operator needs to flip.
 */
export class IngestDisabledError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'IngestDisabledError';
  }
}

/**
 * Thrown when an ingest-class endpoint is rate-limited (429) or the
 * global concurrency cap is hit (503). retryAfterSec is parsed from
 * the Retry-After header so the UI can render an honest "try again
 * in N seconds" instead of guessing.
 */
export class IngestRateLimitedError extends Error {
  retryAfterSec: number;
  constructor(message: string, retryAfterSec: number) {
    super(message);
    this.name = 'IngestRateLimitedError';
    this.retryAfterSec = retryAfterSec;
  }
}

export async function bumpModule(
  params: BumpParams,
  signal?: AbortSignal,
): Promise<ModuleReport> {
  const res = await fetch(`${BASE}${paths.actions.bump()}`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'X-Bzlhub-Source': 'drift-ui',
    },
    body: JSON.stringify(params),
    signal,
  });
  if (res.status === 502) throw new BumpUpstreamError(await res.text());
  if (res.status === 409) throw new DriftNotAvailableError(await res.text());
  // /api/v1/actions/bump is gated by the same feature flag + rate limiter as
  // /api/v1/actions/ingest/recursive — the gate errors share the same shape
  // (429 with Retry-After, 503 with "disabled" body), so the typed
  // errors below are reused for both endpoints.
  if (res.status === 429) {
    const retry = parseInt(res.headers.get('Retry-After') ?? '60', 10);
    throw new IngestRateLimitedError(await res.text(), isNaN(retry) ? 60 : retry);
  }
  if (res.status === 503) {
    const body = await res.text();
    if (body.includes('disabled')) throw new IngestDisabledError(body);
    const retry = parseInt(res.headers.get('Retry-After') ?? '10', 10);
    throw new IngestRateLimitedError(body, isNaN(retry) ? 10 : retry);
  }
  if (!res.ok) throw new Error(`/api/v1/actions/bump: HTTP ${res.status}`);
  return res.json();
}

export interface IngestRecursiveParams {
  module: string;
  version: string;
  upstream?: string;
  include_bazel_tools?: boolean;
  bazel_version?: string;
  workers?: number;
}

export interface IngestRecursiveResult {
  visited: number;
  mirrored: number;
  errors?: { module: string; version: string; error: string }[];
}

export async function ingestRecursive(
  params: IngestRecursiveParams,
  signal?: AbortSignal,
): Promise<IngestRecursiveResult> {
  const res = await fetch(`${BASE}${paths.actions.ingestRecursive()}`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'X-Bzlhub-Source': 'drift-ui',
    },
    body: JSON.stringify(params),
    signal,
  });
  if (res.status === 409) throw new DriftNotAvailableError(await res.text());
  // 429 = per-IP rate limit; 503 = either disabled or capacity. The
  // body distinguishes the two 503 cases so the UI can render the
  // right copy.
  if (res.status === 429) {
    const retry = parseInt(res.headers.get('Retry-After') ?? '60', 10);
    throw new IngestRateLimitedError(await res.text(), isNaN(retry) ? 60 : retry);
  }
  if (res.status === 503) {
    const body = await res.text();
    if (body.includes('disabled')) throw new IngestDisabledError(body);
    const retry = parseInt(res.headers.get('Retry-After') ?? '10', 10);
    throw new IngestRateLimitedError(body, isNaN(retry) ? 10 : retry);
  }
  if (!res.ok) throw new Error(`/api/v1/actions/ingest/recursive: HTTP ${res.status}`);
  return res.json();
}

/**
 * GET /api/v1/system/features — public feature-flag snapshot. The UI hits this
 * once on load and uses it to gate write affordances (the "Ingest
 * from BCR" button on the friendly 404, etc.). The endpoint
 * deliberately omits server-internal flags like registry URL or
 * rate-limit bypass IPs.
 */
export interface FeatureSnapshot {
  ingest_write_enabled: boolean;
  demo_mode?: boolean;
  demo_banner?: string;
}

export async function getFeatures(signal?: AbortSignal): Promise<FeatureSnapshot> {
  const res = await fetch(`${BASE}${paths.system.features()}`, { signal });
  if (!res.ok) throw new Error(`/api/v1/system/features: HTTP ${res.status}`);
  return res.json();
}

/**
 * BCR preflight probe result. The /api/v1/system/bcr-probe handler hits the
 * configured upstream registry to answer "does this (module, version)
 * exist?" before the UI commits to an ingest attempt. Three terminal
 * shapes:
 *
 *   - version_exists=true → green path, the Ingest button is honest
 *   - module_exists=true, version_exists=false → the module is on BCR
 *     but this version isn't; UI can suggest latest_version
 *   - module_exists=false → the name is wrong; UI falls back to local-
 *     index name suggestions (browse-modules link)
 *
 * registry_url is echoed so the UI can render "checked against <url>"
 * without re-fetching /api/v1/system/features.
 */
export interface BcrProbeResult {
  module: string;
  version: string;
  version_exists: boolean;
  module_exists: boolean;
  versions_available?: string[];
  latest_version?: string;
  registry_url: string;
}

/**
 * Thrown when the upstream BCR is unreachable (5xx, TLS, DNS). The
 * server responds 502 for these — distinct from a probe that succeeds
 * with version_exists=false (which is a normal 200 answer).
 */
export class BcrUnreachableError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'BcrUnreachableError';
  }
}

export async function getBcrProbe(
  module: string,
  version: string,
  signal?: AbortSignal,
): Promise<BcrProbeResult> {
  const url = new URL(paths.system.bcrProbe(), window.location.origin);
  url.searchParams.set('module', module);
  url.searchParams.set('version', version);
  const res = await fetch(url, { signal });
  if (res.status === 502) throw new BcrUnreachableError(await res.text());
  if (!res.ok) throw new Error(`/api/v1/system/bcr-probe: HTTP ${res.status}`);
  return res.json();
}

export interface HistoryParams {
  kind?: string[];
  source?: string;
  module?: string;
  limit?: number;
}

export async function getHistory(
  params: HistoryParams = {},
  signal?: AbortSignal,
): Promise<HistoryResult> {
  const url = new URL(paths.activity.history(), window.location.origin);
  for (const k of params.kind ?? []) url.searchParams.append('kind', k);
  if (params.source) url.searchParams.set('source', params.source);
  if (params.module) url.searchParams.set('module', params.module);
  if (params.limit) url.searchParams.set('limit', String(params.limit));
  const res = await fetch(url, { signal });
  if (!res.ok) throw new Error(`/api/v1/activity/history: HTTP ${res.status}`);
  return res.json();
}

export async function getDiff(
  module: string,
  from: string,
  to: string,
  upstream?: string,
  signal?: AbortSignal,
): Promise<ModuleDiffReport> {
  const url = new URL(paths.moduleDiff(module), window.location.origin);
  url.searchParams.set('from', from);
  url.searchParams.set('to', to);
  if (upstream) url.searchParams.set('upstream', upstream);
  const res = await fetch(url, { signal });
  if (res.status === 404) {
    throw new Error(
      `one of ${module}@${from} or ${module}@${to} isn't in the index yet — ingest the missing version first, or retry with ?upstream= to fetch on-the-fly`,
    );
  }
  if (!res.ok) throw new Error(`/api/v1/modules/${module}/diff: HTTP ${res.status}`);
  return res.json();
}

/**
 * getDiffClosure walks the bazel_dep closure on each side via MVS,
 * runs per-module diffs for every dep whose version moved, and rolls
 * up breaking findings. Slow (5–30s typical) — the caller should
 * surface loading state and ideally make the call user-initiated.
 *
 * Requires upstream. Without one, the server returns 400 because MVS
 * needs a registry to walk; we replicate that requirement client-side
 * so the failure is friendlier.
 */
export async function getDiffClosure(
  module: string,
  from: string,
  to: string,
  upstream: string,
  signal?: AbortSignal,
): Promise<ClosureDiffReport> {
  if (!upstream) {
    throw new Error('closure diff requires an upstream registry URL (MVS needs one)');
  }
  const url = new URL(paths.moduleDiffClosure(module), window.location.origin);
  url.searchParams.set('from', from);
  url.searchParams.set('to', to);
  url.searchParams.set('upstream', upstream);
  const res = await fetch(url, { signal });
  if (!res.ok) {
    const body = await res.text();
    throw new Error(`/api/v1/modules/${module}/diff/closure: HTTP ${res.status} — ${body}`);
  }
  return res.json();
}

// ---- External URL surface (per-module + closure-wide) -------------------
//
// These types mirror Go's internal/api/external.go. Kept manually in sync —
// the wire shape is small and stable; codegen would be overkill.

export interface ExternalRef {
  url: string;
  host: string;
  class: string;
  mutability: string;
  sha256?: string;
  integrity?: string;
  api_name?: string;
  rule_name?: string;
  platform?: string;
  tainted?: boolean;
  file?: string;
  // Analyzer's "how sure are we about this URL?" signal — computed at
  // response time from tainted + platform. See Go's ExternalRef.Confidence.
  confidence?: 'tainted' | 'platform-specific' | 'resolved';
  // Populated only in the closure-wide /airgap/surface response —
  // names the closure node that contributed this ref, formatted
  // "<module>@<version>". Empty on per-module /external responses.
  source_module?: string;
}

export interface ExternalForkError {
  platform: string;
  message: string;
}

export interface ExtensionConsumerCall {
  consumer_module: string;
  consumer_version: string;
  tag_name: string;
  tag_attrs?: Record<string, unknown>;
  dev_dependency?: boolean;
  isolate?: boolean;
}

export interface ExtensionCorpusUsage {
  extension_file: string;
  extension_name: string;
  consumers: ExtensionConsumerCall[];
}

export interface ExternalSurfaceResponse {
  module: string;
  version: string;
  refs: ExternalRef[];
  fork_errors?: ExternalForkError[];
  class_counts?: Record<string, number>;
  corpus_usages?: ExtensionCorpusUsage[];
}

export interface ClosureSurfaceModule {
  module: string;
  version: string;
  ref_count: number;
  class_counts?: Record<string, number>;
  external?: boolean;
}

export interface ClosureSurfaceResponse {
  root: string;
  modules: ClosureSurfaceModule[];
  refs: ExternalRef[];
  fork_errors?: ExternalForkError[];
  class_counts?: Record<string, number>;
  max_depth_reached?: boolean;
  missing_modules?: string[];
}

export async function getExternalSurface(
  module: string,
  version: string,
  signal?: AbortSignal,
): Promise<ExternalSurfaceResponse> {
  const url = `${BASE}${paths.external(module, version)}`;
  const res = await fetch(url, { signal });
  if (!res.ok) throw new Error(`${paths.external(module, version)}: HTTP ${res.status}`);
  return res.json();
}

export async function getAirgapSurface(
  module: string,
  version: string,
  signal?: AbortSignal,
): Promise<ClosureSurfaceResponse> {
  const url = `${BASE}${paths.airgap.surface(module, version)}`;
  const res = await fetch(url, { signal });
  if (!res.ok) throw new Error(`${paths.airgap.surface(module, version)}: HTTP ${res.status}`);
  return res.json();
}

// ---- Compat-check -------------------------------------------------------
//
// Mirrors Go's internal/compat.Result (returned by POST
// /api/v1/actions/compat-check). The frontend renders Result directly
// from JSON; PlanMarkdown is a pre-rendered migration script.

export interface CompatSelfInfo {
  name?: string;
  version?: string;
}

export interface CompatBreakingFinding {
  // Mirrors Go's internal/modulediff.BreakingFinding wire shape.
  kind: string;
  symbol: string;
  detail?: string;
  // Reason explains the symptom; Hint prescribes the fix. Both
  // server-authored prose surfaced verbatim in the UI.
  reason: string;
  hint?: string;
  // Buildozer codemod or commented discovery line (Plan 06).
  // Empty for kinds that need a human-decided value (compat-level
  // shifts, "now mandatory" attrs).
  codemod?: string;
}

export interface CompatDepEntry {
  name: string;
  from_version: string;
  to_version?: string;
  in_corpus: boolean;
  same_version?: boolean;
  from_indexed: boolean;
  breaking_count: number;
  // Report is the underlying modulediff.Report; left as unknown here —
  // PlanMarkdown is the user-facing rendering.
  report?: unknown;
  findings?: CompatBreakingFinding[];
}

export interface CompatSummary {
  total_deps: number;
  breaking_deps: number;
  missing_from_corpus: number;
  already_latest: number;
}

export interface CompatResult {
  self: CompatSelfInfo;
  deps: CompatDepEntry[];
  summary: CompatSummary;
  plan_markdown: string;
  // Ready-to-pipe `migrate.sh` (Plan 06). Empty when no finding
  // carries a clean buildozer codemod — UI hides the download
  // button in that case.
  plan_shell?: string;
}

export interface CompatCheckOptions {
  includeDev?: boolean;
}

// ---- Federation introspection (Plan 16 F3) -------------------------------

export interface PrimaryInfo {
  kind: 'local' | 'none' | string;
  root?: string;
}

export interface UpstreamInfo {
  url: string;
  reachable: boolean;
  // RFC3339 timestamp.
  last_probe: string;
  last_probe_latency_ms: number;
  last_probe_error_msg?: string;
}

export interface CacheStatsInfo {
  entries: number;
  hits: number;
  misses: number;
}

export interface UpstreamsResponse {
  primary: PrimaryInfo;
  upstreams: UpstreamInfo[];
  cache_stats: CacheStatsInfo;
}

/**
 * Fetch the federation state snapshot. Empty `upstreams` array means
 * federation isn't configured. Used by the footer reachability dot;
 * also useful for any future status-page integration.
 */
export async function getUpstreams(signal?: AbortSignal): Promise<UpstreamsResponse> {
  const res = await fetch(`${BASE}${paths.upstreams()}`, { signal });
  if (!res.ok) throw new Error(`${paths.upstreams()}: HTTP ${res.status}`);
  return res.json();
}

// ---- Cross-corpus consumers (Plan 07) -----------------------------------

export interface CallSite {
  file: string;
  line: number;
  column?: number;
  href: string;
}

export interface ConsumerEntry {
  module: string;
  version: string;
  module_href: string;
  call_sites: CallSite[];
}

export interface ConsumersResult {
  symbol: string;
  module: string;
  version: string;
  name: string;
  kind?: 'rule' | 'provider' | 'macro' | 'repo_rule' | 'module_extension';
  file?: string;
  total_call_sites: number;
  consumer_count: number;
  skipped: number;
  consumers: ConsumerEntry[];
}

/**
 * Fetch the cross-corpus consumer view for a symbol defined by
 * (module, version). The server resolves the user-facing name via
 * the module's ModuleReport and walks every indexed SCIP blob.
 *
 * Throws (rejecting the promise) on 404 — call sites use that to
 * distinguish "symbol not in this module" from an empty consumer
 * list.
 */
export async function getConsumers(
  module: string,
  version: string,
  name: string,
  options: { includeSelf?: boolean } = {},
  signal?: AbortSignal,
): Promise<ConsumersResult> {
  const url = new URL(paths.consumers(module, version, name), window.location.origin);
  if (options.includeSelf) url.searchParams.set('include_self', 'true');
  const res = await fetch(url, { signal });
  if (res.status === 404) {
    throw new Error(`${name} not found in ${module}@${version}`);
  }
  if (!res.ok) {
    throw new Error(`${paths.consumers(module, version, name)}: HTTP ${res.status}`);
  }
  return res.json();
}

export async function compatCheck(
  body: string,
  opts: CompatCheckOptions = {},
  signal?: AbortSignal,
): Promise<CompatResult> {
  const url = new URL(paths.actions.compatCheck(), window.location.origin);
  if (opts.includeDev) url.searchParams.set('include_dev', 'true');
  const res = await fetch(url, {
    method: 'POST',
    headers: { 'content-type': 'text/plain' },
    body,
    signal,
  });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(`${paths.actions.compatCheck()}: HTTP ${res.status} — ${text}`);
  }
  return res.json();
}

// ---- procurement ---------------------------------------------------

import type {
  Request as ProcRequest,
  RequestState,
  RequestsResult,
  PolicyEffective,
} from './types';
import { auth } from '$lib/auth/auth.svelte';
import { applyAuthHeader } from '$lib/auth/token';

// authed wraps fetch's RequestInit with Authorization when the
// user is signed in. Procurement endpoints route through this;
// anonymous-read endpoints (search, modules listing, etc.) keep
// using raw fetch since they don't need credentials.
function authed(init: RequestInit = {}): RequestInit {
  return applyAuthHeader(init, auth.get());
}

// check401 detects an expired/revoked bearer and clears local
// auth state so the UI flips back to "sign in" without manual
// intervention. Header-auth users never reach this branch — their
// requests have no Authorization header to be wrong about. Bearer
// users see a brief "signed in → signed out" transition; the
// caller still receives the error (we re-throw via the normal
// !res.ok path), so loud failure surfaces too.
function check401(res: Response): void {
  if (res.status === 401 && auth.get() !== null) {
    auth.signOut();
  }
}

export interface ListRequestsParams {
  states?: RequestState[];
  submitter?: string;
  limit?: number;
}

export async function listRequests(
  params: ListRequestsParams = {},
  signal?: AbortSignal,
): Promise<ProcRequest[]> {
  const url = new URL(paths.requests(), window.location.origin);
  for (const s of params.states ?? []) url.searchParams.append('state', s);
  if (params.submitter) url.searchParams.set('submitter', params.submitter);
  if (params.limit) url.searchParams.set('limit', String(params.limit));
  const res = await fetch(url, authed({ signal }));
  check401(res);
  if (!res.ok) throw new Error(`${paths.requests()}: HTTP ${res.status}`);
  const body = (await res.json()) as RequestsResult;
  return body.requests ?? [];
}

export async function getRequest(id: number, signal?: AbortSignal): Promise<ProcRequest> {
  const res = await fetch(`${BASE}${paths.requestDetail(id)}`, authed({ signal }));
  check401(res);
  if (res.status === 404) throw new Error('request not found');
  if (!res.ok) throw new Error(`${paths.requestDetail(id)}: HTTP ${res.status}`);
  return res.json();
}

export interface SubmitRequestBody {
  module: string;
  version: string;
  source_url?: string;
  notes?: string;
}

export interface SubmitRequestResult {
  id: number;
  state: RequestState;
  dedup?: boolean;
}

export async function submitRequest(
  body: SubmitRequestBody,
  signal?: AbortSignal,
): Promise<SubmitRequestResult> {
  const res = await fetch(`${BASE}${paths.requests()}`, authed({
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
    signal,
  }));
  check401(res);
  if (!res.ok) throw new Error(`${paths.requests()}: HTTP ${res.status}: ${await res.text()}`);
  return res.json();
}

export async function approveRequest(id: number, signal?: AbortSignal): Promise<SubmitRequestResult> {
  const res = await fetch(`${BASE}${paths.requestApprove(id)}`, authed({ method: 'POST', signal }));
  check401(res);
  if (!res.ok) throw new Error(`${paths.requestApprove(id)}: HTTP ${res.status}: ${await res.text()}`);
  return res.json();
}

export async function denyRequest(
  id: number,
  reason: string,
  signal?: AbortSignal,
): Promise<SubmitRequestResult> {
  const res = await fetch(`${BASE}${paths.requestDeny(id)}`, authed({
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ reason }),
    signal,
  }));
  check401(res);
  if (!res.ok) throw new Error(`${paths.requestDeny(id)}: HTTP ${res.status}: ${await res.text()}`);
  return res.json();
}

export async function getPolicyEffective(signal?: AbortSignal): Promise<PolicyEffective> {
  const res = await fetch(`${BASE}${paths.policyEffective()}`, authed({ signal }));
  check401(res);
  if (!res.ok) throw new Error(`${paths.policyEffective()}: HTTP ${res.status}`);
  return res.json();
}

export type {
  Hit,
  ModuleReport,
  SearchResults,
  HermeticityClass,
  DriftReport,
  AuditEvent,
  HistoryResult,
  ModuleDiffReport,
  ClosureDiffReport,
};
