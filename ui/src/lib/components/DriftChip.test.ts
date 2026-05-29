// DriftChip.test.ts — pin every derived state of the drift chip.
// Tests run against drift-chip.ts (pure presentation logic) per the
// project convention; the .svelte file is a thin wrapper that
// calls into chipState() and renders the result.

import { describe, test, expect } from 'vitest';
import { chipState, hoverTitle, ageAffix } from './drift-chip';

describe('chipState', () => {
  test('returns visible:false for empty drift (server default {})', () => {
    const s = chipState({});
    expect(s.visible).toBe(false);
    expect(s.status).toBe('unknown');
  });

  test('returns visible:false for explicit unknown', () => {
    const s = chipState({ status: 'unknown' });
    expect(s.visible).toBe(false);
  });

  test('returns visible:false for in-sync (silent green)', () => {
    const s = chipState({ status: 'in-sync' });
    expect(s.visible).toBe(false);
  });

  test('returns visible:false for upstream-error (silent; logged server-side)', () => {
    const s = chipState({ status: 'upstream-error' });
    expect(s.visible).toBe(false);
  });

  test('returns ↑N label for behind', () => {
    const s = chipState({ status: 'behind', behind: 4 });
    expect(s.visible).toBe(true);
    expect(s.label).toBe('↑4');
    expect(s.colorVar).toBe('var(--color-drift-behind)');
  });

  test('behind status with missing count renders silent', () => {
    // The 'behind' status MEANS "N newer exist." A `↑0` chip would
    // be a contract violation — better to hide than mislead. Guards
    // against a future drift source that sets status without count.
    const s = chipState({ status: 'behind' });
    expect(s.visible).toBe(false);
  });

  test('behind status with zero count renders silent', () => {
    const s = chipState({ status: 'behind', behind: 0 });
    expect(s.visible).toBe(false);
  });

  test('returns ⚠ yanked label for yanked-upstream', () => {
    const s = chipState({ status: 'yanked-upstream' });
    expect(s.visible).toBe(true);
    expect(s.label).toBe('⚠ yanked');
    expect(s.colorVar).toBe('var(--color-drift-yanked)');
  });

  test('returns local label for local-only', () => {
    const s = chipState({ status: 'local-only' });
    expect(s.visible).toBe(true);
    expect(s.label).toBe('local');
    expect(s.colorVar).toBe('var(--color-drift-local)');
  });
});

describe('hoverTitle', () => {
  // Frozen "now" for deterministic age math.
  const NOW = new Date('2026-05-28T14:00:00Z').getTime();

  test('behind: count + upstream-at + computed-at', () => {
    const t = hoverTitle(
      {
        status: 'behind',
        behind: 4,
        latest_upstream: '1.9.0',
        computed_at: '2026-05-28T10:00:00Z', // 4h ago
      },
      'behind',
      NOW,
    );
    expect(t).toContain('4 newer upstream');
    expect(t).toContain('upstream at 1.9.0');
    expect(t).toContain('computed 4h ago');
  });

  test('behind: omits upstream-at when absent', () => {
    const t = hoverTitle({ status: 'behind', behind: 2 }, 'behind', NOW);
    expect(t).toContain('2 newer upstream');
    expect(t).not.toContain('upstream at');
  });

  test('behind: omits computed affix when computed_at absent', () => {
    const t = hoverTitle({ status: 'behind', behind: 1 }, 'behind', NOW);
    expect(t).not.toContain('computed');
  });

  test('yanked-upstream: alarm copy + as-of', () => {
    const t = hoverTitle(
      { status: 'yanked-upstream', computed_at: '2026-05-27T14:00:00Z' }, // 24h
      'yanked-upstream',
      NOW,
    );
    expect(t).toContain('yanked this version');
    expect(t).toContain('computed 24h ago');
  });

  test('local-only: copy is fixed (no as-of, since no upstream to be relative to)', () => {
    const t = hoverTitle({ status: 'local-only' }, 'local-only', NOW);
    expect(t).toBe('Not present upstream (local-only module)');
  });

  test('returns empty string for silent states', () => {
    expect(hoverTitle({}, 'unknown', NOW)).toBe('');
    expect(hoverTitle({ status: 'in-sync' }, 'in-sync', NOW)).toBe('');
  });
});

describe('ageAffix', () => {
  const NOW = new Date('2026-05-28T14:00:00Z').getTime();

  test('returns empty for absent timestamp', () => {
    expect(ageAffix(undefined, NOW)).toBe('');
  });

  test('returns empty for unparseable timestamp', () => {
    expect(ageAffix('not-a-date', NOW)).toBe('');
  });

  test('< 90s reads as "just now"', () => {
    const computed = new Date(NOW - 30 * 1000).toISOString();
    expect(ageAffix(computed, NOW)).toBe('just now');
  });

  test('minutes branch: 5m', () => {
    const computed = new Date(NOW - 5 * 60 * 1000).toISOString();
    expect(ageAffix(computed, NOW)).toBe('5m ago');
  });

  test('hours branch: 4h', () => {
    const computed = new Date(NOW - 4 * 60 * 60 * 1000).toISOString();
    expect(ageAffix(computed, NOW)).toBe('4h ago');
  });

  test('days branch: 3d', () => {
    const computed = new Date(NOW - 3 * 24 * 60 * 60 * 1000).toISOString();
    expect(ageAffix(computed, NOW)).toBe('3d ago');
  });

  test('weeks branch: 6w', () => {
    const computed = new Date(NOW - 42 * 24 * 60 * 60 * 1000).toISOString();
    expect(ageAffix(computed, NOW)).toBe('6w ago');
  });

  test('future timestamp clamps to "just now" (no negative ages)', () => {
    const computed = new Date(NOW + 5000).toISOString();
    expect(ageAffix(computed, NOW)).toBe('just now');
  });
});
