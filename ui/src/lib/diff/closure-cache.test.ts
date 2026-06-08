import { describe, expect, it } from 'vitest';

import type { ClosureDiffReport, ModuleDiffReport } from '../api/types';
import {
  closureStorageKey,
  parseStoredClosureScan,
  restoreClosureScanForReport,
  storedClosureScan,
} from './closure-cache';

function closure(overrides: Partial<ClosureDiffReport> = {}): ClosureDiffReport {
  return {
    module: 'rules_go',
    from: '0.50.0',
    to: '0.51.0',
    from_closure_size: 10,
    to_closure_size: 11,
    closure_deps: {},
    closure_breaking_total: 0,
    ...overrides,
  };
}

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

describe('closure scan cache', () => {
  it('scopes entries by upstream and module', () => {
    expect(closureStorageKey('https://bcr.bazel.build', 'rules_go')).toBe(
      'bzlhub:closureScans:https://bcr.bazel.build:rules_go',
    );
  });

  it('serializes and parses a stored closure scan', () => {
    const stored = storedClosureScan(closure(), 123);

    expect(parseStoredClosureScan(JSON.stringify(stored))).toEqual({
      from: '0.50.0',
      to: '0.51.0',
      closure: closure(),
      ts: 123,
    });
  });

  it('restores matching scans and marks mismatched version pairs stale', () => {
    const stored = storedClosureScan(closure(), 123);

    expect(restoreClosureScanForReport(report(), stored)).toEqual({
      closure: closure(),
      scannedAt: 123,
      stale: false,
    });
    expect(restoreClosureScanForReport(report({ to: '0.52.0' }), stored)).toEqual({
      closure: null,
      scannedAt: null,
      stale: true,
    });
  });
});

