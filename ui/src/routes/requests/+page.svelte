<script lang="ts">
  // /requests — procurement queue view.
  //
  // Read-only list of every procurement request bzlhub knows about.
  // Bearer-authed clients submit via the CLI / MCP; reviewers
  // approve/deny on /admin/requests when their token has the right
  // group gate. This page is the public-read counterpart.
  //
  // Polling: 15s — matches Plan 72 §C4 spec.

  import { listRequests, getPolicyEffective } from '$api/client';
  import type { Request, RequestState, PolicyEffective } from '$api/types';
  import { page } from '$app/state';
  import { readParam, writeParam, stringList } from '$lib/url-state';
  import RequestStateChip from '$components/RequestStateChip.svelte';
  import RequestRow from '$components/RequestRow.svelte';
  import ShareLink from '$components/ShareLink.svelte';
  import NewRequestModal from '$components/NewRequestModal.svelte';

  // URL-bound state filter chips. Empty list = "all states."
  let stateFilter = $state<RequestState[]>(
    readParam(page.url, 'state', stringList) as RequestState[],
  );
  $effect(() => {
    const url = readParam(page.url, 'state', stringList) as RequestState[];
    const cur = [...stateFilter].sort();
    const next = [...url].sort();
    if (cur.length !== next.length || cur.some((v, i) => v !== next[i])) {
      stateFilter = url;
    }
  });
  $effect(() => {
    writeParam(page.url, 'state', stateFilter, stringList);
  });

  let rows = $state<Request[]>([]);
  let loading = $state(true);
  let error = $state<string | null>(null);

  // Policy snapshot for button visibility — read once on mount,
  // re-poll every minute so a SIGHUP'd policy reaches the UI
  // without a hard refresh. Whether the user can submit drives
  // the "new request" button's render.
  let policy = $state<PolicyEffective | null>(null);
  let showNew = $state(false);

  const canSubmit = $derived(policy?.actions?.submit_request === true);

  async function loadPolicy() {
    try {
      policy = await getPolicyEffective();
    } catch {
      // Policy fetch failure → leave canSubmit false (no button).
    }
  }

  async function load() {
    loading = rows.length === 0;
    error = null;
    try {
      rows = await listRequests({
        states: stateFilter.length > 0 ? stateFilter : undefined,
        limit: 100,
      });
    } catch (e: unknown) {
      error = e instanceof Error ? e.message : String(e);
      rows = [];
    } finally {
      loading = false;
    }
  }

  // Filter changes trigger an immediate reload.
  $effect(() => {
    void stateFilter; // capture dep
    void load();
  });

  // Initial policy probe + 60s refresh — picks up SIGHUP-driven
  // policy changes without a hard reload.
  $effect(() => {
    void loadPolicy();
    const t = setInterval(() => void loadPolicy(), 60000);
    return () => clearInterval(t);
  });

  // 15s polling. setInterval is plenty; SSE would be over-engineering
  // when the queue rarely changes more than a few times a minute.
  $effect(() => {
    const t = setInterval(() => void load(), 15000);
    return () => clearInterval(t);
  });

  // Filter chip toggle — adds/removes a state from the active set.
  function toggleState(s: RequestState) {
    if (stateFilter.includes(s)) {
      stateFilter = stateFilter.filter((x) => x !== s);
    } else {
      stateFilter = [...stateFilter, s];
    }
  }

  function clearFilters() {
    stateFilter = [];
  }

  // Static state-chip ordering — keeps the filter row stable rather
  // than re-shuffling on each render.
  const ALL_STATES: RequestState[] = [
    'pending',
    'preflighting',
    'needs_review',
    'auto_pass',
    'approved',
    'fetching',
    'indexed',
    'denied',
  ];
</script>

<svelte:head>
  <title>requests — bzlhub</title>
</svelte:head>

<div class="flex flex-col gap-4">
  <header class="flex flex-col gap-3 pb-3 border-b border-line">
    <div class="flex items-baseline justify-between gap-3 flex-wrap">
      <div class="flex items-baseline gap-3 flex-wrap">
        <h1 class="font-mono text-2xl text-fg tracking-tight">requests</h1>
        <p class="text-[12px] text-fg-mute">
          procurement queue — submissions, preflight verdicts, reviewer decisions
        </p>
      </div>
      <div class="flex items-center gap-3">
        {#if canSubmit}
          <button
            type="button"
            class="rounded-md border border-accent bg-accent/10 px-3 py-1 text-[11px] font-mono text-accent hover:bg-accent/20 cursor-pointer"
            onclick={() => { showNew = true; }}
          >+ new request</button>
        {/if}
        <ShareLink />
      </div>
    </div>

    <div class="flex items-center gap-2 flex-wrap text-[11px]">
      <span class="text-fg-dim uppercase tracking-wide mr-1">filter:</span>
      {#each ALL_STATES as s (s)}
        {@const active = stateFilter.includes(s)}
        <button
          type="button"
          class="rounded-md border px-2 py-1 font-mono cursor-pointer hover:border-line-strong {active ? 'border-accent bg-bg-elev text-fg' : 'border-line bg-bg-elev/60 text-fg-mute'}"
          onclick={() => toggleState(s)}
        >
          <RequestStateChip state={s} />
        </button>
      {/each}
      {#if stateFilter.length > 0}
        <button
          type="button"
          class="ml-2 text-fg-dim hover:text-fg cursor-pointer"
          onclick={clearFilters}
        >
          clear
        </button>
      {/if}
    </div>
  </header>

  {#if error}
    <div class="rounded-md border border-err/30 bg-err/10 px-3 py-2 text-sm text-err" role="alert">
      {error}
    </div>
  {:else if loading}
    <div class="flex flex-col gap-1">
      {#each Array.from({ length: 6 }) as _, i (i)}
        <div class="skeleton h-9 w-full"></div>
      {/each}
    </div>
  {:else if rows.length === 0}
    <p class="text-center py-16 text-fg-dim font-mono text-sm">
      {stateFilter.length > 0
        ? 'no requests match the active filters'
        : 'no procurement requests yet'}
    </p>
  {:else}
    <p class="text-[11px] text-fg-mute font-mono">{rows.length} request{rows.length === 1 ? '' : 's'}</p>
    <table class="w-full text-[12px] font-mono">
      <thead>
        <tr class="text-[10px] uppercase tracking-wide text-fg-dim border-b border-line/60">
          <th class="text-left font-medium py-1.5 pr-3">when</th>
          <th class="text-left font-medium py-1.5 pr-3">module</th>
          <th class="text-left font-medium py-1.5 pr-3">version</th>
          <th class="text-left font-medium py-1.5 pr-3">submitter</th>
          <th class="text-left font-medium py-1.5 pr-3">state</th>
          <th class="text-left font-medium py-1.5">detail</th>
        </tr>
      </thead>
      <tbody>
        {#each rows as r (r.id)}
          <RequestRow request={r} />
        {/each}
      </tbody>
    </table>
  {/if}
</div>

{#if showNew}
  <NewRequestModal
    onClose={() => { showNew = false; }}
    onSubmitted={() => { void load(); }}
  />
{/if}
