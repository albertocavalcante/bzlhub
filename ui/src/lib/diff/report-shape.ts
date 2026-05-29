import type { ModuleDiffReport } from '../api/types';

export interface HintSegment {
  text: string;
  code: boolean;
}

// Split a hint string on backtick-delimited spans so callers can render
// code segments without injecting HTML.
export function splitHint(s: string): HintSegment[] {
  const out: HintSegment[] = [];
  const parts = s.split('`');
  for (let i = 0; i < parts.length; i++) {
    if (parts[i] === '') continue;
    out.push({ text: parts[i], code: i % 2 === 1 });
  }
  return out;
}

export function transientVersionSides(report: ModuleDiffReport): string[] {
  return [
    report.from_source === 'upstream' ? report.from : null,
    report.to_source === 'upstream' ? report.to : null,
  ].filter((v): v is string => !!v);
}

// Empty-report sentinel: identical reports leave every field zero/undefined.
export function isEmptyDiffReport(report: ModuleDiffReport | null | undefined): boolean {
  if (!report) return true;
  return (
    !report.compatibility_level &&
    !report.hermeticity &&
    (report.bazel_deps?.added?.length ?? 0) +
      (report.bazel_deps?.removed?.length ?? 0) +
      (report.bazel_deps?.changed?.length ?? 0) ===
      0 &&
    (report.rules?.added?.length ?? 0) +
      (report.rules?.removed?.length ?? 0) +
      (report.rules?.changed?.length ?? 0) ===
      0 &&
    (report.providers?.added?.length ?? 0) +
      (report.providers?.removed?.length ?? 0) +
      (report.providers?.changed?.length ?? 0) ===
      0 &&
    (report.macros?.added?.length ?? 0) + (report.macros?.removed?.length ?? 0) === 0 &&
    (report.aspects?.added?.length ?? 0) + (report.aspects?.removed?.length ?? 0) === 0 &&
    (report.toolchains?.added?.length ?? 0) + (report.toolchains?.removed?.length ?? 0) === 0 &&
    (report.repository_rules?.added?.length ?? 0) +
      (report.repository_rules?.removed?.length ?? 0) +
      (report.repository_rules?.changed?.length ?? 0) ===
      0 &&
    (report.module_extensions?.added?.length ?? 0) +
      (report.module_extensions?.removed?.length ?? 0) +
      (report.module_extensions?.changed?.length ?? 0) ===
      0
  );
}

// True when "breaking only" is on and none of the breaking-flavored
// sections have anything to show.
export function isBreakingOnlyEmptyReport(report: ModuleDiffReport | null | undefined): boolean {
  return (
    !!report &&
    (report.breaking?.length ?? 0) === 0 &&
    !report.compatibility_level &&
    !(report.hermeticity && ((report.hermeticity.added?.length ?? 0) + (report.hermeticity.removed?.length ?? 0) > 0))
  );
}

export function relativeScanAge(now: number, scannedAt: number | null | undefined): string {
  if (!scannedAt) return '';
  const sec = Math.max(0, Math.floor((now - scannedAt) / 1000));
  if (sec < 60) return 'just now';
  const min = Math.floor(sec / 60);
  if (min < 60) return `${min}m ago`;
  const hr = Math.floor(min / 60);
  if (hr < 24) return `${hr}h ago`;
  return `${Math.floor(hr / 24)}d ago`;
}

