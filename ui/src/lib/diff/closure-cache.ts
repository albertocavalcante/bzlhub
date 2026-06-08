import type { ClosureDiffReport, ModuleDiffReport } from '../api/types';

export type StoredClosureScan = {
  from: string;
  to: string;
  closure: ClosureDiffReport;
  ts: number;
};

export function closureStorageKey(upstream: string, module: string): string {
  return `bzlhub:closureScans:${upstream}:${module}`;
}

export function storedClosureScan(scan: ClosureDiffReport, now = Date.now()): StoredClosureScan {
  return {
    from: scan.from,
    to: scan.to,
    closure: scan,
    ts: now,
  };
}

export function parseStoredClosureScan(raw: string | null): StoredClosureScan | null {
  if (!raw) return null;
  return JSON.parse(raw) as StoredClosureScan;
}

export function restoreClosureScanForReport(
  report: Pick<ModuleDiffReport, 'from' | 'to'>,
  entry: StoredClosureScan | null,
): { closure: ClosureDiffReport | null; scannedAt: number | null; stale: boolean } {
  if (!entry) return { closure: null, scannedAt: null, stale: false };
  // Stale-after-bump guard: closure scans are valid only for the exact
  // version pair they were computed against.
  if (entry.from !== report.from || entry.to !== report.to) {
    return { closure: null, scannedAt: null, stale: true };
  }
  return { closure: entry.closure, scannedAt: entry.ts, stale: false };
}

