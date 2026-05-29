<script lang="ts">
  import { paths } from '$lib/api/paths';
  // /drift — surfaces the JFrog-killer feature visually.
  //
  // Reads /api/v1/drift on mount + on user-triggered refresh. The page is
  // intentionally "diagnostic" feeling: monospace counts, status sigils,
  // expandable per-module details. Power-user density over hand-holding.
  //
  // Errors are surfaced inline (not toasts) because the 409 "not configured"
  // case is informative ("you started canopy without --root"), not transient.

  import {
    getDrift,
    getDiff,
    bumpModule,
    ingestRecursive,
    DriftNotAvailableError,
    BumpUpstreamError,
  } from '$api/client';
  import type { DriftReport, DriftStatus, ModuleDrift } from '$api/types';
  import { classifyDriftModule, type BreakingScanMap } from '$lib/drift/classify';
  import {
    breakingScanStorageKey,
    parseStoredScans,
    restoreBreakingScans,
    withStoredScan,
  } from '$lib/drift/scan-cache';

  let report = $state<DriftReport | null>(null);
  let loading = $state(true);
  let elapsedMs = $state(0);
  let error = $state<string | null>(null);
  let notConfigured = $state(false);
  let upstream = $state('https://bcr.bazel.build');
  let expanded = $state<Set<string>>(new Set());

  // Per-row bump state. Keys are module names; values are one of:
  //   'queued'  — selected for a batch advance, waiting to run
  //   'bumping' — POST /api/v1/actions/bump in flight
  //   'done'    — batch finished successfully for this row
  //   string    — error message from the last attempt
  let bumpState = $state<Record<string, 'queued' | 'bumping' | 'done' | string>>({});

  // Per-row breaking-impact scan state. Driven by the "scan breaking"
  // header action; not auto-run because each row triggers an upstream
  // fetch + assay analysis (slow for big modules, rude to bcr.bazel.build
  // on every drift refresh).
  //   'pending' — what-if diff in flight
  //   'error'   — the diff call failed (upstream unreachable, etc.)
  //   number    — count of breaking findings (0 = structurally safe)
  let breakingScan = $state<BreakingScanMap>({});
  let scanning = $state(false);

  function storageKey(): string | null {
    if (!report) return null;
    return breakingScanStorageKey(upstream, report);
  }

  function persistScan(m: ModuleDrift, value: 'error' | number): void {
    if (typeof localStorage === 'undefined') return;
    if (!m.local_latest || !m.upstream_latest) return;
    const key = storageKey();
    if (!key) return;
    try {
      const existing = parseStoredScans(localStorage.getItem(key));
      localStorage.setItem(key, JSON.stringify(withStoredScan(existing, m, value)));
    } catch {
      // localStorage can throw on quota / private-mode; not worth
      // surfacing — scan results live for the session either way.
    }
  }

  function restoreScans(): void {
    if (typeof localStorage === 'undefined' || !report) return;
    const key = storageKey();
    if (!key) return;
    try {
      const raw = localStorage.getItem(key);
      if (!raw) return;
      const restored = restoreBreakingScans(report.modules, parseStoredScans(raw));
      breakingScan = restored.scans;
      if (restored.pruned) localStorage.setItem(key, JSON.stringify(restored.stored));
    } catch {
      // Corrupt entry — just blow it away rather than try to recover.
      try {
        localStorage.removeItem(key);
      } catch {
        /* ignore */
      }
    }
  }

  async function scanAllBreaking(): Promise<void> {
    if (!report || scanning) return;
    const behind = report.modules.filter(
      (m: ModuleDrift) => m.status === 'behind' && m.local_latest && m.upstream_latest,
    );
    if (behind.length === 0) return;
    scanning = true;
    // Reset and mark each row pending up front so the UI immediately
    // reflects the work is queued.
    breakingScan = {};
    for (const m of behind) breakingScan[m.name] = 'pending';

    // Bounded fan-out — 4 concurrent fetches is a sane default that
    // keeps upstream BCR happy and the user's network from saturating.
    const queue = [...behind];
    const worker = async (): Promise<void> => {
      while (queue.length > 0) {
        const m = queue.shift();
        if (!m || !m.local_latest || !m.upstream_latest) continue;
        try {
          const d = await getDiff(m.name, m.local_latest, m.upstream_latest, upstream);
          const count = d.breaking?.length ?? 0;
          breakingScan[m.name] = count;
          persistScan(m, count);
        } catch {
          breakingScan[m.name] = 'error';
          persistScan(m, 'error');
        }
      }
    };
    await Promise.all(Array.from({ length: 4 }, worker));
    scanning = false;
  }

  // --- Drift-row classification ---
  // Conservative tiers determine which rows are eligible for the
  // "auto-safe" preset and which require explicit per-row review.
  // Pre-computed once per drift refresh; reused everywhere. Reactive on
  // breakingScan so the auto-safe count updates live as scans complete.
  const classified = $derived(
    new Map((report?.modules ?? []).map((m: ModuleDrift) => [m.name, classifyDriftModule(m, breakingScan)])),
  );

  // --- Selection state ---
  let selected = $state<Set<string>>(new Set());
  const autoSafeCount = $derived(
    (report?.modules ?? []).filter((m: ModuleDrift) => classified.get(m.name)?.tier === 'auto-safe').length,
  );
  const behindCount = $derived(
    (report?.modules ?? []).filter((m: ModuleDrift) => m.status === 'behind').length,
  );

  function selectionToggle(name: string) {
    if (selected.has(name)) {
      selected.delete(name);
    } else {
      selected.add(name);
    }
    selected = new Set(selected);
  }
  function selectAutoSafe() {
    const next = new Set<string>();
    for (const m of report?.modules ?? []) {
      if (classified.get(m.name)?.tier === 'auto-safe') next.add(m.name);
    }
    selected = next;
  }
  function selectAllBehind() {
    const next = new Set<string>();
    for (const m of report?.modules ?? []) {
      if (m.status === 'behind') next.add(m.name);
    }
    selected = next;
  }
  function clearSelection() {
    selected = new Set();
  }

  // --- Batch advance ---
  let batchRunning = $state(false);
  let batchSummary = $state<{ ok: number; err: number } | null>(null);

  async function advanceSelected() {
    if (selected.size === 0 || batchRunning) return;
    batchRunning = true;
    batchSummary = null;
    let ok = 0;
    let err = 0;

    // Stable order: alpha by name so the user sees a predictable march.
    const targets = [...selected].sort();
    // Mark all queued up front so the user sees the plan.
    const next: Record<string, 'queued' | 'bumping' | 'done' | string> = { ...bumpState };
    for (const name of targets) next[name] = 'queued';
    bumpState = next;

    for (const name of targets) {
      const row = report?.modules.find((m: ModuleDrift) => m.name === name);
      const target = row?.upstream_latest;
      if (!target) {
        bumpState = { ...bumpState, [name]: 'no upstream_latest — skipped' };
        err += 1;
        continue;
      }
      bumpState = { ...bumpState, [name]: 'bumping' };
      try {
        await bumpModule({ module: name, version: target, upstream });
        bumpState = { ...bumpState, [name]: 'done' };
        ok += 1;
      } catch (e: unknown) {
        const msg =
          e instanceof BumpUpstreamError
            ? `upstream: ${e.message}`
            : e instanceof DriftNotAvailableError
              ? e.message
              : e instanceof Error
                ? e.message
                : String(e);
        bumpState = { ...bumpState, [name]: msg };
        err += 1;
      }
    }

    batchSummary = { ok, err };
    batchRunning = false;
    // Final drift refresh ensures rows that succeeded show in-sync.
    await load();
    // Clear selection on success so users don't re-fire by accident.
    if (err === 0) selected = new Set();
  }

  // --- ingest modal ---
  // Form to kick off a recursive closure ingest. The actual progress
  // appears in the live banner (the same one that fires for bump events),
  // so this form just needs to dispatch and close.
  let ingestOpen = $state(false);
  let ingestModule = $state('');
  let ingestVersion = $state('');
  let ingestIncludeBazelTools = $state(false);
  let ingestWorkers = $state(8);
  let ingestSubmitting = $state(false);
  let ingestError = $state<string | null>(null);
  let ingestSummary = $state<{ visited: number; mirrored: number; errors: number } | null>(
    null,
  );

  function openIngestModal() {
    ingestModule = '';
    ingestVersion = '';
    ingestIncludeBazelTools = false;
    ingestWorkers = 8;
    ingestError = null;
    ingestSummary = null;
    ingestOpen = true;
  }

  async function submitIngest() {
    if (!ingestModule.trim() || !ingestVersion.trim()) {
      ingestError = 'module and version are required';
      return;
    }
    ingestSubmitting = true;
    ingestError = null;
    ingestSummary = null;
    try {
      const res = await ingestRecursive({
        module: ingestModule.trim(),
        version: ingestVersion.trim(),
        upstream,
        include_bazel_tools: ingestIncludeBazelTools,
        workers: ingestWorkers,
      });
      ingestSummary = {
        visited: res.visited,
        mirrored: res.mirrored,
        errors: res.errors?.length ?? 0,
      };
      // The live banner already showed progress along the way; one final
      // drift refresh ensures the list reflects the final state even if
      // the SSE-debounced load missed the trailing event.
      await load();
    } catch (e: unknown) {
      ingestError =
        e instanceof DriftNotAvailableError
          ? e.message
          : e instanceof Error
            ? e.message
            : String(e);
    } finally {
      ingestSubmitting = false;
    }
  }

  async function bumpRow(m: ModuleDrift) {
    if (!m.upstream_latest) return;
    bumpState[m.name] = 'bumping';
    bumpState = { ...bumpState };
    try {
      await bumpModule({ module: m.name, version: m.upstream_latest, upstream });
      delete bumpState[m.name];
      bumpState = { ...bumpState };
      // Refetch drift so the row reflects the new local_latest.
      await load();
    } catch (e: unknown) {
      const msg =
        e instanceof BumpUpstreamError
          ? `upstream: ${e.message}`
          : e instanceof DriftNotAvailableError
            ? e.message
            : e instanceof Error
              ? e.message
              : String(e);
      bumpState[m.name] = msg;
      bumpState = { ...bumpState };
    }
  }

  async function load(upstreamOverride?: string) {
    const ctl = new AbortController();
    loading = true;
    error = null;
    notConfigured = false;
    const t0 = performance.now();
    try {
      const r = await getDrift({ upstream: upstreamOverride ?? upstream }, ctl.signal);
      report = r;
      elapsedMs = Math.round(performance.now() - t0);
      // Reset in-flight state and try to hydrate from the last session's
      // persisted scans. Stale entries (where local/upstream versions
      // have moved since) are silently dropped inside restoreScans.
      breakingScan = {};
      restoreScans();
    } catch (e: unknown) {
      if (e instanceof DriftNotAvailableError) {
        notConfigured = true;
        error = e.message;
      } else {
        error = e instanceof Error ? e.message : String(e);
      }
      report = null;
    } finally {
      loading = false;
    }
  }

  // Status palette tuned to be readable against bg-elev backgrounds.
  // Yellow→Behind, Red→Yanked, Cyan→LocalOnly, Green→InSync, Grey→Error.
  const statusMeta: Record<DriftStatus, { sigil: string; label: string; cls: string }> = {
    behind:           { sigil: '↑', label: 'behind',          cls: 'text-warn' },
    'yanked-upstream':{ sigil: '⚠', label: 'yanked upstream', cls: 'text-err' },
    'local-only':     { sigil: '•', label: 'local-only',      cls: 'text-accent' },
    'in-sync':        { sigil: '✓', label: 'in-sync',         cls: 'text-ok' },
    'upstream-error': { sigil: '✗', label: 'upstream error',  cls: 'text-fg-dim' },
  };

  function toggle(name: string) {
    if (expanded.has(name)) {
      expanded.delete(name);
    } else {
      expanded.add(name);
    }
    expanded = new Set(expanded);
  }

  // Auto-load on mount. The void on a fire-and-forget promise satisfies the
  // no-floating-promises rule without a needless top-level await.
  $effect(() => {
    void load();
  });

  // Live progress: when canopy publishes a high rate of module_indexed
  // events (a recursive ingest in flight), surface a banner counting
  // them. The banner fades 2s after the burst stops. Single events
  // (e.g., a one-off bump) still trigger a drift refresh via the
  // debounced scheduleRefresh path.
  let liveCount = $state(0);
  let liveLatest = $state<{ module: string; version: string } | null>(null);

  $effect(() => {
    const es = new EventSource(paths.activity.events());
    let refreshDebounce: ReturnType<typeof setTimeout> | null = null;
    let fadeTimer: ReturnType<typeof setTimeout> | null = null;
    const scheduleRefresh = () => {
      if (refreshDebounce) clearTimeout(refreshDebounce);
      refreshDebounce = setTimeout(() => {
        void load();
      }, 200);
    };
    es.addEventListener('module_indexed', (e: Event) => {
      const me = e as MessageEvent;
      try {
        const data = JSON.parse(me.data);
        liveLatest = { module: data.module, version: data.version };
      } catch {
        // payload not JSON — ignore; the count still increments
      }
      liveCount += 1;
      if (fadeTimer) clearTimeout(fadeTimer);
      fadeTimer = setTimeout(() => {
        liveCount = 0;
        liveLatest = null;
      }, 2000);
      scheduleRefresh();
    });
    return () => {
      es.close();
      if (refreshDebounce) clearTimeout(refreshDebounce);
      if (fadeTimer) clearTimeout(fadeTimer);
    };
  });
</script>

<svelte:head>
  <title>drift — canopy</title>
</svelte:head>

<div class="flex flex-col gap-6">
  <header class="flex flex-col gap-3 pb-3 border-b border-line">
    <div class="flex items-baseline gap-3 flex-wrap">
      <h1 class="font-mono text-2xl text-fg tracking-tight">drift</h1>
      <p class="text-[12px] text-fg-mute">
        compare canopy's local mirror against an upstream BCR-shape registry
      </p>
    </div>
    <form
      class="flex items-center gap-2 flex-wrap"
      onsubmit={(e) => {
        e.preventDefault();
        void load();
      }}
    >
      <label for="upstream" class="text-[11px] text-fg-dim uppercase tracking-wide">upstream</label>
      <input
        id="upstream"
        type="url"
        bind:value={upstream}
        class="flex-1 min-w-[280px] rounded-md border border-line bg-bg-elev px-3 py-1.5 text-[13px] font-mono text-fg outline-none focus:border-accent"
      />
      <button
        type="submit"
        disabled={loading}
        class="rounded-md border border-line bg-bg-elev px-3 py-1.5 text-[12px] text-fg hover:border-accent transition-colors disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer"
      >
        {loading ? 'checking…' : 'refresh'}
      </button>
      <button
        type="button"
        onclick={openIngestModal}
        class="rounded-md border border-accent/40 bg-accent/5 px-3 py-1.5 text-[12px] text-accent hover:bg-accent/15 transition-colors cursor-pointer"
      >
        ingest closure
      </button>
    </form>
  </header>

  {#if notConfigured}
    <div class="rounded-md border border-warn/30 bg-warn/5 px-4 py-3 text-[13px] flex flex-col gap-2">
      <p class="text-warn font-medium">drift requires a mirror root</p>
      <p class="text-fg-mute leading-relaxed">
        Start canopy with <code class="text-fg-mute">--root &lt;path-to-mirror&gt;</code>
        pointing at a BCR-shape directory tree. Then refresh this page.
      </p>
      <p class="text-[11px] text-fg-dim">{error}</p>
    </div>
  {:else if error}
    <div class="rounded-md border border-err/30 bg-err/10 px-3 py-2 text-sm text-err" role="alert">
      {error}
    </div>
  {:else if loading && !report}
    <div class="flex flex-col gap-2">
      {#each Array.from({ length: 4 }) as _, i (i)}
        <div class="skeleton h-14 w-full"></div>
      {/each}
    </div>
  {:else if report}
    {#if liveCount > 0}
      <div
        class="flex items-center gap-3 px-3 py-2 rounded-md border border-accent/40 bg-accent/5 text-[12px]"
        role="status"
        aria-live="polite"
      >
        <span class="relative flex h-2 w-2 shrink-0">
          <span class="absolute inset-0 rounded-full bg-accent opacity-75 animate-ping"></span>
          <span class="relative rounded-full h-2 w-2 bg-accent"></span>
        </span>
        <span class="font-mono text-accent">
          ingesting · {liveCount} module{liveCount === 1 ? '' : 's'} indexed
        </span>
        {#if liveLatest}
          <span class="font-mono text-fg-mute truncate">
            latest: <span class="text-fg">{liveLatest.module}</span>
            <span class="text-fg-dim">@</span>
            <span class="text-fg">{liveLatest.version}</span>
          </span>
        {/if}
      </div>
    {/if}

    <!-- Summary chips -->
    <div class="flex flex-wrap items-baseline gap-3 text-[12px] font-mono">
      <span class="text-fg-mute">
        {report.summary.total} module{report.summary.total === 1 ? '' : 's'}
        <span class="text-fg-dim">·</span>
        {elapsedMs}ms
      </span>
      {#if report.summary.behind > 0}
        <span class="text-warn">{report.summary.behind} behind</span>
      {/if}
      {#if report.summary.yanked_upstream > 0}
        <span class="text-err">{report.summary.yanked_upstream} yanked</span>
      {/if}
      {#if report.summary.local_only > 0}
        <span class="text-accent">{report.summary.local_only} local-only</span>
      {/if}
      {#if report.summary.in_sync > 0}
        <span class="text-ok">{report.summary.in_sync} in-sync</span>
      {/if}
      {#if report.summary.upstream_error > 0}
        <span class="text-fg-dim">{report.summary.upstream_error} error</span>
      {/if}
    </div>

    {#if report.modules.length === 0}
      <p class="text-center py-16 text-fg-dim font-mono text-sm">
        no modules in the mirror yet
      </p>
    {:else}
      <!-- Batch advance bar. Hidden when there's nothing actionable. -->
      {#if behindCount > 0}
        <div class="flex items-center gap-2 flex-wrap text-[12px]">
          <span class="text-fg-dim font-mono">
            advance:
          </span>
          <button
            type="button"
            class="rounded-md border border-line bg-bg-elev px-2 py-1 text-fg hover:border-accent transition-colors cursor-pointer disabled:opacity-40 disabled:cursor-not-allowed"
            disabled={autoSafeCount === 0 || batchRunning}
            onclick={selectAutoSafe}
            title="behind rows that share major with their upstream and have no pre-release suffix"
          >
            auto-safe ({autoSafeCount})
          </button>
          <button
            type="button"
            class="rounded-md border border-line bg-bg-elev px-2 py-1 text-fg-mute hover:text-fg hover:border-line-strong transition-colors cursor-pointer disabled:opacity-40 disabled:cursor-not-allowed"
            disabled={batchRunning}
            onclick={selectAllBehind}
            title="includes major bumps and pre-releases — review each row first"
          >
            all behind ({behindCount})
          </button>
          <button
            type="button"
            class="rounded-md border border-line bg-bg-elev px-2 py-1 text-fg-mute hover:text-fg hover:border-line-strong transition-colors cursor-pointer disabled:opacity-40 disabled:cursor-not-allowed ml-auto"
            disabled={scanning || batchRunning}
            onclick={() => void scanAllBreaking()}
            title="for each behind row, run a what-if diff against upstream and label rows with their breaking-change count (✓ safe / ⚠ N) — triggers upstream fetches"
          >
            {scanning ? 'scanning…' : 'scan breaking impact'}
          </button>
          {#if selected.size > 0}
            <button
              type="button"
              class="rounded-md px-2 py-1 text-fg-dim hover:text-fg transition-colors cursor-pointer disabled:opacity-40"
              disabled={batchRunning}
              onclick={clearSelection}
            >
              clear
            </button>
            <span class="text-fg-mute font-mono ml-auto">{selected.size} selected</span>
            <button
              type="button"
              class="rounded-md border border-accent/40 bg-accent/10 px-3 py-1 font-mono text-accent hover:bg-accent/20 transition-colors cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
              disabled={batchRunning}
              onclick={() => void advanceSelected()}
            >
              {batchRunning ? `advancing… ${Object.values(bumpState).filter((v) => v === 'done').length}/${selected.size}` : `advance ${selected.size}`}
            </button>
          {/if}
        </div>
        {#if batchSummary}
          <p class="text-[12px] font-mono {batchSummary.err === 0 ? 'text-ok' : 'text-warn'}">
            {#if batchSummary.err === 0}
              ✓ advanced {batchSummary.ok} module{batchSummary.ok === 1 ? '' : 's'}
            {:else}
              ⚠ {batchSummary.ok} succeeded · {batchSummary.err} failed — see per-row errors below
            {/if}
          </p>
        {/if}
      {/if}

      <ul class="flex flex-col gap-1.5">
        {#each report.modules as m (m.name)}
          {@const meta = statusMeta[m.status]}
          {@const isOpen = expanded.has(m.name)}
          {@const hasDetails =
            (m.newer_upstream?.length ?? 0) > 0 ||
            (m.yanked_at_upstream?.length ?? 0) > 0 ||
            (m.local_only_versions?.length ?? 0) > 0 ||
            !!m.error}
          {@const cls = classified.get(m.name)}
          {@const rowState = bumpState[m.name]}
          {@const isBehind = m.status === 'behind'}
          <li class="border border-line rounded-md bg-bg-elev/50 overflow-hidden">
            <div class="w-full flex items-center gap-3 px-3 py-2 hover:bg-bg-elev">
              <!-- Checkbox slot. Only "behind" rows are selectable for batch advance;
                   other rows occupy the slot with a tier-appropriate hint so the
                   alignment stays clean across the list. -->
              <span class="w-4 shrink-0 flex items-center justify-center" title={cls?.reason ?? ''}>
                {#if isBehind}
                  <input
                    type="checkbox"
                    checked={selected.has(m.name)}
                    onchange={() => selectionToggle(m.name)}
                    onclick={(e) => e.stopPropagation()}
                    disabled={batchRunning}
                    class="accent-accent cursor-pointer disabled:cursor-not-allowed"
                    aria-label={`select ${m.name}`}
                  />
                {:else if m.status === 'in-sync'}
                  <span class="text-fg-dim text-[10px]">·</span>
                {:else}
                  <span class="text-fg-dim text-[10px]">−</span>
                {/if}
              </span>
              <button
                type="button"
                class="flex-1 flex items-center gap-3 text-left cursor-pointer disabled:cursor-default"
                onclick={() => toggle(m.name)}
                aria-expanded={isOpen}
                disabled={!hasDetails}
              >
                <span class="w-4 text-center font-mono text-[14px] {meta.cls}">{meta.sigil}</span>
                <span class="font-mono text-[13px] text-fg min-w-[180px] truncate">{m.name}</span>
                <span class="text-[12px] text-fg-mute font-mono shrink-0">
                  local <span class="text-fg">{m.local_latest || '—'}</span>
                  <span class="text-fg-dim mx-1">→</span>
                  upstream <span class="text-fg">{m.upstream_latest || '—'}</span>
                </span>
                {#if isBehind && cls?.tier === 'review'}
                  <span
                    class="text-[10px] uppercase tracking-wide text-warn/80 font-medium"
                    title={cls.reason}
                  >
                    review
                  </span>
                {:else if isBehind && cls?.tier === 'auto-safe'}
                  <span
                    class="text-[10px] uppercase tracking-wide text-ok/80 font-medium"
                    title={cls.reason}
                  >
                    safe
                  </span>
                {/if}
                {#if isBehind && breakingScan[m.name] !== undefined}
                  {@const bs = breakingScan[m.name]}
                  {#if bs === 'pending'}
                    <span class="text-[10px] font-mono text-fg-dim shrink-0" title="what-if diff in flight">…</span>
                  {:else if bs === 'error'}
                    <span class="text-[10px] font-mono text-err shrink-0" title="scan failed — upstream unreachable or version unfetchable">scan ✗</span>
                  {:else if bs === 0}
                    <span class="text-[10px] font-mono text-ok shrink-0" title="what-if diff reported no structural breaks">✓ safe-bump</span>
                  {:else}
                    <span class="text-[10px] font-mono text-err shrink-0" title="what-if diff reported {bs} structurally-breaking finding{bs === 1 ? '' : 's'} — open the diff for details">
                      ⚠ {bs} breaking
                    </span>
                  {/if}
                {/if}
                <span class="ml-auto text-[10px] uppercase tracking-wide {meta.cls} font-medium">
                  {meta.label}
                </span>
                {#if rowState}
                  <span
                    class="text-[10px] font-mono shrink-0 {rowState === 'done' ? 'text-ok' : rowState === 'bumping' ? 'text-accent' : rowState === 'queued' ? 'text-fg-dim' : 'text-err'}"
                  >
                    {rowState === 'queued'
                      ? 'queued'
                      : rowState === 'bumping'
                        ? 'bumping…'
                        : rowState === 'done'
                          ? '✓ done'
                          : '✗ error'}
                  </span>
                {/if}
                {#if isBehind && m.newer_upstream && !rowState}
                  <span class="text-[10px] text-fg-dim font-mono shrink-0">
                    +{m.newer_upstream.length}
                  </span>
                {/if}
                {#if hasDetails}
                  <span class="text-fg-dim text-xs ml-1 w-3 text-center">
                    {isOpen ? '−' : '+'}
                  </span>
                {/if}
              </button>
              {#if isBehind && m.local_latest && m.upstream_latest}
                <a
                  href={`/modules/${encodeURIComponent(m.name)}/diff?from=${encodeURIComponent(m.local_latest)}&to=${encodeURIComponent(m.upstream_latest)}&upstream=${encodeURIComponent(upstream)}`}
                  class="shrink-0 text-[10px] font-mono text-fg-mute hover:text-accent transition-colors"
                  title={`preview public-surface delta ${m.local_latest} → ${m.upstream_latest} (target version fetched from upstream if not yet indexed)`}
                  onclick={(e) => e.stopPropagation()}
                >
                  diff →
                </a>
              {/if}
            </div>
            {#if isOpen && hasDetails}
              <div class="px-3 pb-3 pt-1 border-t border-line/60 flex flex-col gap-2 text-[12px]">
                {#if m.newer_upstream && m.newer_upstream.length > 0}
                  <div class="flex items-baseline gap-3 flex-wrap">
                    <span class="text-[10px] uppercase tracking-wide text-warn">newer</span>
                    <span class="font-mono text-fg-mute flex-1 min-w-0">{m.newer_upstream.join('  ')}</span>
                    {#if m.status === 'behind' && m.upstream_latest}
                      {@const bumping = bumpState[m.name] === 'bumping'}
                      <button
                        type="button"
                        class="rounded-md border border-warn/40 bg-warn/5 px-2 py-1 text-[11px] font-mono text-warn hover:bg-warn/15 transition-colors disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer shrink-0"
                        disabled={bumping}
                        onclick={(e) => {
                          e.stopPropagation();
                          void bumpRow(m);
                        }}
                      >
                        {bumping ? `bumping → ${m.upstream_latest}…` : `bump → ${m.upstream_latest}`}
                      </button>
                    {/if}
                  </div>
                  {#if bumpState[m.name] && bumpState[m.name] !== 'bumping'}
                    <p class="text-[11px] text-err font-mono break-all">
                      {bumpState[m.name]}
                    </p>
                  {/if}
                {/if}
                {#if m.yanked_at_upstream && m.yanked_at_upstream.length > 0}
                  <div>
                    <span class="text-[10px] uppercase tracking-wide text-err mr-2">yanked</span>
                    <span class="font-mono text-fg-mute">{m.yanked_at_upstream.join('  ')}</span>
                  </div>
                {/if}
                {#if m.local_only_versions && m.local_only_versions.length > 0}
                  <div>
                    <span class="text-[10px] uppercase tracking-wide text-accent mr-2">local-only</span>
                    <span class="font-mono text-fg-mute">{m.local_only_versions.join('  ')}</span>
                  </div>
                {/if}
                {#if m.error}
                  <div>
                    <span class="text-[10px] uppercase tracking-wide text-fg-dim mr-2">error</span>
                    <span class="font-mono text-fg-mute whitespace-pre-wrap">{m.error}</span>
                  </div>
                {/if}
              </div>
            {/if}
          </li>
        {/each}
      </ul>
    {/if}
  {/if}
</div>

{#if ingestOpen}
  <div
    class="fixed inset-0 z-30 flex items-start justify-center pt-24 px-4 bg-bg/70 backdrop-blur-sm"
    role="dialog"
    aria-modal="true"
    aria-labelledby="ingest-title"
    onclick={(e) => {
      if (e.target === e.currentTarget && !ingestSubmitting) ingestOpen = false;
    }}
    onkeydown={(e) => {
      if (e.key === 'Escape' && !ingestSubmitting) ingestOpen = false;
    }}
    tabindex="-1"
  >
    <div class="w-full max-w-md rounded-md border border-line bg-bg-elev shadow-xl">
      <header class="px-4 py-3 border-b border-line flex items-baseline justify-between">
        <h2 id="ingest-title" class="font-mono text-sm text-fg">ingest closure</h2>
        <button
          type="button"
          class="text-fg-dim hover:text-fg text-[11px] cursor-pointer"
          onclick={() => (ingestOpen = false)}
          disabled={ingestSubmitting}
        >
          esc
        </button>
      </header>

      <form
        class="flex flex-col gap-3 px-4 py-4"
        onsubmit={(e) => {
          e.preventDefault();
          void submitIngest();
        }}
      >
        <label class="flex flex-col gap-1">
          <span class="text-[10px] uppercase tracking-wide text-fg-dim">module</span>
          <input
            type="text"
            bind:value={ingestModule}
            placeholder="rules_go"
            autocomplete="off"
            disabled={ingestSubmitting}
            class="rounded-md border border-line bg-bg px-3 py-1.5 text-[13px] font-mono text-fg outline-none focus:border-accent disabled:opacity-50"
          />
        </label>
        <label class="flex flex-col gap-1">
          <span class="text-[10px] uppercase tracking-wide text-fg-dim">version</span>
          <input
            type="text"
            bind:value={ingestVersion}
            placeholder="0.52.0"
            autocomplete="off"
            disabled={ingestSubmitting}
            class="rounded-md border border-line bg-bg px-3 py-1.5 text-[13px] font-mono text-fg outline-none focus:border-accent disabled:opacity-50"
          />
        </label>

        <div class="flex items-end gap-3">
          <label class="flex flex-col gap-1 flex-1">
            <span class="text-[10px] uppercase tracking-wide text-fg-dim">workers</span>
            <input
              type="number"
              min="1"
              max="32"
              bind:value={ingestWorkers}
              disabled={ingestSubmitting}
              class="rounded-md border border-line bg-bg px-3 py-1.5 text-[13px] font-mono text-fg outline-none focus:border-accent disabled:opacity-50"
            />
          </label>
          <label class="flex items-center gap-2 pb-1 cursor-pointer text-[12px] text-fg-mute">
            <input
              type="checkbox"
              bind:checked={ingestIncludeBazelTools}
              disabled={ingestSubmitting}
              class="accent-accent"
            />
            include bazel-tools
          </label>
        </div>

        {#if ingestError}
          <p class="text-[12px] text-err font-mono break-all">{ingestError}</p>
        {/if}

        {#if ingestSummary}
          <p class="text-[12px] text-ok font-mono">
            ✓ visited {ingestSummary.visited} · mirrored {ingestSummary.mirrored}
            {#if ingestSummary.errors > 0}
              · <span class="text-err">errors {ingestSummary.errors}</span>
            {/if}
          </p>
        {/if}

        <div class="flex items-center justify-end gap-2 pt-2">
          <button
            type="button"
            class="text-[12px] text-fg-mute hover:text-fg cursor-pointer"
            onclick={() => (ingestOpen = false)}
            disabled={ingestSubmitting}
          >
            close
          </button>
          <button
            type="submit"
            disabled={ingestSubmitting}
            class="rounded-md border border-accent/40 bg-accent/10 px-3 py-1.5 text-[12px] font-mono text-accent hover:bg-accent/20 transition-colors disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer"
          >
            {ingestSubmitting ? 'ingesting…' : 'ingest'}
          </button>
        </div>
      </form>
    </div>
  </div>
{/if}
