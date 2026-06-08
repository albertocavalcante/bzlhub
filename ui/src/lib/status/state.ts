/**
 * Status-page state machine. Pure functions; the +page.svelte
 * component owns reactive state and rendering, this module owns
 * the wire-shape contract and the hysteresis (anti-flapping)
 * transition machine.
 *
 * Where the thresholds live
 *
 *   The /status page's INSTANT health verdict ("is this install
 *   healthy / degraded / unhealthy right now?") is computed
 *   server-side by `internal/bzlhub/health.Derive` and shipped on
 *   the wire as `computed.instant_state`. The browser does NOT
 *   recompute it — single source of truth, zero TS/Go drift.
 *
 *   What stays here is purely a RENDER concern: hysteresis. The
 *   server doesn't know how many ticks the browser has observed,
 *   so the smoothing (amber → green needs N consecutive healthy
 *   probes, etc.) lives client-side. The SUCCESS/FAILURE/
 *   AMBER_TIMEOUT thresholds below are about that browser-side
 *   smoothing, not about the underlying health verdict.
 */

// ---- Wire contract (mirrors internal/api.SystemStatus) ------------

export type UpstreamStatus = {
  url: string;
  reachable: boolean;
  last_probe_at?: string;
  last_probe_latency_ms?: number;
  last_probe_error?: string;
  cache_entries: number;
  cache_hit_rate: number;
};

export type SystemStatus = {
  version: string;
  commit?: string;
  built_at?: string;
  uptime_seconds: number;
  mirror: {
    modules_indexed: number;
    versions_indexed: number;
    size_bytes?: number;
    last_ingest_at?: string;
    last_sync_at?: string;
    head_sha?: string;
    promote_on_serve_enabled: boolean;
  };
  federation: { upstreams: UpstreamStatus[] };
  drift: {
    last_refresh_at?: string;
    modules_behind: number;
    modules_yanked_upstream: number;
  };
  addons: {
    promote_on_serve: boolean;
    snapshot_publishing: boolean;
    litestream: boolean;
    mcp_http: boolean;
  };
  /**
   * Server-derived signals. `instant_state` is the worst-
   * contributing-signal verdict at request time, populated by
   * `internal/bzlhub/health.Derive`. `signals` is the unordered
   * list of every check that contributed to a non-healthy
   * state — empty/omitted when healthy. Optional in the type
   * so an older server (pre-Computed-block deploy) doesn't
   * break the UI — see `wireInstantState`'s fallback.
   */
  computed?: {
    instant_state?: StateLevel;
    signals?: Signal[];
  };
};

/**
 * One contributing reason behind a non-healthy `instant_state`.
 * Mirrors `internal/api.Signal`. Kind values are part of the
 * public wire schema — adding new Kinds is non-breaking; renaming
 * needs a deprecation cycle.
 */
export type Signal = {
  kind: string;
  level: 'degraded' | 'unhealthy';
  detail: string;
};

export type StateLevel = 'healthy' | 'degraded' | 'unhealthy';

// ---- Polling cadence ----------------------------------------------

/** Page-poll cadence; matches the server's federation probe rhythm. */
export const POLL_INTERVAL_MS = 15_000;

// ---- Hysteresis thresholds (client-side, RENDER concern only) -----

/** Hysteresis: probes-to-recover (amber → green). */
export const SUCCESS_THRESHOLD = 3;
/** Hysteresis: probes-to-escalate (amber → red). */
export const FAILURE_THRESHOLD = 5;
/** Hysteresis: wall-clock cap on amber; pathological-but-not-blip. */
export const AMBER_TIMEOUT_MS = 5 * 60_000;

// ---- Ordering helpers ---------------------------------------------

export function rank(s: StateLevel): number {
  return s === 'healthy' ? 0 : s === 'degraded' ? 1 : 2;
}

export function worseOf(a: StateLevel, b: StateLevel): StateLevel {
  return rank(a) >= rank(b) ? a : b;
}

// ---- Wire → instant state -----------------------------------------

/**
 * Reads `computed.instant_state` off the wire payload. Falls back
 * to 'unhealthy' on:
 *
 *   - null payload (fetch failed, page hasn't loaded yet)
 *   - missing `computed` block (older server, pre-health-package
 *     deploy — we can't assume healthy when we can't verify)
 *   - unrecognized state string (shouldn't happen against a
 *     matched-version server, but treat as failure mode)
 *
 * The "unhealthy" fallback for missing data is intentional: a UI
 * that can't read a verdict shouldn't claim green. Hysteresis on
 * top means a single missing-data tick doesn't immediately flip
 * the page red — the same anti-flapping rules apply.
 */
export function wireInstantState(s: SystemStatus | null): StateLevel {
  const raw = s?.computed?.instant_state;
  if (raw === 'healthy' || raw === 'degraded' || raw === 'unhealthy') {
    return raw;
  }
  return 'unhealthy';
}

// ---- Hysteresis ----------------------------------------------------

export type HysteresisState = {
  displayed: StateLevel;
  consecutiveSuccesses: number;
  consecutiveFailures: number;
  /** Wall-clock ms when amber was entered; null when not in amber. */
  amberEnteredAt: number | null;
};

/**
 * The default initial state. Starts AMBER so the page doesn't
 * theatrically claim "healthy" before the first probe lands.
 */
export function initialHysteresis(): HysteresisState {
  return {
    displayed: 'degraded',
    consecutiveSuccesses: 0,
    consecutiveFailures: 0,
    amberEnteredAt: null,
  };
}

/**
 * Threshold-smoothed transition. Pure: takes prev state + this
 * tick's instant level + now, returns the new state.
 *
 * Contract:
 *   - Same level ⇒ reset the OPPOSITE-direction counter. A green
 *     bounce after a real failure shouldn't carry into recovery.
 *   - Red → anything → first sample lands in amber. Red→green is
 *     never direct; escalation/recovery always crosses amber.
 *   - Amber → green requires SUCCESS_THRESHOLD consecutive healthy
 *     samples.
 *   - Amber → red requires FAILURE_THRESHOLD consecutive unhealthy
 *     samples OR AMBER_TIMEOUT_MS in amber (whichever first).
 *   - Green → worse is immediate (visitors should see degradation
 *     start), but capped at amber — never green→red.
 */
export function applyHysteresis(
  prev: HysteresisState,
  instant: StateLevel,
  now: number,
): HysteresisState {
  // Same state: reset opposite counter so single bounces don't
  // accumulate toward a transition that hasn't been confirmed.
  if (instant === prev.displayed) {
    if (prev.displayed === 'healthy') {
      return { ...prev, consecutiveFailures: 0 };
    }
    if (prev.displayed === 'unhealthy') {
      return { ...prev, consecutiveSuccesses: 0 };
    }
    return prev;
  }

  // Recovering from red: always land in amber with a single
  // success credited. Red → green direct is never allowed.
  if (prev.displayed === 'unhealthy') {
    return {
      displayed: 'degraded',
      consecutiveSuccesses: 1,
      consecutiveFailures: 0,
      amberEnteredAt: now,
    };
  }

  if (prev.displayed === 'degraded') {
    if (instant === 'healthy') {
      const consecutiveSuccesses = prev.consecutiveSuccesses + 1;
      if (consecutiveSuccesses >= SUCCESS_THRESHOLD) {
        return {
          displayed: 'healthy',
          consecutiveSuccesses,
          consecutiveFailures: 0,
          amberEnteredAt: null,
        };
      }
      return {
        ...prev,
        consecutiveSuccesses,
        consecutiveFailures: 0,
      };
    }
    // instant === 'unhealthy'
    const consecutiveFailures = prev.consecutiveFailures + 1;
    const stuckTooLong =
      prev.amberEnteredAt !== null && now - prev.amberEnteredAt > AMBER_TIMEOUT_MS;
    if (consecutiveFailures >= FAILURE_THRESHOLD || stuckTooLong) {
      return {
        displayed: 'unhealthy',
        consecutiveSuccesses: 0,
        consecutiveFailures,
        amberEnteredAt: prev.amberEnteredAt,
      };
    }
    return {
      ...prev,
      consecutiveFailures,
      consecutiveSuccesses: 0,
    };
  }

  // prev.displayed === 'healthy' and instant is worse: green → amber
  // is immediate, but capped at amber. Never green → red direct;
  // escalation always crosses amber so visitors see the transition.
  return {
    displayed: worseOf(prev.displayed, 'degraded'),
    consecutiveSuccesses: 0,
    consecutiveFailures: 1,
    amberEnteredAt: now,
  };
}
