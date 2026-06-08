import { describe, expect, it } from 'vitest';
import {
  AMBER_TIMEOUT_MS,
  applyHysteresis,
  FAILURE_THRESHOLD,
  initialHysteresis,
  rank,
  SUCCESS_THRESHOLD,
  worseOf,
  wireInstantState,
  type HysteresisState,
  type StateLevel,
  type SystemStatus,
} from './state';

const NOW = Date.parse('2026-06-01T00:00:00Z');

// =================================================================
// Ordering helpers
// =================================================================
describe('rank', () => {
  it('orders healthy < degraded < unhealthy', () => {
    expect(rank('healthy')).toBeLessThan(rank('degraded'));
    expect(rank('degraded')).toBeLessThan(rank('unhealthy'));
  });
});

describe('worseOf', () => {
  it('returns the worse of two states', () => {
    expect(worseOf('healthy', 'degraded')).toBe('degraded');
    expect(worseOf('degraded', 'unhealthy')).toBe('unhealthy');
    expect(worseOf('healthy', 'healthy')).toBe('healthy');
    expect(worseOf('unhealthy', 'healthy')).toBe('unhealthy');
  });
});

// =================================================================
// wireInstantState — wire-payload → instant state
// =================================================================
describe('wireInstantState', () => {
  // Helper: a minimal payload at a given verdict. Only the
  // computed.instant_state field matters for these tests; the
  // other fields are filler.
  function payload(state?: StateLevel | string): SystemStatus {
    return {
      version: 'v0.0.0-test',
      uptime_seconds: 0,
      mirror: {
        modules_indexed: 0,
        versions_indexed: 0,
        promote_on_serve_enabled: false,
      },
      federation: { upstreams: [] },
      drift: { modules_behind: 0, modules_yanked_upstream: 0 },
      addons: {
        promote_on_serve: false,
        snapshot_publishing: false,
        litestream: false,
        mcp_http: false,
      },
      computed:
        state === undefined ? undefined : { instant_state: state as StateLevel },
    };
  }

  it('returns the wire verdict when present and valid', () => {
    expect(wireInstantState(payload('healthy'))).toBe('healthy');
    expect(wireInstantState(payload('degraded'))).toBe('degraded');
    expect(wireInstantState(payload('unhealthy'))).toBe('unhealthy');
  });

  it('falls back to unhealthy when payload is null (fetch failed)', () => {
    expect(wireInstantState(null)).toBe('unhealthy');
  });

  it('falls back to unhealthy when the wire omits the computed block', () => {
    // An older server (pre-Computed deploy) won't ship this field.
    // We can't assume healthy when we can't verify.
    expect(wireInstantState(payload(undefined))).toBe('unhealthy');
  });

  it('falls back to unhealthy on an unrecognized state string', () => {
    // Defensive: if the wire ships "warning" or "" or any other
    // unmodeled value, treat it as failure.
    expect(wireInstantState(payload('warning'))).toBe('unhealthy');
    expect(wireInstantState(payload(''))).toBe('unhealthy');
  });
});

// =================================================================
// applyHysteresis — transition matrix
// =================================================================
describe('applyHysteresis', () => {
  it('initialHysteresis starts amber so first paint is honest', () => {
    expect(initialHysteresis().displayed).toBe('degraded');
  });

  // ----- same-state =====
  it('same level (healthy) clears any pending failure count', () => {
    const prev: HysteresisState = {
      displayed: 'healthy',
      consecutiveSuccesses: 0,
      consecutiveFailures: 2,
      amberEnteredAt: null,
    };
    const next = applyHysteresis(prev, 'healthy', NOW);
    expect(next.displayed).toBe('healthy');
    expect(next.consecutiveFailures).toBe(0);
  });
  it('same level (unhealthy) clears any pending success count', () => {
    const prev: HysteresisState = {
      displayed: 'unhealthy',
      consecutiveSuccesses: 2,
      consecutiveFailures: 0,
      amberEnteredAt: null,
    };
    const next = applyHysteresis(prev, 'unhealthy', NOW);
    expect(next.displayed).toBe('unhealthy');
    expect(next.consecutiveSuccesses).toBe(0);
  });
  it('same level (degraded) leaves counters untouched', () => {
    const prev: HysteresisState = {
      displayed: 'degraded',
      consecutiveSuccesses: 1,
      consecutiveFailures: 2,
      amberEnteredAt: NOW - 1000,
    };
    expect(applyHysteresis(prev, 'degraded', NOW)).toEqual(prev);
  });

  // ----- green → worse -----
  it('green → degraded is immediate, and enters amber', () => {
    const next = applyHysteresis(
      { ...initialHysteresis(), displayed: 'healthy' },
      'degraded',
      NOW,
    );
    expect(next.displayed).toBe('degraded');
    expect(next.consecutiveFailures).toBe(1);
    expect(next.amberEnteredAt).toBe(NOW);
  });
  it('green → unhealthy is capped at amber (never direct red)', () => {
    const next = applyHysteresis(
      { ...initialHysteresis(), displayed: 'healthy' },
      'unhealthy',
      NOW,
    );
    expect(next.displayed).toBe('degraded');
    expect(next.amberEnteredAt).toBe(NOW);
  });

  // ----- amber → green requires SUCCESS_THRESHOLD samples -----
  it('amber → green requires SUCCESS_THRESHOLD confirming probes', () => {
    let s: HysteresisState = initialHysteresis();
    for (let i = 1; i < SUCCESS_THRESHOLD; i++) {
      s = applyHysteresis(s, 'healthy', NOW + i);
      expect(s.displayed).toBe('degraded');
      expect(s.consecutiveSuccesses).toBe(i);
    }
    s = applyHysteresis(s, 'healthy', NOW + SUCCESS_THRESHOLD);
    expect(s.displayed).toBe('healthy');
    expect(s.amberEnteredAt).toBeNull();
  });
  it('amber → green resets on a failure midway', () => {
    let s: HysteresisState = initialHysteresis();
    s = applyHysteresis(s, 'healthy', NOW + 1);
    s = applyHysteresis(s, 'unhealthy', NOW + 2);
    expect(s.consecutiveSuccesses).toBe(0);
    expect(s.consecutiveFailures).toBe(1);
    expect(s.displayed).toBe('degraded');
  });

  // ----- amber → red via failure count -----
  it('amber → red requires FAILURE_THRESHOLD confirming failures', () => {
    let s: HysteresisState = { ...initialHysteresis(), amberEnteredAt: NOW };
    for (let i = 1; i < FAILURE_THRESHOLD; i++) {
      s = applyHysteresis(s, 'unhealthy', NOW + i);
      expect(s.displayed).toBe('degraded');
    }
    s = applyHysteresis(s, 'unhealthy', NOW + FAILURE_THRESHOLD);
    expect(s.displayed).toBe('unhealthy');
  });

  // ----- amber → red via timeout -----
  it('amber → red also fires when stuck in amber > AMBER_TIMEOUT_MS', () => {
    const s: HysteresisState = { ...initialHysteresis(), amberEnteredAt: NOW };
    // One unhealthy sample, but timing is well past timeout — escalates.
    const next = applyHysteresis(s, 'unhealthy', NOW + AMBER_TIMEOUT_MS + 1);
    expect(next.displayed).toBe('unhealthy');
  });

  // ----- red → anything always lands in amber -----
  it('red → healthy is forbidden direct; lands in amber with one success', () => {
    const prev: HysteresisState = {
      displayed: 'unhealthy',
      consecutiveSuccesses: 0,
      consecutiveFailures: 6,
      amberEnteredAt: null,
    };
    const next = applyHysteresis(prev, 'healthy', NOW);
    expect(next.displayed).toBe('degraded');
    expect(next.consecutiveSuccesses).toBe(1);
    expect(next.consecutiveFailures).toBe(0);
    expect(next.amberEnteredAt).toBe(NOW);
  });
  it('red → degraded also lands in amber with the recovery counter started', () => {
    const prev: HysteresisState = {
      displayed: 'unhealthy',
      consecutiveSuccesses: 0,
      consecutiveFailures: 6,
      amberEnteredAt: null,
    };
    const next = applyHysteresis(prev, 'degraded', NOW);
    expect(next.displayed).toBe('degraded');
    expect(next.consecutiveSuccesses).toBe(1);
  });

  // ----- purity -----
  it('is pure — does not mutate prev', () => {
    const prev: HysteresisState = initialHysteresis();
    const snapshot = { ...prev };
    applyHysteresis(prev, 'healthy', NOW);
    expect(prev).toEqual(snapshot);
  });
});

// =================================================================
// Integration: wire → hysteresis pipeline
// =================================================================
describe('wire → hysteresis pipeline', () => {
  it('three consecutive healthy wire ticks recover from initial amber', () => {
    let h: HysteresisState = initialHysteresis();
    const healthy: SystemStatus = {
      version: 'v',
      uptime_seconds: 0,
      mirror: { modules_indexed: 0, versions_indexed: 0, promote_on_serve_enabled: false },
      federation: { upstreams: [] },
      drift: { modules_behind: 0, modules_yanked_upstream: 0 },
      addons: { promote_on_serve: false, snapshot_publishing: false, litestream: false, mcp_http: false },
      computed: { instant_state: 'healthy' },
    };
    for (let i = 0; i < SUCCESS_THRESHOLD; i++) {
      h = applyHysteresis(h, wireInstantState(healthy), NOW + i);
    }
    expect(h.displayed).toBe('healthy');
  });

  it('a server reporting unhealthy escalates through amber after FAILURE_THRESHOLD ticks', () => {
    let h: HysteresisState = { ...initialHysteresis(), displayed: 'healthy' };
    const sick: SystemStatus = {
      version: 'v',
      uptime_seconds: 0,
      mirror: { modules_indexed: 0, versions_indexed: 0, promote_on_serve_enabled: false },
      federation: { upstreams: [] },
      drift: { modules_behind: 0, modules_yanked_upstream: 0 },
      addons: { promote_on_serve: false, snapshot_publishing: false, litestream: false, mcp_http: false },
      computed: { instant_state: 'unhealthy' },
    };
    // First sample: green → amber (immediate, capped at amber).
    h = applyHysteresis(h, wireInstantState(sick), NOW);
    expect(h.displayed).toBe('degraded');
    // Then FAILURE_THRESHOLD - 1 more confirming samples to reach red.
    for (let i = 1; i < FAILURE_THRESHOLD; i++) {
      h = applyHysteresis(h, wireInstantState(sick), NOW + i);
    }
    expect(h.displayed).toBe('unhealthy');
  });
});
