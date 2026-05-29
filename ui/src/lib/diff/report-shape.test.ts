import { describe, expect, it } from 'vitest';

import type { ModuleDiffReport } from '../api/types';
import {
  isBreakingOnlyEmptyReport,
  isEmptyDiffReport,
  relativeScanAge,
  splitHint,
  transientVersionSides,
} from './report-shape';

function report(overrides: Partial<ModuleDiffReport> = {}): ModuleDiffReport {
  return {
    module: 'rules_go',
    from: '0.50.0',
    to: '0.51.0',
    bazel_deps: {},
    rules: {},
    providers: {},
    macros: {},
    aspects: {},
    toolchains: {},
    repository_rules: {},
    module_extensions: {},
    ...overrides,
  };
}

describe('splitHint', () => {
  it('splits backtick spans into text and code segments', () => {
    expect(splitHint('replace `foo` with `bar`')).toEqual([
      { text: 'replace ', code: false },
      { text: 'foo', code: true },
      { text: ' with ', code: false },
      { text: 'bar', code: true },
    ]);
  });
});

describe('diff report shape helpers', () => {
  it('detects an empty diff report', () => {
    expect(isEmptyDiffReport(report())).toBe(true);
    expect(isEmptyDiffReport(report({ rules: { added: ['go_binary'] } }))).toBe(false);
    expect(isEmptyDiffReport(report({ compatibility_level: { from: 1, to: 2 } }))).toBe(false);
  });

  it('detects when the breaking-only view has nothing to render', () => {
    expect(isBreakingOnlyEmptyReport(report())).toBe(true);
    expect(isBreakingOnlyEmptyReport(report({ breaking: [{ kind: 'rule_removed', symbol: 'go_binary', reason: 'removed' }] }))).toBe(false);
    expect(isBreakingOnlyEmptyReport(report({ hermeticity: { added: ['network-fetch-pinned'] } }))).toBe(false);
  });

  it('finds versions fetched transiently from upstream', () => {
    expect(transientVersionSides(report({ from_source: 'upstream' }))).toEqual(['0.50.0']);
    expect(transientVersionSides(report({ to_source: 'upstream' }))).toEqual(['0.51.0']);
    expect(transientVersionSides(report({ from_source: 'local', to_source: 'local' }))).toEqual([]);
  });
});

describe('relativeScanAge', () => {
  it('formats cached scan age at coarse granularity', () => {
    const now = Date.UTC(2026, 4, 21, 12, 0, 0);
    expect(relativeScanAge(now, now - 10_000)).toBe('just now');
    expect(relativeScanAge(now, now - 5 * 60_000)).toBe('5m ago');
    expect(relativeScanAge(now, now - 2 * 60 * 60_000)).toBe('2h ago');
    expect(relativeScanAge(now, now - 3 * 24 * 60 * 60_000)).toBe('3d ago');
  });
});

