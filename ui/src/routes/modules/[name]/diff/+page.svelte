<script lang="ts">
  // /modules/[name]/diff?from=A&to=B — structured delta between two
  // stored versions. Companion to the drift dashboard's REVIEW tier:
  // turns "major bump, breaking" into a concrete attribute-level
  // migration plan.

  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { getDiff, getDiffClosure, listVersions, type VersionEntry } from '$api/client';
  import type { ModuleDiffReport, DiffRules, ClosureDiffReport } from '$api/types';
  import { renderDiffMarkdown } from '$lib/diff-markdown';
  import {
    closureStorageKey,
    parseStoredClosureScan,
    restoreClosureScanForReport,
    storedClosureScan,
  } from '$lib/diff/closure-cache';
  import {
    isBreakingOnlyEmptyReport,
    isEmptyDiffReport,
    relativeScanAge,
    splitHint,
    transientVersionSides,
  } from '$lib/diff/report-shape';
  import { diffHref } from '$lib/links';

  let report = $state<ModuleDiffReport | null>(null);
  let loading = $state(true);
  let error = $state<string | null>(null);

  // Indexed versions of this module — drives the in-page from/to
  // dropdowns. Fetched once per module name; navigating between
  // version pairs reuses the list.
  let versionEntries = $state<VersionEntry[]>([]);

  $effect(() => {
    const name = page.params.name;
    if (!name) return;
    const ctl = new AbortController();
    listVersions(name, ctl.signal)
      .then((r) => {
        if (!ctl.signal.aborted) versionEntries = r.entries ?? [];
      })
      .catch(() => {
        // Silent — the pickers just won't render. Diff itself
        // loads independently.
      });
    return () => ctl.abort();
  });

  function navigateToPair(from: string, to: string) {
    const name = page.params.name;
    if (!name || !from || !to || from === to) return;
    void goto(diffHref(name, from, to));
  }

  function onFromChange(ev: Event) {
    const newFrom = (ev.target as HTMLSelectElement).value;
    const to = page.url.searchParams.get('to') ?? '';
    navigateToPair(newFrom, to);
  }

  function onToChange(ev: Event) {
    const newTo = (ev.target as HTMLSelectElement).value;
    const from = page.url.searchParams.get('from') ?? '';
    navigateToPair(from, newTo);
  }

  function onSwap() {
    const from = page.url.searchParams.get('from') ?? '';
    const to = page.url.searchParams.get('to') ?? '';
    navigateToPair(to, from);
  }

  $effect(() => {
    const module = page.params.name;
    const from = page.url.searchParams.get('from') ?? '';
    const to = page.url.searchParams.get('to') ?? '';
    const upstream = page.url.searchParams.get('upstream') ?? undefined;
    if (!module || !from || !to) {
      error = 'missing module, from, or to in URL';
      loading = false;
      return;
    }
    const ctl = new AbortController();
    loading = true;
    error = null;
    report = null;
    getDiff(module, from, to, upstream, ctl.signal)
      .then((d) => {
        report = d;
      })
      .catch((e: unknown) => {
        error = e instanceof Error ? e.message : String(e);
      })
      .finally(() => {
        loading = false;
      });
    return () => ctl.abort();
  });

  let copyState = $state<'idle' | 'copied' | 'error'>('idle');
  let copyResetTimer: ReturnType<typeof setTimeout> | null = null;

  // Closure-impact scan state. Lazily triggered by an explicit button —
  // a closure walk + per-module fan-out is 5–30s and would dominate
  // page load if we ran it automatically. Reset whenever the diff
  // report identity changes.
  let closure = $state<ClosureDiffReport | null>(null);
  let closureLoading = $state(false);
  let closureError = $state<string | null>(null);
  let closureScannedAt = $state<number | null>(null);

  // localStorage persistence for closure scans. Closure walks take
  // 5–30s; throwing them away on every page reload is hostile both to
  // the user and to upstream BCR.
  function persistClosureScan(upstream: string, scan: ClosureDiffReport): void {
    if (typeof localStorage === 'undefined') return;
    try {
      localStorage.setItem(closureStorageKey(upstream, scan.module), JSON.stringify(storedClosureScan(scan)));
    } catch {
      // Quota/private-mode: scan still lives for this session.
    }
  }

  function restoreClosureScan(): void {
    if (typeof localStorage === 'undefined' || !report) return;
    const upstream = page.url.searchParams.get('upstream') ?? '';
    if (!upstream) return; // no upstream → no scans possible
    const key = closureStorageKey(upstream, report.module);
    try {
      const raw = localStorage.getItem(key);
      if (!raw) return;
      const restored = restoreClosureScanForReport(report, parseStoredClosureScan(raw));
      if (restored.stale) {
        localStorage.removeItem(key);
        return;
      }
      closure = restored.closure;
      closureScannedAt = restored.scannedAt;
    } catch {
      try {
        localStorage.removeItem(key);
      } catch {
        /* ignore */
      }
    }
  }

  $effect(() => {
    // Reactive dependency: when the diff report swaps (user navigates
    // to a different pair), drop any in-memory closure result and try
    // to hydrate from localStorage. The restore is no-op if the cached
    // entry doesn't match the new (from, to).
    void report?.module;
    void report?.from;
    void report?.to;
    closure = null;
    closureError = null;
    closureScannedAt = null;
    restoreClosureScan();
  });

  async function scanClosure(): Promise<void> {
    if (!report || closureLoading) return;
    const upstream = page.url.searchParams.get('upstream') ?? '';
    if (!upstream) {
      closureError = 'closure scan requires an upstream URL — re-open this diff via the drift dashboard or append &upstream=...';
      return;
    }
    closureLoading = true;
    closureError = null;
    try {
      const result = await getDiffClosure(report.module, report.from, report.to, upstream);
      closure = result;
      closureScannedAt = Date.now();
      persistClosureScan(upstream, result);
    } catch (e: unknown) {
      closureError = e instanceof Error ? e.message : String(e);
    } finally {
      closureLoading = false;
    }
  }

  // Human-friendly "X ago" for the cached-scan hint. Recomputed on a
  // 60s tick so the badge stays roughly current without manual refresh.
  let nowTick = $state(Date.now());
  $effect(() => {
    const id = setInterval(() => (nowTick = Date.now()), 60_000);
    return () => clearInterval(id);
  });
  const closureScanAge = $derived(relativeScanAge(nowTick, closureScannedAt));

  async function copyMarkdown(): Promise<void> {
    if (!report) return;
    try {
      await navigator.clipboard.writeText(renderDiffMarkdown(report));
      copyState = 'copied';
    } catch {
      copyState = 'error';
    }
    if (copyResetTimer) clearTimeout(copyResetTimer);
    copyResetTimer = setTimeout(() => {
      copyState = 'idle';
      copyResetTimer = null;
    }, 1800);
  }

  const transientSides = $derived(
    report ? transientVersionSides(report) : [],
  );

  const isEmpty = $derived(isEmptyDiffReport(report));

  // breakingOnlyEmpty is true when "breaking only" is on AND none
  // of the three breaking-flavored sections (findings, compat
  // shift, hermeticity) have anything to show. Used to render a
  // distinct empty-state instead of a silent blank canvas.
  const breakingOnlyEmpty = $derived(isBreakingOnlyEmptyReport(report));

  // "Breaking only" filter. When on, hide sections that surface
  // purely-additive deltas (bazel_deps changes, rules/providers/
  // macros/aspects/toolchains/repository_rules/module_extensions
  // sections). The dedicated Breaking findings panel + the
  // compat_level + hermeticity sections stay visible because those
  // are exactly the consumer-visible breakage categories.
  //
  // UI-local state: not persisted across navigations on purpose —
  // each diff view starts in the comprehensive default. Persist
  // later if the toggle gets heavy use.
  let showOnlyBreaking = $state(false);

  // Deep-link bases. Rules added/changed live in the `to` version's module
  // page; removed rules link to the `from` version (where they still exist).
  const toModuleHref = $derived(
    report ? `/modules/${encodeURIComponent(report.module)}/${encodeURIComponent(report.to)}` : '',
  );
  const fromModuleHref = $derived(
    report ? `/modules/${encodeURIComponent(report.module)}/${encodeURIComponent(report.from)}` : '',
  );
</script>

<svelte:head>
  <title>{report ? `${report.module} ${report.from} → ${report.to}` : 'diff'} — canopy</title>
</svelte:head>

<div class="flex flex-col gap-6">
  <nav class="text-[11px] text-fg-dim font-mono flex items-center gap-1.5" aria-label="breadcrumb">
    <a href="/" class="hover:text-fg transition-colors">canopy</a>
    <span class="text-fg-dim/60">/</span>
    <span class="text-fg-mute">modules</span>
    <span class="text-fg-dim/60">/</span>
    <a
      href={`/modules/${page.params.name}/${page.url.searchParams.get('to') ?? ''}`}
      class="hover:text-fg transition-colors text-fg-mute"
    >
      {page.params.name}
    </a>
    <span class="text-fg-dim/60">/</span>
    <span class="text-fg">diff</span>
  </nav>

  <header class="flex flex-col gap-2 pb-4 border-b border-line">
    <div class="flex items-baseline gap-3 flex-wrap">
      <h1 class="font-mono text-2xl text-fg tracking-tight">
        {page.params.name}
        <span class="text-fg-dim mx-2">·</span>
        <span class="text-fg-mute">{page.url.searchParams.get('from') ?? '?'}</span>
        <span class="text-fg-dim mx-2">→</span>
        <span class="text-fg">{page.url.searchParams.get('to') ?? '?'}</span>
      </h1>
      {#if report && !isEmpty}
        <button
          type="button"
          onclick={copyMarkdown}
          class="ml-auto text-[11px] font-mono px-2 py-1 rounded-md border border-line hover:border-accent/60 hover:text-accent transition-colors cursor-pointer shrink-0
            {copyState === 'copied' ? 'border-ok/60 text-ok' : ''}
            {copyState === 'error' ? 'border-err/60 text-err' : ''}"
          title="copy a PR-body-ready markdown summary of this diff"
          aria-live="polite"
        >
          {copyState === 'copied' ? '✓ copied' : copyState === 'error' ? '✗ clipboard blocked' : 'copy as markdown'}
        </button>
      {/if}
    </div>
    <p class="text-[12px] text-fg-mute">structured migration delta</p>

    {#if report && !isEmpty}
      <label class="text-[11px] text-fg-dim flex items-center gap-1.5 pt-1 cursor-pointer w-fit">
        <input type="checkbox" bind:checked={showOnlyBreaking} class="accent-accent" />
        breaking only
        <span class="text-fg-dim/70">— hide additive deltas; keep breaking findings, compat shift, hermeticity</span>
      </label>
    {/if}

    {#if versionEntries.length > 1}
      <!--
        In-page version pickers. Lets users re-pivot the diff
        without navigating back to the versions list. Each select
        is keyed off the URL search params so reloads / shared
        links restore the exact pair.
      -->
      <div class="flex items-center gap-2 flex-wrap text-[11px] font-mono pt-2">
        <span class="text-fg-dim">from</span>
        <select
          class="rounded border border-line bg-bg-elev px-2 py-1 text-fg outline-none focus:border-accent"
          value={page.url.searchParams.get('from') ?? ''}
          onchange={onFromChange}
        >
          {#each versionEntries as e (e.version)}
            <option value={e.version}>{e.version}{e.is_stub ? ' (stub)' : ''}</option>
          {/each}
        </select>
        <button
          type="button"
          onclick={onSwap}
          class="px-1.5 py-0.5 rounded border border-line text-fg-mute hover:text-accent hover:border-accent/60"
          title="swap from and to"
          aria-label="swap from and to"
        >
          ↔
        </button>
        <span class="text-fg-dim">to</span>
        <select
          class="rounded border border-line bg-bg-elev px-2 py-1 text-fg outline-none focus:border-accent"
          value={page.url.searchParams.get('to') ?? ''}
          onchange={onToChange}
        >
          {#each versionEntries as e (e.version)}
            <option value={e.version}>{e.version}{e.is_stub ? ' (stub)' : ''}</option>
          {/each}
        </select>
      </div>
    {/if}
  </header>

  {#if transientSides.length > 0}
    <div class="rounded-md border border-accent/30 bg-accent/5 px-3 py-2 text-[12px] flex items-baseline gap-2 flex-wrap">
      <span class="text-[10px] uppercase tracking-wide text-accent font-medium">what-if</span>
      <span class="text-fg-mute">
        {transientSides.length === 1 ? 'version' : 'versions'}
        <span class="font-mono text-fg">{transientSides.join(', ')}</span>
        fetched from upstream on the fly — nothing was written to the local index.
      </span>
    </div>
  {/if}

  {#if report && report.breaking && report.breaking.length > 0}
    <section
      class="rounded-md border border-err/40 bg-err/5 p-4 flex flex-col gap-2"
      aria-labelledby="breaking-heading"
    >
      <div class="flex items-baseline gap-2">
        <span class="text-err font-mono text-[16px]" aria-hidden="true">!</span>
        <h2 id="breaking-heading" class="text-[13px] font-medium text-err tracking-wide uppercase">
          Breaking · {report.breaking.length} finding{report.breaking.length === 1 ? '' : 's'}
        </h2>
      </div>
      <p class="text-[12px] text-fg-mute leading-relaxed">
        Consumers exercising these surfaces will need code changes to migrate to
        <span class="font-mono text-fg">{report.to}</span>.
      </p>
      <ul class="flex flex-col gap-2 text-[12px] font-mono">
        {#each report.breaking as f (f.kind + f.symbol + (f.detail ?? ''))}
          <li class="flex flex-col gap-0.5">
            <div class="flex items-baseline gap-2 flex-wrap">
              <span class="text-[10px] uppercase tracking-wide text-err shrink-0">{f.kind.replaceAll('_', ' ')}</span>
              <span class="text-fg">
                {f.symbol}{#if f.detail}<span class="text-fg-dim">.</span>{f.detail}{/if}
              </span>
              <span class="text-fg-mute text-[11px] leading-snug">— {f.reason}</span>
            </div>
            {#if f.hint}
              <!--
                Migration hint: imperative, prescribes the concrete
                edit the consumer needs to make. The differentiator
                vs other ecosystem registries that only describe the
                breakage. Inline backticks (rendered by the global
                CSS as code spans) call out identifiers.
              -->
              <div class="text-[11px] text-accent leading-snug pl-[60px] sm:pl-[68px]">
                <span class="text-accent">→</span>
                {#each splitHint(f.hint) as seg, i (i)}
                  {#if seg.code}
                    <code class="bg-bg-elev px-1 py-0.5 rounded text-fg">{seg.text}</code>
                  {:else}
                    <span>{seg.text}</span>
                  {/if}
                {/each}
              </div>
            {/if}
          </li>
        {/each}
      </ul>
    </section>
  {/if}

  {#if report && !isEmpty && !showOnlyBreaking}
    <section class="rounded-md border border-line bg-bg-elev/30 p-4 flex flex-col gap-3">
      <div class="flex items-baseline gap-3 flex-wrap">
        <h2 class="text-xs uppercase tracking-wide text-fg-dim">closure impact</h2>
        <span class="text-[11px] text-fg-mute">
          MVS-walked bazel_dep closure — surfaces transitive breaking impact you can't see from the root alone
        </span>
        {#if !closure && !closureLoading}
          <button
            type="button"
            onclick={() => void scanClosure()}
            disabled={!page.url.searchParams.get('upstream')}
            class="ml-auto text-[11px] font-mono px-2 py-1 rounded-md border border-line hover:border-accent/60 hover:text-accent transition-colors cursor-pointer shrink-0 disabled:opacity-40 disabled:cursor-not-allowed"
            title={page.url.searchParams.get('upstream')
              ? 'walks bazel_dep closure on both sides via MVS, then per-module diff on every shifted dep — typically 5–30s'
              : 'closure scan requires an upstream URL (this diff was opened without one)'}
          >
            scan closure impact
          </button>
        {/if}
        {#if closureLoading}
          <span class="ml-auto text-[11px] text-fg-dim font-mono shrink-0">walking closure…</span>
        {/if}
        {#if closure && !closureLoading}
          <span class="ml-auto text-[11px] text-fg-dim font-mono shrink-0" title={closureScannedAt ? new Date(closureScannedAt).toLocaleString() : ''}>
            scanned {closureScanAge}
          </span>
          <button
            type="button"
            onclick={() => void scanClosure()}
            class="text-[11px] font-mono px-2 py-1 rounded-md border border-line hover:border-accent/60 hover:text-accent transition-colors cursor-pointer shrink-0"
            title="re-run the closure scan (fresh fetch from upstream)"
          >
            re-scan
          </button>
        {/if}
      </div>

      {#if closureError}
        <p class="text-[12px] text-err font-mono leading-relaxed">{closureError}</p>
      {/if}

      {#if closure}
        <p class="text-[12px] text-fg-mute font-mono">
          closure: <span class="text-fg">{closure.from_closure_size}</span>
          <span class="mx-1">→</span>
          <span class="text-fg">{closure.to_closure_size}</span>
          modules
          {#if (closure.closure_deps.added?.length ?? 0) + (closure.closure_deps.removed?.length ?? 0) > 0}
            <span class="text-fg-dim mx-2">·</span>
            {#if closure.closure_deps.added?.length}+{closure.closure_deps.added.length}{/if}
            {#if closure.closure_deps.removed?.length}<span class="mx-1">−{closure.closure_deps.removed.length}</span>{/if}
          {/if}
        </p>

        {#if closure.closure_breaking_total > 0 && closure.closure_breaking_by_module}
          <div class="rounded-md border border-err/40 bg-err/5 px-3 py-2 flex flex-col gap-1">
            <p class="text-[11px] uppercase tracking-wide text-err font-medium">
              ⚠ Closure-wide breaking · {closure.closure_breaking_total} finding{closure.closure_breaking_total === 1 ? '' : 's'}
              across {Object.keys(closure.closure_breaking_by_module).length}
              module{Object.keys(closure.closure_breaking_by_module).length === 1 ? '' : 's'}
            </p>
            <ul class="flex flex-col gap-0.5 text-[12px] font-mono">
              {#each Object.entries(closure.closure_breaking_by_module).sort(([a], [b]) => a.localeCompare(b)) as [name, count] (name)}
                {@const md = closure.module_diffs?.[name]}
                <li class="flex items-baseline gap-2 flex-wrap">
                  <span class="text-err shrink-0">!</span>
                  {#if md}
                    <a
                      href={`/modules/${encodeURIComponent(name)}/diff?from=${encodeURIComponent(md.from)}&to=${encodeURIComponent(md.to)}&upstream=${encodeURIComponent(page.url.searchParams.get('upstream') ?? '')}`}
                      class="text-fg hover:text-accent transition-colors"
                    >
                      {name}
                    </a>
                    <span class="text-fg-mute text-[11px]">{md.from} → {md.to}</span>
                  {:else}
                    <span class="text-fg">{name}</span>
                  {/if}
                  <span class="text-fg-mute text-[11px] ml-auto">{count} breaking</span>
                </li>
              {/each}
            </ul>
          </div>
        {:else if closure.closure_breaking_total === 0}
          <p class="text-[12px] text-ok font-mono">✓ no structural breaks in the closure — safe across all transitive deps</p>
        {/if}

        {#if closure.errors_by_module && Object.keys(closure.errors_by_module).length > 0}
          <details class="text-[11px] font-mono text-fg-mute">
            <summary class="cursor-pointer">
              {Object.keys(closure.errors_by_module).length}
              analysis gap{Object.keys(closure.errors_by_module).length === 1 ? '' : 's'} —
              modules that couldn't be analyzed
            </summary>
            <ul class="mt-1 pl-3 flex flex-col gap-0.5">
              {#each Object.entries(closure.errors_by_module).sort(([a], [b]) => a.localeCompare(b)) as [name, err] (name)}
                <li class="text-fg-dim"><span class="text-fg-mute">{name}</span>: {err}</li>
              {/each}
            </ul>
          </details>
        {/if}
      {/if}
    </section>
  {/if}

  {#if loading}
    <div class="flex flex-col gap-2">
      {#each Array.from({ length: 4 }) as _, i (i)}
        <div class="skeleton h-16 w-full"></div>
      {/each}
    </div>
  {:else if error}
    <div class="rounded-md border border-err/30 bg-err/10 px-4 py-3 text-[13px] flex flex-col gap-1">
      <p class="text-err font-medium">can't compute diff</p>
      <p class="text-fg-mute leading-relaxed">{error}</p>
    </div>
  {:else if isEmpty}
    <p class="text-center py-12 text-fg-mute font-mono text-sm">
      no public-surface differences detected — these versions look identical to consumers.
    </p>
  {:else if report && showOnlyBreaking && breakingOnlyEmpty}
    <p class="text-center py-12 text-fg-mute font-mono text-sm">
      no breaking changes — this bump is purely additive. uncheck <em class="not-italic text-fg">breaking only</em> to see the full delta.
    </p>
  {:else if report}
    {#if report.compatibility_level}
      <section class="flex items-center gap-3 rounded-md border border-warn/30 bg-warn/5 px-4 py-3">
        <span class="text-[10px] uppercase tracking-wide text-warn font-medium">compat level</span>
        <span class="font-mono text-[14px]">
          L{report.compatibility_level.from}
          <span class="text-fg-dim mx-1">→</span>
          L{report.compatibility_level.to}
        </span>
        <span class="text-[12px] text-fg-mute">
          Bazel treats different compatibility_levels as incompatible — likely a hard migration.
        </span>
      </section>
    {/if}

    {#if report.hermeticity && ((report.hermeticity.added?.length ?? 0) + (report.hermeticity.removed?.length ?? 0)) > 0}
      <section class="flex flex-col gap-2">
        <h2 class="text-xs uppercase tracking-wide text-fg-dim">hermeticity</h2>
        <div class="flex flex-wrap items-baseline gap-2 text-[12px] font-mono">
          {#each report.hermeticity.added ?? [] as cls (cls)}
            <span class="rounded border border-ok/30 bg-ok/10 px-2 py-0.5 text-ok">+ {cls}</span>
          {/each}
          {#each report.hermeticity.removed ?? [] as cls (cls)}
            <span class="rounded border border-err/30 bg-err/10 px-2 py-0.5 text-err">− {cls}</span>
          {/each}
        </div>
      </section>
    {/if}

    {#if !showOnlyBreaking && ((report.bazel_deps?.added?.length ?? 0) + (report.bazel_deps?.removed?.length ?? 0) + (report.bazel_deps?.changed?.length ?? 0) > 0)}
      <section class="flex flex-col gap-2">
        <h2 class="text-xs uppercase tracking-wide text-fg-dim">bazel_deps</h2>
        <ul class="flex flex-col gap-1 font-mono text-[12px]">
          {#each report.bazel_deps.changed ?? [] as d (d.name)}
            <li class="flex items-baseline gap-2">
              <span class="text-warn">~</span>
              <span class="text-fg min-w-[180px]">{d.name}</span>
              <span class="text-fg-mute">{d.from_version}</span>
              <span class="text-fg-dim mx-1">→</span>
              <span class="text-fg">{d.to_version}</span>
            </li>
          {/each}
          {#each report.bazel_deps.added ?? [] as d (d.name)}
            <li class="flex items-baseline gap-2">
              <span class="text-ok">+</span>
              <span class="text-fg min-w-[180px]">{d.name}</span>
              <span class="text-fg-mute">@ {d.version}</span>
            </li>
          {/each}
          {#each report.bazel_deps.removed ?? [] as d (d.name)}
            <li class="flex items-baseline gap-2">
              <span class="text-err">−</span>
              <span class="text-fg-dim line-through min-w-[180px]">{d.name}</span>
              <span class="text-fg-dim">@ {d.version}</span>
            </li>
          {/each}
        </ul>
      </section>
    {/if}

    {#snippet rulesSection(
      title: string,
      hint: string | undefined,
      data: DiffRules,
      anchorPrefix: string,
    )}
      <section class="flex flex-col gap-3">
        <h2 class="text-xs uppercase tracking-wide text-fg-dim">
          {title}
          {#if hint}
            <span class="text-fg-dim/70 normal-case ml-1 text-[10px]">({hint})</span>
          {/if}
        </h2>

        {#if data.changed && data.changed.length > 0}
          <div class="flex flex-col gap-2">
            {#each data.changed as ch (ch.name)}
              <details class="diff-changed border border-warn/30 rounded-md bg-warn/5">
                <summary class="cursor-pointer px-3 py-2 flex items-baseline gap-2 text-[12px] font-mono select-none">
                  <span
                    class="disclosure inline-block text-fg-dim transition-transform"
                    aria-hidden="true"
                  >▸</span>
                  <span class="text-warn">~</span>
                  <a
                    href={`${toModuleHref}#${anchorPrefix}${ch.name}`}
                    class="text-fg hover:text-accent transition-colors"
                    onclick={(e) => e.stopPropagation()}
                    title={`open ${ch.name} on the ${report?.to} module page`}
                  >
                    {ch.name}
                  </a>
                  <span class="text-fg-mute text-[11px]">
                    {#if ch.attrs_added}+{ch.attrs_added.length} attr{ch.attrs_added.length === 1 ? '' : 's'}{/if}
                    {#if ch.attrs_removed && ch.attrs_added}<span class="mx-1">·</span>{/if}
                    {#if ch.attrs_removed}−{ch.attrs_removed.length} attr{ch.attrs_removed.length === 1 ? '' : 's'}{/if}
                    {#if ch.attrs_changed && (ch.attrs_added || ch.attrs_removed)}<span class="mx-1">·</span>{/if}
                    {#if ch.attrs_changed}~{ch.attrs_changed.length} attr{ch.attrs_changed.length === 1 ? '' : 's'}{/if}
                  </span>
                  <span class="text-[10px] text-fg-dim ml-auto">click to expand</span>
                </summary>
                <div class="px-3 pb-3 pt-1 border-t border-warn/30 flex flex-col gap-2 text-[12px] font-mono">
                  {#if ch.attrs_added && ch.attrs_added.length > 0}
                    <div>
                      <span class="text-[10px] uppercase tracking-wide text-ok mr-2">added</span>
                      {#each ch.attrs_added as a (a.name)}
                        <span class="inline-block mr-3 text-fg">
                          {a.name}<span class="text-fg-dim">: {a.type || 'any'}</span>
                          {#if a.mandatory}<span class="text-warn ml-1">(required)</span>{/if}
                        </span>
                      {/each}
                    </div>
                  {/if}
                  {#if ch.attrs_removed && ch.attrs_removed.length > 0}
                    <div>
                      <span class="text-[10px] uppercase tracking-wide text-err mr-2">removed</span>
                      {#each ch.attrs_removed as a (a.name)}
                        <span class="inline-block mr-3 text-fg-dim line-through">
                          {a.name}<span class="text-fg-dim">: {a.type || 'any'}</span>
                        </span>
                      {/each}
                    </div>
                  {/if}
                  {#if ch.attrs_changed && ch.attrs_changed.length > 0}
                    <div class="flex flex-col gap-1">
                      <span class="text-[10px] uppercase tracking-wide text-warn">changed</span>
                      {#each ch.attrs_changed as a (a.name)}
                        <div class="text-fg-mute pl-2">
                          <span class="text-fg">{a.name}</span>
                          {#if a.from_type || a.to_type}
                            <span class="ml-2">
                              type: <span class="text-fg-dim">{a.from_type || '—'}</span>
                              <span class="mx-1">→</span>
                              <span class="text-fg">{a.to_type || '—'}</span>
                            </span>
                          {/if}
                          {#if a.from_default !== undefined && a.to_default !== undefined}
                            <span class="ml-2">
                              default: <span class="text-fg-dim">{a.from_default || '—'}</span>
                              <span class="mx-1">→</span>
                              <span class="text-fg">{a.to_default || '—'}</span>
                            </span>
                          {/if}
                          {#if a.mandatory_flip}
                            <span class="ml-2 text-warn">
                              mandatory: {a.from_mandatory ? 'yes' : 'no'}
                              <span class="mx-1">→</span>
                              {a.to_mandatory ? 'yes' : 'no'}
                            </span>
                          {/if}
                        </div>
                      {/each}
                    </div>
                  {/if}
                </div>
              </details>
            {/each}
          </div>
        {/if}

        <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
          {#if data.added && data.added.length > 0}
            <div class="flex flex-col gap-1">
              <span class="text-[10px] uppercase tracking-wide text-ok">added · {data.added.length}</span>
              <ul class="font-mono text-[12px] text-fg flex flex-wrap gap-x-3">
                {#each data.added as n (n)}
                  <li>
                    <a
                      href={`${toModuleHref}#${anchorPrefix}${n}`}
                      class="hover:text-accent transition-colors"
                    >
                      + {n}
                    </a>
                  </li>
                {/each}
              </ul>
            </div>
          {/if}
          {#if data.removed && data.removed.length > 0}
            <div class="flex flex-col gap-1">
              <span class="text-[10px] uppercase tracking-wide text-err">removed · {data.removed.length}</span>
              <ul class="font-mono text-[12px] text-fg-dim flex flex-wrap gap-x-3">
                {#each data.removed as n (n)}
                  <li>
                    <a
                      href={`${fromModuleHref}#${anchorPrefix}${n}`}
                      class="line-through hover:text-accent transition-colors"
                      title={`open ${n} on the ${report?.from} module page (where it still exists)`}
                    >
                      {n}
                    </a>
                  </li>
                {/each}
              </ul>
            </div>
          {/if}
        </div>
      </section>
    {/snippet}

    {#if !showOnlyBreaking && ((report.rules?.added?.length ?? 0) + (report.rules?.removed?.length ?? 0) + (report.rules?.changed?.length ?? 0) > 0)}
      {@render rulesSection('rules', undefined, report.rules, 'rule-')}
    {/if}

    {#if !showOnlyBreaking && ((report.providers?.added?.length ?? 0) + (report.providers?.removed?.length ?? 0) + (report.providers?.changed?.length ?? 0) > 0)}
      <section class="flex flex-col gap-2">
        <h2 class="text-xs uppercase tracking-wide text-fg-dim">providers</h2>
        <div class="grid grid-cols-1 sm:grid-cols-2 gap-3 font-mono text-[12px]">
          {#if report.providers.added && report.providers.added.length > 0}
            <div>
              <span class="text-[10px] uppercase tracking-wide text-ok">added · {report.providers.added.length}</span>
              <ul class="text-fg flex flex-wrap gap-x-3">
                {#each report.providers.added as n (n)}<li>+ {n}</li>{/each}
              </ul>
            </div>
          {/if}
          {#if report.providers.removed && report.providers.removed.length > 0}
            <div>
              <span class="text-[10px] uppercase tracking-wide text-err">removed · {report.providers.removed.length}</span>
              <ul class="text-fg-dim flex flex-wrap gap-x-3">
                {#each report.providers.removed as n (n)}<li class="line-through">{n}</li>{/each}
              </ul>
            </div>
          {/if}
        </div>
        {#if report.providers.changed && report.providers.changed.length > 0}
          <div class="flex flex-col gap-1 font-mono text-[12px]">
            {#each report.providers.changed as ch (ch.name)}
              <div class="text-fg">
                <span class="text-warn">~</span>
                {ch.name}
                {#if ch.fields_added}<span class="ml-2 text-ok">+fields: {ch.fields_added.join(', ')}</span>{/if}
                {#if ch.fields_removed}<span class="ml-2 text-err">−fields: {ch.fields_removed.join(', ')}</span>{/if}
              </div>
            {/each}
          </div>
        {/if}
      </section>
    {/if}

    {#if !showOnlyBreaking && ((report.macros?.added?.length ?? 0) + (report.macros?.removed?.length ?? 0) > 0)}
      <section class="flex flex-col gap-2">
        <h2 class="text-xs uppercase tracking-wide text-fg-dim">macros</h2>
        <div class="grid grid-cols-1 sm:grid-cols-2 gap-3 font-mono text-[12px]">
          {#if report.macros.added && report.macros.added.length > 0}
            <div>
              <span class="text-[10px] uppercase tracking-wide text-ok">added · {report.macros.added.length}</span>
              <ul class="text-fg flex flex-wrap gap-x-3">
                {#each report.macros.added as n (n)}<li>+ {n}</li>{/each}
              </ul>
            </div>
          {/if}
          {#if report.macros.removed && report.macros.removed.length > 0}
            <div>
              <span class="text-[10px] uppercase tracking-wide text-err">removed · {report.macros.removed.length}</span>
              <ul class="text-fg-dim flex flex-wrap gap-x-3">
                {#each report.macros.removed as n (n)}<li class="line-through">{n}</li>{/each}
              </ul>
            </div>
          {/if}
        </div>
      </section>
    {/if}

    {#if !showOnlyBreaking && ((report.module_extensions?.added?.length ?? 0) + (report.module_extensions?.removed?.length ?? 0) + (report.module_extensions?.changed?.length ?? 0) > 0)}
      <section class="flex flex-col gap-2">
        <h2 class="text-xs uppercase tracking-wide text-fg-dim">
          module_extensions
          <span class="text-fg-dim/70 normal-case ml-1 text-[10px]">
            (use_extension surface — most impactful for Bzlmod consumers)
          </span>
        </h2>
        <div class="grid grid-cols-1 sm:grid-cols-2 gap-3 font-mono text-[12px]">
          {#if report.module_extensions.added && report.module_extensions.added.length > 0}
            <div>
              <span class="text-[10px] uppercase tracking-wide text-ok">added · {report.module_extensions.added.length}</span>
              <ul class="text-fg flex flex-wrap gap-x-3">
                {#each report.module_extensions.added as n (n)}<li>+ {n}</li>{/each}
              </ul>
            </div>
          {/if}
          {#if report.module_extensions.removed && report.module_extensions.removed.length > 0}
            <div>
              <span class="text-[10px] uppercase tracking-wide text-err">removed · {report.module_extensions.removed.length}</span>
              <ul class="text-fg-dim flex flex-wrap gap-x-3">
                {#each report.module_extensions.removed as n (n)}<li class="line-through">{n}</li>{/each}
              </ul>
            </div>
          {/if}
        </div>
        {#if report.module_extensions.changed && report.module_extensions.changed.length > 0}
          <div class="flex flex-col gap-1 font-mono text-[12px]">
            {#each report.module_extensions.changed as ch (ch.name)}
              <div class="text-fg flex flex-wrap items-baseline gap-2">
                <span class="text-warn">~</span>
                <span>{ch.name}</span>
                {#if ch.tag_classes_added && ch.tag_classes_added.length > 0}
                  <span class="text-ok">+tag_classes: {ch.tag_classes_added.join(', ')}</span>
                {/if}
                {#if ch.tag_classes_removed && ch.tag_classes_removed.length > 0}
                  <span class="text-err">−tag_classes: {ch.tag_classes_removed.join(', ')}</span>
                {/if}
              </div>
            {/each}
          </div>
        {/if}
      </section>
    {/if}

    {#if !showOnlyBreaking && ((report.repository_rules?.added?.length ?? 0) + (report.repository_rules?.removed?.length ?? 0) + (report.repository_rules?.changed?.length ?? 0) > 0)}
      {@render rulesSection(
        'repository_rules',
        'WORKSPACE-era external-repo surface — http_archive, git_repository, etc.',
        report.repository_rules,
        'repo-rule-',
      )}
    {/if}

    {#if !showOnlyBreaking && ((report.aspects?.added?.length ?? 0) + (report.aspects?.removed?.length ?? 0) > 0)}
      <section class="flex flex-col gap-2">
        <h2 class="text-xs uppercase tracking-wide text-fg-dim">aspects</h2>
        <div class="grid grid-cols-1 sm:grid-cols-2 gap-3 font-mono text-[12px]">
          {#if report.aspects.added && report.aspects.added.length > 0}
            <div>
              <span class="text-[10px] uppercase tracking-wide text-ok">added · {report.aspects.added.length}</span>
              <ul class="text-fg flex flex-wrap gap-x-3">
                {#each report.aspects.added as n (n)}<li>+ {n}</li>{/each}
              </ul>
            </div>
          {/if}
          {#if report.aspects.removed && report.aspects.removed.length > 0}
            <div>
              <span class="text-[10px] uppercase tracking-wide text-err">removed · {report.aspects.removed.length}</span>
              <ul class="text-fg-dim flex flex-wrap gap-x-3">
                {#each report.aspects.removed as n (n)}<li class="line-through">{n}</li>{/each}
              </ul>
            </div>
          {/if}
        </div>
      </section>
    {/if}

    {#if !showOnlyBreaking && ((report.toolchains?.added?.length ?? 0) + (report.toolchains?.removed?.length ?? 0) > 0)}
      <section class="flex flex-col gap-2">
        <h2 class="text-xs uppercase tracking-wide text-fg-dim">toolchains</h2>
        <div class="grid grid-cols-1 sm:grid-cols-2 gap-3 font-mono text-[12px]">
          {#if report.toolchains.added && report.toolchains.added.length > 0}
            <div>
              <span class="text-[10px] uppercase tracking-wide text-ok">added · {report.toolchains.added.length}</span>
              <ul class="text-fg flex flex-wrap gap-x-3">
                {#each report.toolchains.added as n (n)}<li>+ {n}</li>{/each}
              </ul>
            </div>
          {/if}
          {#if report.toolchains.removed && report.toolchains.removed.length > 0}
            <div>
              <span class="text-[10px] uppercase tracking-wide text-err">removed · {report.toolchains.removed.length}</span>
              <ul class="text-fg-dim flex flex-wrap gap-x-3">
                {#each report.toolchains.removed as n (n)}<li class="line-through">{n}</li>{/each}
              </ul>
            </div>
          {/if}
        </div>
      </section>
    {/if}
  {/if}
</div>

<style>
  /*
    Native <details>/<summary> ships with a browser-default disclosure
    triangle that doesn't match our typography. Hide it and use the
    `.disclosure` span we render inside <summary> instead, which we
    can rotate on [open] for a consistent affordance.
  */
  .diff-changed > summary {
    list-style: none;
  }
  .diff-changed > summary::-webkit-details-marker {
    display: none;
  }
  .diff-changed[open] > summary .disclosure {
    transform: rotate(90deg);
  }
  .diff-changed[open] > summary .ml-auto {
    /* Hide the "click to expand" hint once the user has expanded. */
    display: none;
  }
</style>
