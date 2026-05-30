<!--
  /status — live operational snapshot of this bzlhub instance.

  Reads /api/v1/system/status (Cache-Control: no-store) every 15s.
  Polling pauses when the tab is hidden (Page Visibility API) and
  resumes on visibility change. No invented metrics — every field
  on the page is something canopy already tracks.

  Contract + design: docs/plans/65-about-and-status-content-spec.md
  §Part 3. Backend handler: internal/server/system_handlers.go::apiStatus.

  Anti-slop discipline (per plan-65 v2):
    - Colour is decoration; state is conveyed by icon AND text too,
      so colourblind / high-contrast readers get the same information.
    - When amber or red, the page renders MORE information (which
      upstream is slow, last error, drift list), not less.
    - Hysteresis prevents flapping: green→amber is immediate, but
      amber→red requires 5 consecutive failures OR 5 minutes in amber,
      and amber→green requires 3 consecutive successes. During a
      transition the page shows the WORSE of (current actual) vs
      (displayed), so we never claim recovery faster than reality.
-->
<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { paths } from '$lib/api/paths';

  // ---- JSON wire shape (mirrors api.SystemStatus) -----------------

  type UpstreamStatus = {
    url: string;
    reachable: boolean;
    last_probe_at?: string;
    last_probe_latency_ms?: number;
    last_probe_error?: string;
    cache_entries: number;
    cache_hit_rate: number;
  };
  type SystemStatus = {
    version: string;
    commit?: string;
    built_at?: string;
    uptime_seconds: number;
    mirror: {
      modules_indexed: number;
      versions_indexed: number;
      size_bytes?: number;
      last_ingest_at?: string;
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
  };

  type StateLevel = 'healthy' | 'degraded' | 'unhealthy';

  // ---- Polling config + hysteresis thresholds ---------------------
  // Match the federation probe cadence so the page never shows
  // data the server itself hasn't refreshed since last tick.
  const POLL_INTERVAL_MS = 15_000;
  // Amber → Green requires 3 consecutive healthy snapshots — one
  // green bounce after a real failure is not recovery.
  const SUCCESS_THRESHOLD = 3;
  // Amber → Red requires 5 consecutive unhealthy snapshots — short
  // upstream blips shouldn't escalate to red on the public page.
  const FAILURE_THRESHOLD = 5;
  // Amber → Red also triggers when we've been stuck in amber for
  // five minutes — pathological-but-not-blip patterns escalate.
  const AMBER_TIMEOUT_MS = 5 * 60_000;
  // Steady-state thresholds (plan-65 v2 §Part 3).
  const SLOW_PROBE_MS = 500;
  const MIRROR_STALE_DAYS_AMBER = 7;
  const MIRROR_STALE_DAYS_RED = 30;
  const DRIFT_COUNT_AMBER = 5;
  const DRIFT_COUNT_RED = 20;

  // ---- Reactive state ---------------------------------------------

  let status = $state<SystemStatus | null>(null);
  let fetchError = $state<string | null>(null);
  let lastFetchAt = $state<Date | null>(null);
  // The HYSTERESIS-smoothed state — what we render. Starts amber so
  // we don't theatrically claim "healthy" before the first probe.
  let displayedState = $state<StateLevel>('degraded');
  // Counters used by the hysteresis state machine.
  let consecutiveFailures = $state(0);
  let consecutiveSuccesses = $state(0);
  let amberEnteredAt: number | null = null;
  // Driver for "now" — updates every second so relative-time
  // strings ("35s ago") tick without waiting for the next /status
  // poll. Cheap.
  let nowTick = $state(Date.now());
  let pollHandle = $state<ReturnType<typeof setInterval> | undefined>(undefined);
  let tickHandle: ReturnType<typeof setInterval> | undefined;

  // ---- Helpers ----------------------------------------------------

  function rank(s: StateLevel): number {
    return s === 'healthy' ? 0 : s === 'degraded' ? 1 : 2;
  }
  function worseOf(a: StateLevel, b: StateLevel): StateLevel {
    return rank(a) >= rank(b) ? a : b;
  }

  function computeInstantState(s: SystemStatus | null): StateLevel {
    if (!s) return 'unhealthy';
    const ups = s.federation?.upstreams ?? [];
    const anyUnreachable = ups.some((u) => !u.reachable);
    const anySlow = ups.some(
      (u) => u.reachable && (u.last_probe_latency_ms ?? 0) > SLOW_PROBE_MS,
    );

    const lastIngest = s.mirror?.last_ingest_at
      ? new Date(s.mirror.last_ingest_at)
      : null;
    const ageDays =
      lastIngest === null ? null : (Date.now() - lastIngest.getTime()) / 86_400_000;

    const driftCount =
      (s.drift?.modules_behind ?? 0) + (s.drift?.modules_yanked_upstream ?? 0);

    if (anyUnreachable) return 'unhealthy';
    if (ageDays !== null && ageDays > MIRROR_STALE_DAYS_RED) return 'unhealthy';
    if (driftCount > DRIFT_COUNT_RED) return 'unhealthy';

    if (anySlow) return 'degraded';
    if (ageDays !== null && ageDays >= MIRROR_STALE_DAYS_AMBER) return 'degraded';
    if (driftCount >= DRIFT_COUNT_AMBER) return 'degraded';

    return 'healthy';
  }

  function applyHysteresis(prev: StateLevel, instant: StateLevel): StateLevel {
    // Same-state: reset the counter for the OPPOSITE direction so a
    // single bounce on the recovery side doesn't carry into a real
    // recovery later.
    if (instant === prev) {
      if (prev === 'healthy') consecutiveFailures = 0;
      else if (prev === 'unhealthy') consecutiveSuccesses = 0;
      return prev;
    }

    // Improving: prev=unhealthy → instant healthier
    if (prev === 'unhealthy') {
      // Red → Amber on FIRST recovery sample (show that recovery has
      // begun); Red → Green is never direct — must pass through amber.
      consecutiveSuccesses = 1;
      amberEnteredAt = Date.now();
      return 'degraded';
    }
    if (prev === 'degraded') {
      if (instant === 'healthy') {
        consecutiveSuccesses += 1;
        consecutiveFailures = 0;
        if (consecutiveSuccesses >= SUCCESS_THRESHOLD) {
          amberEnteredAt = null;
          return 'healthy';
        }
        return 'degraded';
      }
      if (instant === 'unhealthy') {
        consecutiveFailures += 1;
        consecutiveSuccesses = 0;
        const stuckTooLong =
          amberEnteredAt !== null && Date.now() - amberEnteredAt > AMBER_TIMEOUT_MS;
        if (consecutiveFailures >= FAILURE_THRESHOLD || stuckTooLong) {
          return 'unhealthy';
        }
        return 'degraded';
      }
    }

    // prev === 'healthy' and instant is worse: green → amber is
    // immediate (visitors should see degradation start). Never jump
    // straight to red from green — escalation goes through amber.
    consecutiveFailures = 1;
    consecutiveSuccesses = 0;
    amberEnteredAt = Date.now();
    return worseOf(prev, 'degraded');
  }

  async function tick() {
    try {
      const res = await fetch(paths.system.status(), {
        headers: { accept: 'application/json' },
        cache: 'no-store',
      });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const json = (await res.json()) as SystemStatus;
      status = json;
      fetchError = null;
      lastFetchAt = new Date();
      displayedState = applyHysteresis(displayedState, computeInstantState(json));
    } catch (e) {
      fetchError = e instanceof Error ? e.message : String(e);
      // A polling failure is itself an unhealthy signal — the server
      // is at least partially unreachable from where the browser sits.
      displayedState = applyHysteresis(displayedState, 'unhealthy');
    }
  }

  function startPolling() {
    if (pollHandle !== undefined) return;
    void tick();
    pollHandle = setInterval(() => void tick(), POLL_INTERVAL_MS);
  }
  function stopPolling() {
    if (pollHandle !== undefined) {
      clearInterval(pollHandle);
      pollHandle = undefined;
    }
  }
  function onVisibilityChange() {
    if (typeof document === 'undefined') return;
    if (document.hidden) stopPolling();
    else startPolling();
  }

  onMount(() => {
    document.addEventListener('visibilitychange', onVisibilityChange);
    tickHandle = setInterval(() => (nowTick = Date.now()), 1000);
    startPolling();
  });
  onDestroy(() => {
    if (typeof document !== 'undefined') {
      document.removeEventListener('visibilitychange', onVisibilityChange);
    }
    stopPolling();
    if (tickHandle !== undefined) clearInterval(tickHandle);
  });

  // ---- Display formatting -----------------------------------------

  function fmtUptime(secs: number): string {
    if (secs < 60) return `${secs}s`;
    const m = Math.floor(secs / 60);
    if (m < 60) return `${m}m`;
    const h = Math.floor(m / 60);
    const rem = m % 60;
    if (h < 24) return `${h}h ${rem}m`;
    const d = Math.floor(h / 24);
    return `${d}d ${h % 24}h`;
  }
  function fmtRelative(iso: string | undefined | null, now: number): string {
    if (!iso) return 'unknown';
    const then = new Date(iso).getTime();
    if (Number.isNaN(then)) return iso;
    const secs = Math.max(0, Math.floor((now - then) / 1000));
    if (secs < 60) return `${secs}s ago`;
    const m = Math.floor(secs / 60);
    if (m < 60) return `${m}m ago`;
    const h = Math.floor(m / 60);
    if (h < 24) return `${h}h ago`;
    const d = Math.floor(h / 24);
    return `${d}d ago`;
  }
  function fmtDateOnly(iso: string | undefined | null): string {
    if (!iso) return '';
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return iso;
    return d.toISOString().slice(0, 10);
  }
  function fmtBytes(n: number | undefined): string {
    if (!n) return '';
    const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
    let i = 0;
    let v = n;
    while (v >= 1024 && i < units.length - 1) {
      v /= 1024;
      i++;
    }
    return `${v.toFixed(v >= 100 ? 0 : 1)} ${units[i]}`;
  }
  function fmtHostname(u: string): string {
    try {
      return new URL(u).host;
    } catch {
      return u;
    }
  }

  // ---- Derived UI bits --------------------------------------------

  const stateIcon = $derived(
    displayedState === 'healthy' ? '✓' : displayedState === 'degraded' ? '⚠' : '✗',
  );
  const stateLabel = $derived(
    displayedState === 'healthy'
      ? 'healthy'
      : displayedState === 'degraded'
        ? 'degraded'
        : 'unhealthy',
  );
  // Are we in a transition (the instant state differs from displayed)?
  const inTransition = $derived.by(() => {
    if (!status) return false;
    return computeInstantState(status) !== displayedState;
  });
  // During recovery (instant healthier than displayed), surface how
  // many confirming probes we've seen so far so the reader knows the
  // amber checkmark isn't theatre — it's "recovering, almost there."
  const transitionHint = $derived.by(() => {
    if (!inTransition || !status) return '';
    const instant = computeInstantState(status);
    if (rank(instant) < rank(displayedState)) {
      return `recovering — ${consecutiveSuccesses} of ${SUCCESS_THRESHOLD} confirming probes`;
    }
    if (rank(instant) > rank(displayedState)) {
      return `degrading — ${consecutiveFailures} of ${FAILURE_THRESHOLD} confirming failures`;
    }
    return '';
  });

  const driftItems = $derived.by(() => {
    if (!status) return [] as string[];
    const out: string[] = [];
    if (status.drift.modules_behind > 0) out.push(`${status.drift.modules_behind} behind`);
    if (status.drift.modules_yanked_upstream > 0)
      out.push(`${status.drift.modules_yanked_upstream} yanked-upstream`);
    return out;
  });

  const enabledAddons = $derived.by(() => {
    if (!status) return [] as string[];
    const a = status.addons;
    return [
      a.promote_on_serve && 'promote-on-serve',
      a.snapshot_publishing && 'snapshot',
      a.litestream && 'litestream',
      a.mcp_http && 'mcp-http',
    ].filter((v): v is string => Boolean(v));
  });
</script>

<svelte:head>
  <!--
    Defensive: the Go side injects per-route head tags via the
    HEADTAGS-SENTINEL pipeline. The SvelteKit dev server (pnpm dev)
    skips that pipeline, so this <svelte:head> block gives the same
    title in dev as in prod. The Go injection still wins in prod
    because it runs at the SPA fallback and the browser honours the
    last <title>.
  -->
  <title>Status · bzlhub</title>
</svelte:head>

<noscript>
  <!--
    bzlhub is a SvelteKit SPA (adapter-static + ssr=false). With JS
    disabled, polling does nothing — so we surface the raw JSON
    endpoint as the honest fallback. Plan-65 v2 §Part 3 option (b).
  -->
  <div class="rounded border border-line bg-bg-elev p-3 mb-4 text-sm">
    JavaScript is required for the live view. Raw status data:
    <a href="/api/v1/system/status" class="text-accent hover:underline">
      /api/v1/system/status
    </a>
  </div>
</noscript>

<article class="mx-auto max-w-[860px] py-2">
  <!-- Title strip with live-state pill. aria-live=polite so screen
       readers announce state changes when polling refreshes them. -->
  <header class="flex items-baseline justify-between gap-4 mb-6">
    <h1 class="font-mono text-base text-fg">bzlhub.com — status</h1>
    <div
      class="font-mono text-xs flex items-center gap-2"
      aria-live="polite"
      aria-atomic="true"
    >
      <span
        class="inline-block w-2 h-2 rounded-full"
        class:bg-ok={displayedState === 'healthy'}
        class:bg-warn={displayedState === 'degraded'}
        class:bg-err={displayedState === 'unhealthy'}
        aria-hidden="true"
      ></span>
      <span class="text-fg-mute">{stateLabel}</span>
      {#if pollHandle !== undefined}
        <span class="text-fg-dim ml-2">live</span>
      {:else}
        <span class="text-fg-dim ml-2">paused</span>
      {/if}
    </div>
  </header>

  {#if transitionHint}
    <p
      class="font-mono text-xs text-fg-mute mb-4 px-3 py-2 rounded border border-line bg-bg-elev"
      aria-live="polite"
    >
      {transitionHint}
    </p>
  {/if}

  {#if fetchError && !status}
    <p class="font-mono text-xs text-err mb-4">
      could not fetch status: {fetchError}
    </p>
  {/if}

  {#if status}
    <dl class="status-table">
      <!-- Service -->
      <div class="row">
        <dt class="label">Service</dt>
        <dd class="value">
          <div class="line">
            <span class="icon" aria-hidden="true">{stateIcon}</span>
            <span class="primary">{stateLabel}</span>
            <span class="sep">·</span>
            <span class="muted">uptime {fmtUptime(status.uptime_seconds)}</span>
          </div>
          <div class="sub">
            build
            {status.version}
            {#if status.commit}
              · <span class="font-mono">{status.commit}</span>
            {/if}
            {#if status.built_at && status.built_at !== 'unknown'}
              · {status.built_at}
            {/if}
          </div>
        </dd>
      </div>

      <!-- Mirror -->
      <div class="row">
        <dt class="label">Mirror</dt>
        <dd class="value">
          <div class="line">
            <span class="primary">
              {status.mirror.modules_indexed.toLocaleString()} modules
            </span>
            <span class="sep">·</span>
            <span class="primary">
              {status.mirror.versions_indexed.toLocaleString()} versions
            </span>
            {#if status.mirror.size_bytes}
              <span class="sep">·</span>
              <span class="primary">{fmtBytes(status.mirror.size_bytes)}</span>
            {/if}
          </div>
          <div class="sub">
            {#if status.mirror.last_ingest_at}
              last ingest {fmtRelative(status.mirror.last_ingest_at, nowTick)}
              <span class="muted">
                ({fmtDateOnly(status.mirror.last_ingest_at)})
              </span>
            {:else}
              no ingests yet
            {/if}
          </div>
          <div class="sub muted">
            promote-on-serve: {status.mirror.promote_on_serve_enabled
              ? 'on'
              : 'off'}
          </div>
        </dd>
      </div>

      <!-- Federation -->
      <div class="row">
        <dt class="label">Federation</dt>
        <dd class="value">
          {#if status.federation.upstreams.length === 0}
            <div class="line muted">not configured</div>
          {:else}
            {#each status.federation.upstreams as u (u.url)}
              <div class="line">
                <span class="icon" aria-hidden="true">{u.reachable ? '✓' : '✗'}</span>
                <span
                  class="primary"
                  class:text-err={!u.reachable}
                  class:text-warn={u.reachable &&
                    (u.last_probe_latency_ms ?? 0) > SLOW_PROBE_MS}
                >
                  {fmtHostname(u.url)}
                </span>
                {#if u.reachable && u.last_probe_latency_ms !== undefined}
                  <span class="sep">·</span>
                  <span class="muted">{u.last_probe_latency_ms}ms</span>
                {/if}
                {#if u.last_probe_at}
                  <span class="sep">·</span>
                  <span class="muted">last probe {fmtRelative(u.last_probe_at, nowTick)}</span>
                {/if}
              </div>
              {#if !u.reachable && u.last_probe_error}
                <!--
                  Anti-slop: when a probe fails, render the verbatim
                  error string. "DNS failure" vs "TLS handshake" vs
                  "HTTP 502" each suggest different remediation; the
                  reader (operator or visitor) deserves the truth.
                -->
                <div class="sub error">last error: {u.last_probe_error}</div>
              {/if}
              <div class="sub muted">
                cache: {u.cache_entries} entries
                {#if u.cache_entries === 0}
                  · 0% hit rate <span class="text-fg-dim">(warming)</span>
                {:else}
                  · {(u.cache_hit_rate * 100).toFixed(0)}% hit rate
                {/if}
              </div>
            {/each}
          {/if}
        </dd>
      </div>

      <!-- Drift -->
      <div class="row">
        <dt class="label">Drift</dt>
        <dd class="value">
          <div class="line">
            {#if driftItems.length === 0}
              <span class="icon" aria-hidden="true">✓</span>
              <span class="primary">in sync with upstream</span>
            {:else}
              <span class="icon" aria-hidden="true">⚠</span>
              <span class="primary">{driftItems.join(' · ')}</span>
              <span class="sep">·</span>
              <a class="link" href="/drift">view drift →</a>
            {/if}
          </div>
          {#if status.drift.last_refresh_at}
            <div class="sub muted">
              last refresh {fmtRelative(status.drift.last_refresh_at, nowTick)}
            </div>
          {/if}
        </dd>
      </div>

      <!-- Addons -->
      <div class="row">
        <dt class="label">Addons</dt>
        <dd class="value">
          {#if enabledAddons.length === 0}
            <div class="line muted">promote-on-serve · snapshot · litestream · mcp-http</div>
            <div class="sub muted">all disabled</div>
          {:else}
            <div class="line">
              <span class="primary">{enabledAddons.join(' · ')}</span>
            </div>
          {/if}
        </dd>
      </div>
    </dl>

    {#if lastFetchAt}
      <p class="font-mono text-xs text-fg-dim mt-6">
        last fetched {fmtRelative(lastFetchAt.toISOString(), nowTick)}
        {#if fetchError}
          · <span class="text-err">last poll failed: {fetchError}</span>
        {/if}
      </p>
    {/if}
  {/if}
</article>

<style>
  /*
    Status-table layout. Two columns desktop (label | value), stacked
    rows mobile (<720px). Monospace primary text reinforces the
    "diagnostic surface" feel — this isn't a marketing page.
  */
  .status-table {
    display: grid;
    grid-template-columns: 140px 1fr;
    gap: 14px 24px;
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 13px;
  }
  .status-table .row {
    display: contents;
  }
  .label {
    color: var(--color-fg-dim, #888);
    padding-top: 2px;
  }
  .value {
    border-left: 2px solid var(--color-line, #ddd);
    padding-left: 14px;
    /* Print: never split a component row across pages. */
    page-break-inside: avoid;
    break-inside: avoid;
  }
  .line {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 6px;
    color: var(--color-fg, #111);
  }
  .icon {
    display: inline-block;
    min-width: 1em;
    color: var(--color-fg-mute, #555);
  }
  .primary {
    color: var(--color-fg, #111);
  }
  .muted {
    color: var(--color-fg-mute, #666);
  }
  .sep {
    color: var(--color-fg-dim, #aaa);
  }
  .sub {
    margin-top: 4px;
    color: var(--color-fg-mute, #666);
    font-size: 12px;
  }
  .sub.error {
    color: var(--color-err);
  }
  .link {
    color: var(--color-accent);
  }
  .link:hover {
    text-decoration: underline;
  }
  .text-err {
    color: var(--color-err);
  }
  .text-warn {
    color: var(--color-warn);
  }

  /* Mobile reflow: stack label above value. */
  @media (max-width: 720px) {
    .status-table {
      grid-template-columns: 1fr;
      gap: 18px 0;
    }
    .label {
      padding-top: 0;
    }
    .value {
      border-left: none;
      padding-left: 0;
      border-top: 1px solid var(--color-line, #ddd);
      padding-top: 6px;
    }
  }

  /* Print: expand every detail so a printed snapshot is a usable
     incident handoff. No collapsed sections, no hover-only state. */
  @media print {
    .status-table {
      font-size: 11pt;
    }
    .value {
      border-left-color: #000;
    }
    a.link::after {
      content: ' (' attr(href) ')';
      color: #444;
    }
  }
</style>
