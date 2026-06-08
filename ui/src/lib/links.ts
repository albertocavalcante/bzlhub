/**
 * URL composition helpers shared across the bzlhub UI.
 *
 * Centralizing these in one module keeps the route shape (`/modules/<m>`,
 * `/modules/<m>/<v>`, `/modules/<m>/<v>/code-nav/file/<path>?line=<n>`)
 * defined in exactly one place — no string-template duplication scattered
 * across components that drifts as we change bzlhub's URL surface.
 *
 * All inputs are URI-encoded at the boundary so callers can pass raw
 * module/version/file values straight from API responses.
 */

/** Bzlhub's per-module listing page (all indexed versions). */
export function moduleHref(name: string): string {
  return `/modules/${encodeURIComponent(name)}`;
}

/** Bzlhub's per-(module, version) module landing page. */
export function moduleVersionHref(name: string, version: string): string {
  return `/modules/${encodeURIComponent(name)}/${encodeURIComponent(version)}`;
}

/**
 * Code-nav file URL inside the per-(module, version) mount.
 *
 * `file` is a forward-slash path (no leading slash); each segment gets
 * URI-encoded so paths with spaces or unicode work cleanly. Optional
 * `line` is one-based for display, matching the rest of the UI.
 */
export function codeNavFileHref(name: string, version: string, file: string, line?: number): string {
  const encFile = file.split('/').map(encodeURIComponent).join('/');
  const base = `/modules/${encodeURIComponent(name)}/${encodeURIComponent(version)}/code-nav/file/${encFile}`;
  return line && line > 0 ? `${base}?line=${line}` : base;
}

/** Code-nav root for a module version (browse the file tree). */
export function codeNavRootHref(name: string, version: string): string {
  return `/modules/${encodeURIComponent(name)}/${encodeURIComponent(version)}/code-nav/`;
}

/**
 * Structured-diff page between two versions of the same module.
 * Mirrors the URL shape the backend emits in versionEntry.diff_href —
 * kept symmetric so in-page from/to selectors can navigate to a URL
 * the server would have produced anyway.
 */
export function diffHref(name: string, from: string, to: string): string {
  return `/modules/${encodeURIComponent(name)}/diff?from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}`;
}

/**
 * Diff-vs-upstream URL: compares local-mirrored (module, version)
 * against the same version freshly-fetched from upstream. Useful
 * for mirror-integrity checks ("is what I mirrored still what BCR
 * has?").
 *
 * Hardcodes the canonical BCR URL as the default upstream. Operators
 * with custom upstreams (private mirrors, mirror-of-a-mirror) can
 * manually edit the URL; surfacing the configured registry from
 * /api/v1/system/features is a Sprint 3 enhancement.
 */
export function diffVsUpstreamHref(name: string, version: string): string {
  const upstream = 'https://bcr.bazel.build';
  return `/modules/${encodeURIComponent(name)}/diff?from=${encodeURIComponent(version)}&to=${encodeURIComponent(version)}&upstream=${encodeURIComponent(upstream)}`;
}

/**
 * Human-readable version label. Compat-only bazel_deps emit an empty
 * version string (or the literal "0"); rendering those as `—` is
 * clearer than letting the raw value through.
 */
export function displayVersion(version: string): string {
  if (!version || version === '0' || version === '') return '—';
  return version;
}

/**
 * True iff `version` is a placeholder/stub value (MODULE.bazel with no
 * real version, ingested fallback). Mirrors the backend's
 * `api.IsStubVersion`. Used to gate UI affordances (install snippet,
 * "new module" badge, etc.) that would be misleading for stub
 * coordinates.
 */
export function isStubVersion(version: string): boolean {
  return version === '' || version === '0' || version === '0.0.0';
}

/**
 * True iff a (module, version) coordinate looks navigable. Used to gate
 * link rendering when the version is a stub placeholder — we don't
 * want a "follow this dep" link that resolves to a useless page.
 */
export function isNavigableVersion(version: string): boolean {
  return !isStubVersion(version);
}
