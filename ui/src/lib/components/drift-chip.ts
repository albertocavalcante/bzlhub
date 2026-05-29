// drift-chip.ts — pure presentation logic for DriftChip.svelte.
//
// Extracted from the .svelte module-block so vitest can unit-test
// without pulling in the Svelte vite plugin (matching the hover-
// card.ts precedent established in the May-26 organization pass).
// The .svelte file is a thin wrapper around the functions here.

import type { DriftSummary } from '$lib/api/client';

/**
 * ChipState is the rendered shape of one drift signal. `visible:false`
 * means render no DOM at all — the design discipline is signal-by-
 * absence (Plan 19 Idea A): a silent chip means "no actionable drift",
 * not "we lost the signal."
 */
export interface ChipState {
  visible: boolean;
  status: 'unknown' | 'in-sync' | 'behind' | 'yanked-upstream' | 'local-only' | 'upstream-error';
  label: string;
  colorVar: string; // CSS var() expression, e.g. 'var(--color-drift-behind)'
  title: string; // hover affordance
}

/**
 * chipState derives all rendered properties from one DriftSummary
 * in one pass. The Svelte component does no further derivation —
 * everything flows from this function. `now` is injectable for
 * deterministic tests.
 */
export function chipState(drift: DriftSummary, now: number = Date.now()): ChipState {
  const status = drift.status ?? 'unknown';

  // 'behind' with a missing or zero count is incoherent: the status
  // MEANS "N newer exist." A `↑0` chip would be a contract violation.
  // We render silent in that case — better to hide than mislead.
  const behindHasCount = status === 'behind' && (drift.behind ?? 0) > 0;
  const visible =
    behindHasCount || status === 'yanked-upstream' || status === 'local-only';

  let label = '';
  let colorVar = 'var(--color-fg-dim)';
  switch (status) {
    case 'behind':
      label = `↑${drift.behind ?? 0}`;
      colorVar = 'var(--color-drift-behind)';
      break;
    case 'yanked-upstream':
      label = '⚠ yanked';
      colorVar = 'var(--color-drift-yanked)';
      break;
    case 'local-only':
      label = 'local';
      colorVar = 'var(--color-drift-local)';
      break;
  }

  const title = hoverTitle(drift, status, now);

  return { visible, status, label, colorVar, title };
}

/**
 * hoverTitle builds the human-readable affordance text. Exported
 * for direct testing of the as-of formatting branches.
 */
export function hoverTitle(drift: DriftSummary, status: string, now: number = Date.now()): string {
  if (status === 'behind') {
    const n = drift.behind ?? 0;
    const upstream = drift.latest_upstream ? `; upstream at ${drift.latest_upstream}` : '';
    const asof = ageAffix(drift.computed_at, now);
    const asofPart = asof ? ` — computed ${asof}` : '';
    return `${n} newer upstream${upstream}${asofPart}`;
  }
  if (status === 'yanked-upstream') {
    const asof = ageAffix(drift.computed_at, now);
    const asofPart = asof ? ` — computed ${asof}` : '';
    return `Upstream yanked this version${asofPart}`;
  }
  if (status === 'local-only') {
    return 'Not present upstream (local-only module)';
  }
  return '';
}

/**
 * ageAffix renders the "X ago" suffix for a RFC3339 timestamp.
 * Bounded to the largest unit (no chained durations). Returns
 * empty string when the timestamp is absent or unparseable —
 * the hover text composes around an empty affix gracefully.
 *
 * Thresholds chosen so the boundary case (1.5x the unit) prefers
 * the larger unit, matching human expectation: "90 minutes" reads
 * as "1h" once the gap is even slightly past 90 min.
 */
export function ageAffix(computedAt: string | undefined, now: number = Date.now()): string {
  if (!computedAt) return '';
  const t = new Date(computedAt).getTime();
  if (!Number.isFinite(t)) return '';
  const seconds = Math.max(0, (now - t) / 1000);
  if (seconds < 90) return 'just now';
  const minutes = seconds / 60;
  if (minutes < 90) return `${Math.round(minutes)}m ago`;
  const hours = minutes / 60;
  if (hours < 36) return `${Math.round(hours)}h ago`;
  const days = hours / 24;
  if (days < 14) return `${Math.round(days)}d ago`;
  const weeks = days / 7;
  return `${Math.round(weeks)}w ago`;
}
