<script lang="ts">
  // /requests/[id] — detail page for one procurement request.
  //
  // Renders every field on the Request row plus a pretty-printed
  // preflight_json so reviewers can see why preflight routed the
  // request the way it did.

  import { getRequest } from '$api/client';
  import type { Request } from '$api/types';
  import { page } from '$app/state';
  import RequestStateChip from '$components/RequestStateChip.svelte';

  const id = $derived(parseInt(page.params.id ?? '0', 10));

  let req = $state<Request | null>(null);
  let loading = $state(true);
  let error = $state<string | null>(null);

  async function load() {
    if (!Number.isFinite(id) || id <= 0) {
      error = 'invalid request id';
      loading = false;
      return;
    }
    loading = req === null;
    error = null;
    try {
      req = await getRequest(id);
    } catch (e: unknown) {
      error = e instanceof Error ? e.message : String(e);
      req = null;
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    void id; // capture dep on route change
    void load();
  });

  // Poll until the request reaches a terminal state — keeps the
  // detail view live while a request is moving through preflight /
  // fetching without burning CPU after it lands.
  $effect(() => {
    if (req && (req.state === 'indexed' || req.state === 'denied')) {
      return;
    }
    const t = setInterval(() => void load(), 5000);
    return () => clearInterval(t);
  });

  function fmt(ts: string | undefined): string {
    if (!ts) return '—';
    return new Date(ts).toLocaleString();
  }

  function prettyJSON(v: unknown): string {
    if (v === null || v === undefined) return '';
    try {
      return JSON.stringify(v, null, 2);
    } catch {
      return String(v);
    }
  }
</script>

<svelte:head>
  <title>{req ? `${req.module}@${req.version}` : 'request'} — bzlhub</title>
</svelte:head>

<div class="flex flex-col gap-4">
  <header class="flex flex-col gap-2 pb-3 border-b border-line">
    <a href="/requests" class="text-[11px] text-fg-mute hover:text-accent font-mono">← all requests</a>
    {#if req}
      <div class="flex items-baseline gap-3 flex-wrap">
        <h1 class="font-mono text-2xl text-fg tracking-tight">
          {req.module}<span class="text-fg-dim">@</span>{req.version}
        </h1>
        <RequestStateChip state={req.state} />
        <span class="text-[11px] text-fg-mute font-mono">#{req.id}</span>
      </div>
    {/if}
  </header>

  {#if error}
    <div class="rounded-md border border-err/30 bg-err/10 px-3 py-2 text-sm text-err" role="alert">
      {error}
    </div>
  {:else if loading}
    <div class="flex flex-col gap-1">
      {#each Array.from({ length: 8 }) as _, i (i)}
        <div class="skeleton h-6 w-full"></div>
      {/each}
    </div>
  {:else if req}
    <section class="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-1.5 text-[12px] font-mono">
      <div class="text-fg-dim">submitter</div>
      <div class="text-fg">{req.submitter_email || req.submitter_sub}</div>

      <div class="text-fg-dim">auth method</div>
      <div class="text-fg-mute">{req.auth_method}</div>

      <div class="text-fg-dim">source url</div>
      <div class="text-fg break-all">
        {#if req.source_url}
          <a href={req.source_url} class="hover:text-accent hover:underline" target="_blank" rel="noopener noreferrer">{req.source_url}</a>
        {:else}—{/if}
      </div>

      <div class="text-fg-dim">submitted</div>
      <div class="text-fg-mute" title={req.created_at}>{fmt(req.created_at)}</div>

      <div class="text-fg-dim">state changed</div>
      <div class="text-fg-mute" title={req.state_changed_at}>{fmt(req.state_changed_at)}</div>

      {#if req.submitter_notes}
        <div class="text-fg-dim">notes</div>
        <div class="text-fg">{req.submitter_notes}</div>
      {/if}

      {#if req.fetched_sha}
        <div class="text-fg-dim">fetched (sri)</div>
        <div class="text-fg break-all">{req.fetched_sha}</div>
      {/if}

      {#if req.committed_sha}
        <div class="text-fg-dim">commit</div>
        <div class="text-fg">{req.committed_sha}</div>
      {/if}

      {#if req.denial_reason}
        <div class="text-fg-dim">denial reason</div>
        <div class="text-err">{req.denial_reason}</div>
      {/if}

      {#if req.retry_count > 0}
        <div class="text-fg-dim">retries</div>
        <div class="text-fg-mute">{req.retry_count}</div>
      {/if}
    </section>

    {#if req.preflight_json}
      <section class="flex flex-col gap-2">
        <h2 class="font-mono text-[11px] uppercase tracking-wide text-fg-dim">preflight verdict</h2>
        <pre class="text-[11px] font-mono bg-bg-elev/60 border border-line rounded-md p-3 overflow-x-auto text-fg">{prettyJSON(req.preflight_json)}</pre>
      </section>
    {/if}
  {/if}
</div>
