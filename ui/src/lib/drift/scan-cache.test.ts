import { describe, expect, it } from 'vitest';

import type { ModuleDrift } from '../api/types';
import {
  breakingScanStorageKey,
  parseStoredScans,
  restoreBreakingScans,
  withStoredScan,
} from './scan-cache';

function row(overrides: Partial<ModuleDrift> = {}): ModuleDrift {
  return {
    name: 'rules_go',
    status: 'behind',
    local_versions: ['0.50.0'],
    upstream_versions: ['0.51.0'],
    local_latest: '0.50.0',
    upstream_latest: '0.51.0',
    ...overrides,
  };
}

describe('breakingScanStorageKey', () => {
  it('scopes cache entries by upstream and mirror root', () => {
    expect(breakingScanStorageKey('https://bcr.bazel.build', { mirror_root: '/tmp/bzlhub' })).toBe(
      'bzlhub:breakingScans:https://bcr.bazel.build:/tmp/bzlhub',
    );
  });
});

describe('scan cache parsing and writing', () => {
  it('parses missing cache as an empty record', () => {
    expect(parseStoredScans(null)).toEqual({});
  });

  it('stores successful and failed scan results with version pins', () => {
    const success = withStoredScan({}, row(), 3, 123);
    expect(success.rules_go).toEqual({ from: '0.50.0', to: '0.51.0', count: 3, ts: 123 });

    const failed = withStoredScan(success, row({ name: 'rules_rust' }), 'error', 456);
    expect(failed.rules_rust).toEqual({ from: '0.50.0', to: '0.51.0', error: true, ts: 456 });
  });
});

describe('restoreBreakingScans', () => {
  it('restores scans for matching version pairs', () => {
    const restored = restoreBreakingScans([row()], {
      rules_go: { from: '0.50.0', to: '0.51.0', count: 0, ts: 123 },
    });

    expect(restored).toEqual({
      scans: { rules_go: 0 },
      stored: { rules_go: { from: '0.50.0', to: '0.51.0', count: 0, ts: 123 } },
      pruned: false,
    });
  });

  it('prunes stale scans when the drift row version pair moves', () => {
    const restored = restoreBreakingScans([row({ upstream_latest: '0.52.0' })], {
      rules_go: { from: '0.50.0', to: '0.51.0', count: 0, ts: 123 },
      rules_java: { from: '8.0.0', to: '8.1.0', error: true, ts: 456 },
    });

    expect(restored).toEqual({
      scans: {},
      stored: {
        rules_java: { from: '8.0.0', to: '8.1.0', error: true, ts: 456 },
      },
      pruned: true,
    });
  });
});

