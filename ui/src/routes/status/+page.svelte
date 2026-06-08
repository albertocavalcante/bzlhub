<!--
  /status — live operational snapshot of this bzlhub instance.

  Reads /api/v1/system/status (Cache-Control: no-store) every 15s.
  Polling pauses when the tab is hidden (Page Visibility API) and
  resumes on visibility change. No invented metrics — every field
  on the page is something bzlhub already tracks.

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
  import {
    applyHysteresis,
    initialHysteresis,
    POLL_INTERVAL_MS,
    rank,
    SUCCESS_THRESHOLD,
    FAILURE_THRESHOLD,
    wireInstantState,
    type HysteresisState,
    type StateLevel,
    type SystemStatus,
  } from '$lib/status/state';

  // ---- Reactive state ---------------------------------------------

  let status = $state<SystemStatus | null>(null);
  let fetchError = $state<string | null>(null);
  let lastFetchAt = $state<Date | null>(null);
  // Threaded HYSTERESIS state — pure updates each tick via
  // applyHysteresis. Starts amber so first paint isn't theatre.
  let hysteresis = $state<HysteresisState>(initialHysteresis());
  // Driver for "now" — updates every second so relative-time
  // strings ("35s ago") tick without waiting for the next /status
  // poll. Cheap.
  let nowTick = $state(Date.now());
  let pollHandle = $state<ReturnType<typeof setInterval> | undefined>(undefined);
  let tickHandle: ReturnType<typeof setInterval> | undefined;

  // Convenience accessor — render code reads displayedState, not
  // the full hysteresis record.
  const displayedState = $derived(hysteresis.displayed);

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
      // Server has already computed the instant verdict via
      // internal/bzlhub/health.Derive; we just thread it through
      // client-side hysteresis. Single source of truth lives in Go.
      hysteresis = applyHysteresis(hysteresis, wireInstantState(json), Date.now());
    } catch (e) {
      fetchError = e instanceof Error ? e.message : String(e);
      // A polling failure is itself an unhealthy signal — the server
      // is at least partially unreachable from where the browser sits.
      hysteresis = applyHysteresis(hysteresis, 'unhealthy', Date.now());
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
  // Are we in a transition (the instant state from the wire differs
  // from the smoothed displayed state)? The wire's instant_state
  // moves the moment the server's view changes; displayedState
  // catches up only after SUCCESS_THRESHOLD / FAILURE_THRESHOLD
  // confirming ticks. The gap between the two is the transition
  // window we surface as a "recovering / degrading" hint.
  const wireInstant = $derived(wireInstantState(status));
  const inTransition = $derived(status !== null && wireInstant !== displayedState);
  // During recovery (instant healthier than displayed), surface how
  // many confirming probes we've seen so far so the reader knows the
  // amber checkmark isn't theatre — it's "recovering, almost there."
  const transitionHint = $derived.by(() => {
    if (!inTransition) return '';
    if (rank(wireInstant) < rank(displayedState)) {
      return `recovering — ${hysteresis.consecutiveSuccesses} of ${SUCCESS_THRESHOLD} confirming probes`;
    }
    if (rank(wireInstant) > rank(displayedState)) {
      return `degrading — ${hysteresis.consecutiveFailures} of ${FAILURE_THRESHOLD} confirming failures`;
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

  // Server-derived signal breakdown (computed.signals[]). Empty
  // when healthy. Surfaced as a why-paragraph between the state
  // pill and the structured details — readers triaging an amber/
  // red verdict see EVERY contributing signal in one glance,
  // including which upstream is slow and how stale the sync is,
  // without re-reading the source rows below.
  const signals = $derived(status?.computed?.signals ?? []);

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

  {#if signals.length > 0}
    <!--
      Why-paragraph: server-derived signal breakdown
      (computed.signals[]). Anti-slop §Part 3 rule: when amber or
      red, render MORE information, not less. The reader who hits
      /status during a degraded state sees EVERY contributing
      signal in one block, not just the page-level verdict.
      Sorted by level (red first) so the most severe shows up top.
    -->
    <ul
      class="signals-list mb-4"
      aria-label="contributing signals"
      aria-live="polite"
    >
      {#each [...signals].sort((a, b) => (b.level === 'unhealthy' ? 1 : 0) - (a.level === 'unhealthy' ? 1 : 0)) as sig (sig.kind + sig.detail)}
        <li
          class="signal"
          class:signal-unhealthy={sig.level === 'unhealthy'}
          class:signal-degraded={sig.level === 'degraded'}
        >
          <span class="signal-icon" aria-hidden="true">
            {sig.level === 'unhealthy' ? '✗' : '⚠'}
          </span>
          <span class="signal-kind">{sig.kind}</span>
          <span class="signal-detail">{sig.detail}</span>
        </li>
      {/each}
    </ul>
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
          {#if status.mirror.last_sync_at || status.mirror.head_sha}
            <!--
              Mirror heartbeat. last_sync_at proves `bzlhub sync run`
              is reaching upstream regardless of whether new modules
              landed in the index; head_sha pins the BCR commit the
              cascade is serving from. Both omitted for File-backed
              installs and pre-Plan-21 mirrors.
            -->
            <div class="sub">
              {#if status.mirror.last_sync_at}
                synced {fmtRelative(status.mirror.last_sync_at, nowTick)}
              {/if}
              {#if status.mirror.last_sync_at && status.mirror.head_sha}
                <span class="sep">·</span>
              {/if}
              {#if status.mirror.head_sha}
                HEAD
                <span class="font-mono">{status.mirror.head_sha.slice(0, 7)}</span>
              {/if}
            </div>
          {/if}
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
                <!--
                  Hostname coloring: red on unreachable. The "slow
                  but reachable" amber decoration was retired when
                  the threshold moved server-side — the page-level
                  state pill already carries that verdict, and
                  per-row coloring would require duplicating the
                  threshold constant client-side.
                -->
                <span class="primary" class:text-err={!u.reachable}>
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

  /* Signal breakdown: one row per contributing reason. Red rows
     get a stronger left border so triage-by-glance is colour-
     blind safe (border + icon + kind text all carry the level). */
  .signals-list {
    list-style: none;
    padding: 0;
    margin: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 12px;
  }
  .signal {
    display: flex;
    align-items: baseline;
    gap: 8px;
    padding: 6px 10px;
    border-left: 3px solid var(--color-line, #ddd);
    background: var(--color-bg-elev, transparent);
    border-radius: 0 3px 3px 0;
  }
  .signal-unhealthy {
    border-left-color: var(--color-err);
  }
  .signal-degraded {
    border-left-color: var(--color-warn, var(--color-fg-mute));
  }
  .signal-icon {
    color: var(--color-fg-mute, #555);
    min-width: 1em;
  }
  .signal-unhealthy .signal-icon {
    color: var(--color-err);
  }
  .signal-degraded .signal-icon {
    color: var(--color-warn, var(--color-fg-mute));
  }
  .signal-kind {
    color: var(--color-fg, #111);
    font-weight: 500;
  }
  .signal-detail {
    color: var(--color-fg-mute, #555);
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
