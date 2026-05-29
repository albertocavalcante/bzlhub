import { describe, expect, it } from 'vitest';

import type { ModuleDrift } from '../api/types';
import { classifyDriftModule, classifyVersion } from './classify';

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

describe('classifyVersion', () => {
  it('extracts the major version and prerelease flag', () => {
    expect(classifyVersion('8.1.0')).toEqual({ major: 8, pre: false });
    expect(classifyVersion('8.1.0-rc1')).toEqual({ major: 8, pre: true });
    expect(classifyVersion('8.1.0.bcr.1')).toEqual({ major: 8, pre: true });
    expect(classifyVersion('not-semver')).toEqual({ major: -1, pre: false });
  });
});

describe('classifyDriftModule', () => {
  it('skips rows that are not directly advanceable', () => {
    expect(classifyDriftModule(row({ status: 'in-sync' })).tier).toBe('skipped');
    expect(classifyDriftModule(row({ status: 'local-only' })).tier).toBe('skipped');
    expect(classifyDriftModule(row({ status: 'yanked-upstream' })).tier).toBe('skipped');
    expect(classifyDriftModule(row({ status: 'upstream-error' })).tier).toBe('skipped');
  });

  it('uses scan results as ground truth when present', () => {
    expect(classifyDriftModule(row(), { rules_go: 0 })).toEqual({
      tier: 'auto-safe',
      reason: 'what-if scan: no structural breaks detected',
    });
    expect(classifyDriftModule(row(), { rules_go: 2 })).toEqual({
      tier: 'review',
      reason: 'what-if scan: 2 structurally-breaking findings — open the diff',
    });
  });

  it('falls back to conservative semver heuristics', () => {
    expect(classifyDriftModule(row()).tier).toBe('auto-safe');
    expect(classifyDriftModule(row({ local_latest: '1.9.0', upstream_latest: '2.0.0' })).tier).toBe('review');
    expect(classifyDriftModule(row({ upstream_latest: '0.52.0-rc1' })).tier).toBe('review');
    expect(classifyDriftModule(row({ upstream_latest: 'rolling' })).tier).toBe('review');
  });
});

