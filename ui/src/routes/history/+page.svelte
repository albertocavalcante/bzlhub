<script lang="ts">
  import { paths } from '$lib/api/paths';
  // /history — audit log table.
  //
  // Surfaces every write operation that bzlhub recorded: bump_success /
  // bump_failure / ingest_recursive_success / ingest_recursive_failure
  // and similar. Reads are deliberately not in this table.
  //
  // Auto-refresh: subscribes to /api/v1/activity/events for "audit_recorded" events
  // and prepends new rows without a full reload.

  import { getHistory } from '$api/client';
  import type { AuditEvent } from '$api/types';
  import { codeNavRootHref, moduleVersionHref } from '$lib/links';
  import { page } from '$app/state';
  import { readParam, writeParam, stringField } from '$lib/url-state';
  import ShareLink from '$components/ShareLink.svelte';

  let events = $state<AuditEvent[]>([]);
  let loading = $state(true);
  let error = $state<string | null>(null);

  // URL-bound facets: ?kind=<x>&source=<y>. Empty string = no filter.
  // Plan 14 principle 1: filters live in the URL so the audit-view
  // is shareable. URL → state and state → URL effects below maintain
  // bidirectional sync; guards break the potential loop.
  let kindFilter = $state<string>(readParam(page.url, 'kind', stringField));
  let sourceFilter = $state<string>(readParam(page.url, 'source', stringField));

  $effect(() => {
    const k = readParam(page.url, 'kind', stringField);
    if (k !== kindFilter) kindFilter = k;
  });
  $effect(() => {
    const s = readParam(page.url, 'source', stringField);
    if (s !== sourceFilter) sourceFilter = s;
  });
  $effect(() => {
    writeParam(page.url, 'kind', kindFilter, stringField);
  });
  $effect(() => {
    writeParam(page.url, 'source', sourceFilter, stringField);
  });

  async function load() {
    loading = true;
    error = null;
    try {
      const r = await getHistory({
        kind: kindFilter ? [kindFilter] : undefined,
        source: sourceFilter || undefined,
        limit: 200,
      });
      events = r.events ?? [];
    } catch (e: unknown) {
      error = e instanceof Error ? e.message : String(e);
      events = [];
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    void load();
  });

  // Live updates: any audit_recorded event triggers a refresh. We
  // could prepend the new event without a full re-fetch for lower
  // latency, but a refresh is dead-simple and the table is small.
  $effect(() => {
    const es = new EventSource(paths.activity.events());
    let debounce: ReturnType<typeof setTimeout> | null = null;
    const refresh = () => {
      if (debounce) clearTimeout(debounce);
      debounce = setTimeout(() => void load(), 200);
    };
    es.addEventListener('audit_recorded', refresh);
    return () => {
      es.close();
      if (debounce) clearTimeout(debounce);
    };
  });

  // Relative-time formatter. Avoids pulling in date-fns for one helper.
  function rel(ts: string): string {
    const then = new Date(ts).getTime();
    const now = Date.now();
    const dt = Math.max(0, Math.floor((now - then) / 1000));
    if (dt < 5) return 'just now';
    if (dt < 60) return `${dt}s ago`;
    if (dt < 3600) return `${Math.floor(dt / 60)}m ago`;
    if (dt < 86400) return `${Math.floor(dt / 3600)}h ago`;
    return `${Math.floor(dt / 86400)}d ago`;
  }

  // Kind taxonomy mapping → display label + style tier.
  function kindStyle(kind: string): { label: string; cls: string } {
    switch (kind) {
      case 'bump_success':
        return { label: 'bump', cls: 'text-ok' };
      case 'bump_failure':
        return { label: 'bump ✗', cls: 'text-err' };
      case 'ingest_recursive_success':
        return { label: 'ingest closure', cls: 'text-ok' };
      case 'ingest_recursive_failure':
        return { label: 'ingest closure ✗', cls: 'text-err' };
      case 'ingest_module_failure':
        return { label: 'module ingest ✗', cls: 'text-err' };
      default:
        return { label: kind, cls: 'text-fg-mute' };
    }
  }

  // Compact payload renderer. We only surface the most useful fields
  // per kind to keep the table scannable.
  function payloadHint(ev: AuditEvent): string {
    const p = ev.payload as Record<string, unknown> | undefined;
    if (!p) return '';
    if ('visited' in p && 'mirrored' in p) {
      const errs = (p.errors as number | undefined) ?? 0;
      return `visited ${p.visited} · mirrored ${p.mirrored}${errs ? ` · err ${errs}` : ''}`;
    }
    if ('rules' in p) {
      return `rules ${p.rules} · providers ${p.providers} · macros ${p.macros}`;
    }
    return '';
  }
</script>

<svelte:head>
  <title>history — bzlhub</title>
</svelte:head>

<div class="flex flex-col gap-4">
  <header class="flex flex-col gap-3 pb-3 border-b border-line">
    <div class="flex items-baseline justify-between gap-3 flex-wrap">
      <div class="flex items-baseline gap-3 flex-wrap">
        <h1 class="font-mono text-2xl text-fg tracking-tight">history</h1>
        <p class="text-[12px] text-fg-mute">
          every write operation bzlhub ran — bumps and closure ingests across all surfaces
        </p>
      </div>
      <ShareLink />
    </div>

    <div class="flex items-baseline gap-3 flex-wrap text-[11px]">
      <span class="text-fg-dim uppercase tracking-wide">filter:</span>
      <select
        bind:value={kindFilter}
        class="rounded-md border border-line bg-bg-elev px-2 py-1 text-[12px] font-mono text-fg outline-none focus:border-accent"
        onchange={load}
      >
        <option value="">all kinds</option>
        <option value="bump_success">bump_success</option>
        <option value="bump_failure">bump_failure</option>
        <option value="ingest_recursive_success">ingest_recursive_success</option>
        <option value="ingest_recursive_failure">ingest_recursive_failure</option>
        <option value="ingest_module_failure">ingest_module_failure</option>
      </select>
      <select
        bind:value={sourceFilter}
        class="rounded-md border border-line bg-bg-elev px-2 py-1 text-[12px] font-mono text-fg outline-none focus:border-accent"
        onchange={load}
      >
        <option value="">all sources</option>
        <option value="drift-ui">drift-ui</option>
        <option value="rest">rest</option>
        <option value="mcp">mcp</option>
        <option value="cli">cli</option>
      </select>
      <button
        type="button"
        class="rounded-md border border-line bg-bg-elev px-2 py-1 text-fg-mute hover:text-fg hover:border-line-strong cursor-pointer"
        onclick={load}
      >
        refresh
      </button>
    </div>
  </header>

  {#if error}
    <div class="rounded-md border border-err/30 bg-err/10 px-3 py-2 text-sm text-err" role="alert">
      {error}
    </div>
  {:else if loading && events.length === 0}
    <div class="flex flex-col gap-1">
      {#each Array.from({ length: 6 }) as _, i (i)}
        <div class="skeleton h-9 w-full"></div>
      {/each}
    </div>
  {:else if events.length === 0}
    <p class="text-center py-16 text-fg-dim font-mono text-sm">
      no write operations recorded yet
    </p>
  {:else}
    <p class="text-[11px] text-fg-mute font-mono">{events.length} most-recent events</p>
    <table class="w-full text-[12px] font-mono">
      <thead>
        <tr class="text-[10px] uppercase tracking-wide text-fg-dim border-b border-line/60">
          <th class="text-left font-medium py-1.5 pr-3">when</th>
          <th class="text-left font-medium py-1.5 pr-3">kind</th>
          <th class="text-left font-medium py-1.5 pr-3">source</th>
          <th class="text-left font-medium py-1.5 pr-3">target</th>
          <th class="text-left font-medium py-1.5 pr-3">detail</th>
          <th class="text-right font-medium py-1.5">dur</th>
        </tr>
      </thead>
      <tbody>
        {#each events as ev (ev.id)}
          {@const k = kindStyle(ev.kind)}
          <tr class="border-b border-line/30 hover:bg-bg-elev/50">
            <td class="py-1.5 pr-3 text-fg-mute" title={ev.timestamp}>{rel(ev.timestamp)}</td>
            <td class="py-1.5 pr-3 {k.cls}">{k.label}</td>
            <td class="py-1.5 pr-3 text-fg-mute">{ev.source}</td>
            <td class="py-1.5 pr-3 text-fg">
              {#if ev.module && ev.version}
                <!--
                  Link to /modules/<m>/<v> (the module page) as the
                  primary action. Code-nav lives one hop deeper and
                  fits as a secondary affordance below — keeps the
                  history table scannable while restoring the click
                  paths that used to land on plain text.
                -->
                <a
                  href={moduleVersionHref(ev.module, ev.version)}
                  class="hover:text-accent hover:underline"
                >{ev.module}<span class="text-fg-dim">@</span>{ev.version}</a>
                <a
                  href={codeNavRootHref(ev.module, ev.version)}
                  class="ml-2 text-[10px] font-medium text-fg-mute hover:text-accent"
                  title="Browse {ev.module}@{ev.version} source"
                >code →</a>
              {:else if ev.module}
                {ev.module}
              {:else}—{/if}
            </td>
            <td class="py-1.5 pr-3 text-fg-mute">
              {#if ev.error}
                <span class="text-err">{ev.error}</span>
              {:else}
                {payloadHint(ev)}
              {/if}
            </td>
            <td class="py-1.5 text-right text-fg-dim">
              {ev.duration_ms ? `${ev.duration_ms}ms` : '—'}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
</div>
