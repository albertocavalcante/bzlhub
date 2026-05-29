<script lang="ts">
  // Cross-corpus consumer view (Plan 07).
  //
  // /modules/<m>/<v>/consumers/<name> — every call site of the
  // named rule / provider / macro / repo_rule / module_extension
  // across canopy's indexed corpus. Backed by Service.LookupConsumers
  // (which resolves the user-facing name to a SCIP symbol via the
  // defining module's ModuleReport and walks every blob).
  //
  // Self-filtering: the defining module's own occurrences are
  // hidden by default — operators investigating "who uses my rule?"
  // don't want their own examples drowning the list. The
  // include-self toggle is URL-bound (Plan 14) so a shared link
  // preserves the view.

  import { page } from '$app/state';
  import { getConsumers, type ConsumersResult } from '$api/client';
  import { parseScipSymbol } from '$api/scip';
  import { readParam, writeParam, boolField, stringList } from '$lib/url-state';
  import ShareLink from '$components/ShareLink.svelte';

  let result = $state<ConsumersResult | null>(null);
  let loading = $state(true);
  let error = $state<string | null>(null);

  // ?include_self=true → keep the defining module in results.
  let includeSelf = $state<boolean>(readParam(page.url, 'include_self', boolField));
  // ?expand=<m@v,m@v,...> — comma-list of "<module>@<version>" keys
  // whose call-site list is open. URL-bound so a shared link
  // preserves which sections the recipient sees open.
  let expanded = $state<Set<string>>(
    new Set(readParam(page.url, 'expand', stringList)),
  );

  // URL → state
  $effect(() => {
    const v = readParam(page.url, 'include_self', boolField);
    if (v !== includeSelf) includeSelf = v;
  });
  $effect(() => {
    const list = readParam(page.url, 'expand', stringList);
    const cur = Array.from(expanded).sort();
    const next = [...list].sort();
    if (cur.length !== next.length || cur.some((v, i) => v !== next[i])) {
      expanded = new Set(list);
    }
  });
  $effect(() => {
    writeParam(page.url, 'include_self', includeSelf, boolField);
  });
  $effect(() => {
    writeParam(page.url, 'expand', Array.from(expanded), stringList);
  });

  // Initial load + re-fetch when include_self changes.
  $effect(() => {
    const m = page.params.name;
    const v = page.params.version;
    const sym = page.params.symbol;
    if (!m || !v || !sym) return;
    const ctl = new AbortController();
    loading = true;
    error = null;
    getConsumers(m, v, sym, { includeSelf }, ctl.signal)
      .then((r) => {
        if (ctl.signal.aborted) return;
        result = r;
        loading = false;
      })
      .catch((e) => {
        if (ctl.signal.aborted) return;
        error = String(e instanceof Error ? e.message : e);
        loading = false;
      });
    return () => ctl.abort();
  });

  function toggleExpanded(m: string) {
    // Svelte 5's reactivity treats Sets by identity; re-assign on mutate.
    const next = new Set(expanded);
    if (next.has(m)) next.delete(m);
    else next.add(m);
    expanded = next;
  }

  function shortFile(f: string, max = 70): string {
    if (f.length <= max) return f;
    return `${f.slice(0, 30)}…${f.slice(-30)}`;
  }

  // Humanized symbol parts: prefer the API response (the canonical
  // source) and fall back to URL-parsed bits during the initial load
  // so the header renders something readable even before getConsumers
  // returns. Both can be partially populated; render defensively.
  const fallback = $derived(parseScipSymbol(page.params.symbol));
  const display = $derived({
    name: result?.name ?? fallback?.name ?? page.params.symbol ?? '',
    file: result?.file ?? fallback?.file ?? '',
    kind: result?.kind ?? '',
    module: result?.module ?? fallback?.module ?? page.params.name ?? '',
    version: result?.version ?? fallback?.version ?? page.params.version ?? '',
  });
</script>

<svelte:head>
  <title>Consumers of {display.name} — canopy</title>
</svelte:head>

<div class="flex flex-col gap-4">
  <header class="flex items-baseline justify-between gap-3 flex-wrap pb-3 border-b border-line">
    <div class="flex flex-col gap-1">
      <div class="text-[11px] text-fg-mute font-mono">
        <a href={`/modules/${encodeURIComponent(page.params.name ?? '')}/${encodeURIComponent(page.params.version ?? '')}`} class="hover:text-accent">
          {display.module}@{display.version}
        </a>
        <span class="mx-1 text-fg-dim">/</span>
        consumers
      </div>
      <h1 class="text-[20px] font-medium font-mono text-fg">{display.name}</h1>
      {#if display.kind || display.file}
        <p class="text-[12px] text-fg-mute">
          {#if display.kind}{display.kind}{/if}
          {#if display.kind && display.file} defined at {/if}
          {#if display.file}<code class="text-fg">{display.file}</code>{/if}
        </p>
      {/if}
    </div>
    <div class="flex items-center gap-2">
      <label class="text-[12px] text-fg-mute flex items-center gap-1.5 cursor-pointer">
        <input type="checkbox" bind:checked={includeSelf} class="accent-accent" />
        include self
      </label>
      <ShareLink />
    </div>
  </header>

  {#if loading}
    <div class="text-fg-mute text-[13px]">Loading consumers…</div>
  {:else if error}
    <div class="rounded border border-line bg-bg-elev/40 px-4 py-3 text-[13px] text-fg-mute">
      {error}
    </div>
  {:else if result}
    <section class="flex flex-wrap gap-4 text-[12px] font-mono">
      <div class="rounded border border-line bg-bg-elev/40 px-3 py-2">
        <div class="text-fg-mute text-[10px] uppercase tracking-wide">call sites</div>
        <div class="text-fg text-[14px]">{result.total_call_sites}</div>
      </div>
      <div class="rounded border border-line bg-bg-elev/40 px-3 py-2">
        <div class="text-fg-mute text-[10px] uppercase tracking-wide">consumer modules</div>
        <div class="text-fg text-[14px]">{result.consumer_count}</div>
      </div>
      {#if result.skipped > 0}
        <div
          class="rounded border px-3 py-2"
          style:border-color="color-mix(in oklab, oklch(0.74 0.18 80) 40%, transparent)"
          style:background="color-mix(in oklab, oklch(0.74 0.18 80) 12%, transparent)"
        >
          <div class="text-[10px] uppercase tracking-wide" style:color="oklch(0.74 0.18 80)">
            blobs skipped
          </div>
          <div class="text-fg text-[14px]">{result.skipped}</div>
        </div>
      {/if}
    </section>

    {#if result.consumers.length === 0}
      <div class="rounded border border-line bg-bg-elev/40 px-4 py-3 text-[13px] text-fg-mute">
        {#if !includeSelf}
          No call sites in canopy's indexed corpus outside the defining module.
          Toggle "include self" to also see {result.module}'s own references.
        {:else}
          No call sites in canopy's indexed corpus. Ingest more modules to populate cross-corpus consumer data.
        {/if}
      </div>
    {:else}
      <ul class="flex flex-col gap-1">
        {#each result.consumers as c (c.module + '@' + c.version)}
          {@const isOpen = expanded.has(c.module + '@' + c.version)}
          <li class="rounded border border-line">
            <button
              type="button"
              class="w-full px-3 py-2 flex items-baseline justify-between gap-3 text-left hover:bg-bg-elev/30 transition-colors"
              onclick={() => toggleExpanded(c.module + '@' + c.version)}
              aria-expanded={isOpen}
            >
              <span class="font-mono text-[13px]">
                <span class="text-fg-mute mr-1">{isOpen ? '▾' : '▸'}</span>
                <a href={c.module_href} class="text-accent hover:underline" onclick={(e) => e.stopPropagation()}>
                  {c.module}@{c.version}
                </a>
              </span>
              <span class="text-[11px] text-fg-mute font-mono">
                {c.call_sites.length} site{c.call_sites.length === 1 ? '' : 's'}
              </span>
            </button>
            {#if isOpen}
              <ul class="border-t border-line/50 px-3 py-2 flex flex-col gap-1 text-[12px] font-mono">
                {#each c.call_sites as s, i (s.file + ':' + s.line + ':' + i)}
                  <li>
                    <a href={s.href} class="text-fg-mute hover:text-accent transition-colors" title={s.file}>
                      <span class="text-fg">{shortFile(s.file)}</span>
                      <span class="text-fg-dim">:</span>
                      <span class="text-fg">{s.line}</span>
                    </a>
                  </li>
                {/each}
              </ul>
            {/if}
          </li>
        {/each}
      </ul>
    {/if}
  {/if}
</div>
