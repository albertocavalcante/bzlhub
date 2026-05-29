import type { DriftReport, ModuleDrift } from '../api/types';
import type { BreakingScanMap } from './classify';

export type StoredScan = {
  from: string;
  to: string;
  error?: true;
  count?: number;
  ts: number;
};

export type StoredScanRecord = Record<string, StoredScan>;

export function breakingScanStorageKey(upstream: string, report: Pick<DriftReport, 'mirror_root'>): string {
  // Scope by (upstream, mirror_root) so two canopy setups don't bleed
  // their scans into each other if a developer flips between them.
  return `canopy:breakingScans:${upstream}:${report.mirror_root}`;
}

export function parseStoredScans(raw: string | null): StoredScanRecord {
  if (!raw) return {};
  const parsed = JSON.parse(raw);
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return {};
  return parsed as StoredScanRecord;
}

export function withStoredScan(
  existing: StoredScanRecord,
  m: ModuleDrift,
  value: 'error' | number,
  now = Date.now(),
): StoredScanRecord {
  if (!m.local_latest || !m.upstream_latest) return existing;
  return {
    ...existing,
    [m.name]:
      value === 'error'
        ? { from: m.local_latest, to: m.upstream_latest, error: true, ts: now }
        : { from: m.local_latest, to: m.upstream_latest, count: value, ts: now },
  };
}

export function restoreBreakingScans(
  modules: ModuleDrift[],
  stored: StoredScanRecord,
): { scans: BreakingScanMap; stored: StoredScanRecord; pruned: boolean } {
  const scans: BreakingScanMap = {};
  const nextStored = { ...stored };
  let pruned = false;

  for (const m of modules) {
    const s = nextStored[m.name];
    if (!s) continue;
    // Stored scan is for an exact version pair. If the row has moved
    // (bump happened, upstream advanced), drop it.
    if (s.from !== m.local_latest || s.to !== m.upstream_latest) {
      delete nextStored[m.name];
      pruned = true;
      continue;
    }
    scans[m.name] = s.error ? 'error' : (s.count ?? 0);
  }

  return { scans, stored: nextStored, pruned };
}

