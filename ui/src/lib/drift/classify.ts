import type { ModuleDrift } from '../api/types';

export type BreakingScanValue = 'pending' | 'error' | number;
export type BreakingScanMap = Record<string, BreakingScanValue>;
export type DriftTier = 'auto-safe' | 'review' | 'skipped';

export interface DriftClassification {
  tier: DriftTier;
  reason: string;
}

// splitVersion returns the integer leading segment (major) plus a flag
// for the presence of a pre-release / BCR-backport suffix. Good enough
// for Bazel-style versions; we don't reach for a full semver parser.
export function classifyVersion(v: string | undefined): { major: number; pre: boolean } {
  if (!v) return { major: -1, pre: false };
  // Pre-release / patch-set markers Bazel modules tend to use.
  const pre = /-rc|-alpha|-beta|-pre|-dev|\.bcr\./.test(v);
  const m = /^(\d+)/.exec(v);
  return { major: m ? parseInt(m[1], 10) : -1, pre };
}

export function classifyDriftModule(m: ModuleDrift, breakingScan: BreakingScanMap = {}): DriftClassification {
  if (m.status === 'yanked-upstream') {
    return { tier: 'skipped', reason: 'yanked upstream — review before advancing' };
  }
  if (m.status === 'local-only') {
    return { tier: 'skipped', reason: 'local-only (private / bzlhub-published)' };
  }
  if (m.status === 'upstream-error') {
    return { tier: 'skipped', reason: 'upstream error — retry drift first' };
  }
  if (m.status === 'in-sync') {
    return { tier: 'skipped', reason: 'already in sync' };
  }

  // Behind. The scan result, when present, is ground truth: a what-if diff
  // against upstream gives a definitive answer about whether consumer code
  // will mechanically break. Fall back to the semver heuristic only when
  // no scan has been run for this row.
  const scan = breakingScan[m.name];
  if (typeof scan === 'number') {
    if (scan === 0) {
      return { tier: 'auto-safe', reason: 'what-if scan: no structural breaks detected' };
    }
    return {
      tier: 'review',
      reason: `what-if scan: ${scan} structurally-breaking finding${scan === 1 ? '' : 's'} — open the diff`,
    };
  }

  const local = classifyVersion(m.local_latest);
  const upstream = classifyVersion(m.upstream_latest);
  if (upstream.pre) {
    return { tier: 'review', reason: 'upstream-latest is a pre-release / BCR-backport' };
  }
  if (local.major < 0 || upstream.major < 0) {
    return { tier: 'review', reason: 'unparseable version — review manually' };
  }
  if (local.major !== upstream.major) {
    return { tier: 'review', reason: `major bump (${local.major} → ${upstream.major}) likely breaking — run scan for ground truth` };
  }
  return { tier: 'auto-safe', reason: 'same major, no pre-release — semver-safe advance (heuristic; scan for ground truth)' };
}

