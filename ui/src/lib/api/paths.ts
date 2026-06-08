/**
 * Single source of truth for bzlhub's HTTP API path shape on the UI
 * side. Mirrors internal/api/paths/paths.go on the server.
 *
 * Every fetch site, EventSource, and download link goes through here.
 * Future restructure (rename, v2 bump, sub-resource split) touches
 * THIS FILE plus its Go counterpart — not the dozens of call sites
 * that consume it.
 *
 * Manual sync with the Go side. Both files are small and editor-
 * visible; cross-language drift is rare enough that a presence-check
 * test would be more theater than safety. If drift becomes an
 * incident, do real codegen — see docs/plans/15-route-abstractions.md.
 */

const PREFIX = '/api/v1';
const e = encodeURIComponent;

// /api/v1/modules/<m>/versions/<v> prefix used by every per-version
// endpoint. Internal helper; call sites use the exported composers.
const mv = (m: string, v: string) => `${PREFIX}/modules/${e(m)}/versions/${e(v)}`;

export const paths = {
  // Top-level + collections
  search: () => `${PREFIX}/search`,
  xrefs: () => `${PREFIX}/xrefs`,
  drift: () => `${PREFIX}/drift`,
  upstreams: () => `${PREFIX}/upstreams`,
  modulesIndex: () => `${PREFIX}/modules`,
  // Per-module summary — powers the cross-module HoverCard. Same
  // ModuleSummary shape as one entry of modulesIndex.
  moduleDetail: (m: string) => `${PREFIX}/modules/${e(m)}`,
  moduleVersions: (m: string) => `${PREFIX}/modules/${e(m)}/versions`,

  // Module-scoped (no version pinned)
  moduleDiff: (m: string) => `${PREFIX}/modules/${e(m)}/diff`,
  moduleDiffClosure: (m: string) => `${PREFIX}/modules/${e(m)}/diff/closure`,

  // Version-scoped
  moduleVersion: mv,
  external: (m: string, v: string) => `${mv(m, v)}/external`,
  scip: (m: string, v: string) => `${mv(m, v)}/scip`,
  docs: (m: string, v: string) => `${mv(m, v)}/docs`,
  exampleFiles: (m: string, v: string) => `${mv(m, v)}/example-files`,

  closure: {
    graph: (m: string, v: string) => `${mv(m, v)}/closure/graph`,
    reverseDeps: (m: string, v: string) => `${mv(m, v)}/closure/reverse-deps`,
  },

  // Plan 07: cross-corpus consumer view. `name` is the user-facing
  // rule/provider/macro identifier; server resolves to the SCIP symbol.
  consumers: (m: string, v: string, name: string) =>
    `${mv(m, v)}/consumers/${e(name)}`,

  airgap: {
    surface: (m: string, v: string) => `${mv(m, v)}/airgap/surface`,
    downloaderConfig: (m: string, v: string) => `${mv(m, v)}/airgap/downloader-config`,
    moduleMirrors: (m: string, v: string) => `${mv(m, v)}/airgap/module-mirrors`,
  },

  // Actions (RPC writes)
  actions: {
    bump: () => `${PREFIX}/actions/bump`,
    compatCheck: () => `${PREFIX}/actions/compat-check`,
    ingestRecursive: () => `${PREFIX}/actions/ingest/recursive`,
    ingestMissing: (m: string, v: string) =>
      `${PREFIX}/actions/modules/${e(m)}/versions/${e(v)}/ingest-missing`,
  },

  // Activity (observability)
  activity: {
    history: () => `${PREFIX}/activity/history`,
    events: () => `${PREFIX}/activity/events`,
  },

  // System (admin / introspection)
  system: {
    version: () => `${PREFIX}/system/version`,
    features: () => `${PREFIX}/system/features`,
    bcrProbe: () => `${PREFIX}/system/bcr-probe`,
    status: () => `${PREFIX}/system/status`,
  },

  // Procurement (Plan 67 — submit / list / approve / deny).
  requests: () => `${PREFIX}/requests`,
  requestDetail: (id: number | string) => `${PREFIX}/requests/${e(String(id))}`,
  requestApprove: (id: number | string) => `${PREFIX}/requests/${e(String(id))}/approve`,
  requestDeny: (id: number | string) => `${PREFIX}/requests/${e(String(id))}/deny`,

  // Per-caller effective policy view — powers UI button visibility.
  policyEffective: () => `${PREFIX}/policy/effective`,
};
