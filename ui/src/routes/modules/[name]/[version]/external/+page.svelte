<script lang="ts">
  import { page } from '$app/state';
  import {
    getExternalSurface,
    type ExternalRef,
    type ExternalSurfaceResponse,
  } from '$api/client';
  import { readParam, writeParam, stringField } from '$lib/url-state';
  import ShareLink from '$components/ShareLink.svelte';

  let surface = $state<ExternalSurfaceResponse | null>(null);
  let loading = $state(true);
  let error = $state<string | null>(null);

  // URL-bound filter: `?class=<className>` selects a single class chip.
  // Empty string = no filter (key absent from URL). The two effects
  // below maintain bidirectional sync between $state and the URL —
  // see docs/plans/14-permalinks.md for the binding pattern.
  let classFilter = $state<string>(readParam(page.url, 'class', stringField));

  // URL → state: browser back/forward + external navigation updates
  // the URL; mirror the change into local state. Guard against
  // unnecessary writes so this doesn't form a loop with the
  // state-to-URL effect below.
  $effect(() => {
    const fromUrl = readParam(page.url, 'class', stringField);
    if (fromUrl !== classFilter) classFilter = fromUrl;
  });

  // State → URL: user-driven chip toggles update local state; write
  // back to the URL with push-style history (back button undoes the
  // chip). writeParam no-ops when the URL is already in sync.
  $effect(() => {
    writeParam(page.url, 'class', classFilter, stringField);
  });

  $effect(() => {
    const name = page.params.name;
    const version = page.params.version;
    if (!name || !version) return;
    const ctl = new AbortController();
    loading = true;
    error = null;
    getExternalSurface(name, version, ctl.signal)
      .then((r) => {
        if (ctl.signal.aborted) return;
        surface = r;
        loading = false;
      })
      .catch((e) => {
        if (ctl.signal.aborted) return;
        error = String(e);
        loading = false;
      });
    return () => ctl.abort();
  });

  // Class-color mapping: ecosystem class → tag color. Keeps the chip
  // row scannable. Unknown classes fall back to neutral grey.
  const CLASS_COLORS: Record<string, string> = {
    bcr: 'oklch(0.72 0.16 200)',
    maven: 'oklch(0.65 0.18 30)',
    'pypi-canonical': 'oklch(0.72 0.16 250)',
    'pypi-extra': 'oklch(0.65 0.14 250)',
    npm: 'oklch(0.65 0.18 0)',
    'go-proxy': 'oklch(0.72 0.16 200)',
    'github-release': 'oklch(0.72 0.16 160)',
    'github-archive': 'oklch(0.7 0.16 90)',
    'github-other': 'oklch(0.65 0.10 160)',
    'gitlab-release': 'oklch(0.72 0.16 60)',
    'gitlab-archive': 'oklch(0.7 0.16 50)',
    'gitlab-other': 'oklch(0.65 0.10 60)',
    oci: 'oklch(0.7 0.16 280)',
    'cloud-storage': 'oklch(0.7 0.14 220)',
    'vendor-http': 'oklch(0.7 0.10 130)',
    unknown: 'oklch(0.65 0.04 270)',
  };
  function classColor(c: string): string {
    return CLASS_COLORS[c] ?? CLASS_COLORS.unknown;
  }

  // The class chip palette is ordered by count desc so the most-used
  // ecosystems appear first; ties broken alphabetically.
  const classChips = $derived.by(() => {
    const counts = surface?.class_counts ?? {};
    return Object.entries(counts)
      .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
      .map(([cls, n]) => ({ cls, n }));
  });

  // Filtered ref list based on the active chip.
  const visibleRefs = $derived.by<ExternalRef[]>(() => {
    const refs = surface?.refs ?? [];
    if (!classFilter) return refs;
    return refs.filter((r) => r.class === classFilter);
  });

  function setFilter(c: string) {
    classFilter = classFilter === c ? '' : c;
  }

  // For display: shorten very long URLs in the middle so the column
  // doesn't overflow on narrow screens. The full URL is in the title
  // attribute + the clickable link.
  function shortURL(u: string, max = 90): string {
    if (u.length <= max) return u;
    const head = u.slice(0, 50);
    const tail = u.slice(-30);
    return `${head}…${tail}`;
  }

  // Confidence badge — analyzer's signal about the URL itself,
  // orthogonal to the Mutability column. "resolved" omitted (no
  // badge — the default state shouldn't visually compete).
  function confidenceBadge(r: ExternalRef): { label: string; color: string; title: string } | null {
    switch (r.confidence) {
      case 'tainted':
        return {
          label: 'verify',
          color: 'oklch(0.6 0.18 25)',
          title: "Analyzer's eval touched opaque state (ctx.execute, unresolved external load). Verify the URL at runtime before trusting.",
        };
      case 'platform-specific':
        return {
          label: 'platform',
          color: 'oklch(0.72 0.16 250)',
          title: `URL captured per-fork — applies only to the ${r.platform} platform.`,
        };
      default:
        return null;
    }
  }

  // Mutability badge label/color — the more dangerous the mutability,
  // the warmer the color (mutable-host=amber, immutable=green,
  // unknown=grey, taint=red regardless).
  function mutabilityBadge(r: ExternalRef): { label: string; color: string } {
    if (r.tainted) return { label: 'tainted', color: 'oklch(0.6 0.18 25)' };
    switch (r.mutability) {
      case 'immutable':
        return { label: 'immutable', color: 'oklch(0.7 0.16 145)' };
      case 'mutable-host':
        return { label: 'mutable', color: 'oklch(0.74 0.18 80)' };
      case 'unknown':
        return { label: 'unknown', color: 'oklch(0.65 0.04 270)' };
      default:
        return { label: r.mutability || '—', color: 'oklch(0.65 0.04 270)' };
    }
  }
</script>

<div class="flex flex-col gap-4">
  <header class="flex items-baseline justify-between">
    <div>
      <h1 class="text-[20px] font-medium">External URL surface</h1>
      <p class="text-[13px] text-fg-mute mt-1 max-w-[60ch]">
        Every URL this module's <code class="text-fg">repository_rule</code> +
        <code class="text-fg">module_extension</code> implementations would fetch,
        extracted by static analysis. Use this when bringing the module to an
        air-gapped environment — point your downloader at a mirror for each class
        below.
      </p>
    </div>
    <div class="flex items-center gap-3">
      {#if surface && !loading}
        <div class="text-[12px] text-fg-mute font-mono">
          {surface.refs.length} URL{surface.refs.length === 1 ? '' : 's'}
        </div>
      {/if}
      <ShareLink />
    </div>
  </header>

  {#if loading}
    <div class="text-fg-mute text-[13px]">Loading…</div>
  {:else if error}
    <div class="rounded border border-line bg-bg-elev/40 px-4 py-3 text-[13px] text-fg-mute">
      {error}
    </div>
  {:else if !surface || surface.refs.length === 0}
    <div class="rounded border border-line bg-bg-elev/40 px-4 py-3 text-[13px] text-fg-mute">
      No external URLs captured for this module yet. Either the module's
      analysis is still pending, or its <code>.bzl</code> files don't declare
      any <code>repository_rule</code> with a literal download URL the
      analyzer could resolve.
    </div>
  {:else}
    {#if classChips.length > 0}
      <div class="flex flex-wrap gap-1.5" data-testid="external-class-chips">
        <button
          type="button"
          class="px-2 py-0.5 rounded border text-[11px] font-mono transition-colors {classFilter === '' ? 'border-accent text-accent' : 'border-line text-fg-mute hover:text-fg'}"
          onclick={() => (classFilter = '')}
        >
          all
          <span class="text-fg-mute"> · {surface.refs.length}</span>
        </button>
        {#each classChips as { cls, n } (cls)}
          <button
            type="button"
            class="px-2 py-0.5 rounded border text-[11px] font-mono transition-colors {classFilter === cls ? 'border-accent text-accent' : 'border-line text-fg-mute hover:text-fg'}"
            onclick={() => setFilter(cls)}
            style:--chip={classColor(cls)}
          >
            <span class="inline-block w-1.5 h-1.5 rounded-full mr-1" style:background={classColor(cls)}></span>
            {cls}
            <span class="text-fg-mute"> · {n}</span>
          </button>
        {/each}
      </div>
    {/if}

    <div class="rounded border border-line overflow-hidden">
      <table class="w-full text-[12px] font-mono">
        <thead class="bg-bg-elev/40 text-fg-mute text-[11px] uppercase tracking-wide">
          <tr>
            <th class="text-left px-3 py-2 font-normal">Class</th>
            <th class="text-left px-3 py-2 font-normal">URL</th>
            <th class="text-left px-3 py-2 font-normal">Mutability</th>
            <th class="text-left px-3 py-2 font-normal">Rule</th>
            <th class="text-left px-3 py-2 font-normal">Platform</th>
            <th class="text-left px-3 py-2 font-normal">File</th>
          </tr>
        </thead>
        <tbody>
          {#each visibleRefs as r (r.url + ':' + (r.platform ?? '') + ':' + (r.file ?? ''))}
            {@const mb = mutabilityBadge(r)}
            <tr class="border-t border-line/50 hover:bg-bg-elev/30">
              <td class="px-3 py-2 align-top whitespace-nowrap">
                <span class="inline-flex items-center gap-1">
                  <span class="inline-block w-1.5 h-1.5 rounded-full" style:background={classColor(r.class)}></span>
                  <span class="text-fg">{r.class}</span>
                </span>
              </td>
              <td class="px-3 py-2 align-top break-all">
                <a
                  href={r.url}
                  target="_blank"
                  rel="noopener noreferrer"
                  class="text-accent hover:underline"
                  title={r.url}
                >
                  {shortURL(r.url)}
                </a>
                {#if r.sha256}
                  <span class="block text-fg-mute text-[10px] mt-0.5" title="sha256">sha256:{r.sha256.slice(0, 12)}…</span>
                {:else if r.integrity}
                  <span class="block text-fg-mute text-[10px] mt-0.5" title="integrity">{r.integrity}</span>
                {/if}
              </td>
              <td class="px-3 py-2 align-top whitespace-nowrap">
                <span
                  class="inline-block px-1.5 py-0.5 rounded text-[10px]"
                  style:background="color-mix(in oklab, {mb.color} 18%, transparent)"
                  style:color={mb.color}
                >
                  {mb.label}
                </span>
                {#if confidenceBadge(r)}
                  {@const cb = confidenceBadge(r)}
                  {#if cb}
                    <span
                      class="inline-block px-1.5 py-0.5 rounded text-[10px] ml-1"
                      style:background="color-mix(in oklab, {cb.color} 18%, transparent)"
                      style:color={cb.color}
                      title={cb.title}
                    >
                      {cb.label}
                    </span>
                  {/if}
                {/if}
              </td>
              <td class="px-3 py-2 align-top text-fg-mute">
                {r.rule_name || '—'}
              </td>
              <td class="px-3 py-2 align-top text-fg-mute">
                {r.platform || 'any'}
              </td>
              <td class="px-3 py-2 align-top text-fg-mute">
                {r.file || '—'}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>

    {#if surface.fork_errors && surface.fork_errors.length > 0}
      <details class="rounded border border-line bg-bg-elev/40 px-4 py-3 text-[12px]">
        <summary class="cursor-pointer text-fg-mute">
          {surface.fork_errors.length} fork error{surface.fork_errors.length === 1 ? '' : 's'} during analysis
        </summary>
        <ul class="mt-2 space-y-1 font-mono text-[11px]">
          {#each surface.fork_errors as fe (fe.platform + ':' + fe.message)}
            <li class="flex gap-2">
              <span class="text-fg-mute shrink-0">{fe.platform}</span>
              <span class="text-fg break-all">{fe.message}</span>
            </li>
          {/each}
        </ul>
      </details>
    {/if}

    {#if surface.corpus_usages && surface.corpus_usages.length > 0}
      <section class="rounded border border-line overflow-hidden">
        <header class="bg-bg-elev/40 px-3 py-2 text-[11px] uppercase tracking-wide text-fg-mute">
          Consumer-corpus tag values — {surface.corpus_usages.length} extension{surface.corpus_usages.length === 1 ? '' : 's'} used by other indexed modules
        </header>
        <div class="text-[12px] text-fg-mute px-3 py-2 border-b border-line/50">
          Tag instances that consumers of this ruleset pin on its
          <code class="text-fg">module_extension</code>s. Use this when
          deciding which versions/platforms to mirror — these are what
          your canopy-indexed ecosystem actually fetches, not just the
          ruleset's declared defaults.
        </div>
        {#each surface.corpus_usages as cu (cu.extension_file + '%' + cu.extension_name)}
          <div class="border-b border-line/50 last:border-b-0">
            <header class="px-3 py-2 bg-bg-elev/20 font-mono text-[12px]">
              <code class="text-fg">{cu.extension_file}%{cu.extension_name}</code>
              <span class="text-fg-mute ml-2">{cu.consumers.length} consumer call{cu.consumers.length === 1 ? '' : 's'}</span>
            </header>
            <table class="w-full text-[12px] font-mono">
              <thead class="bg-bg-elev/10 text-fg-mute text-[10px] uppercase tracking-wide">
                <tr>
                  <th class="text-left px-3 py-1.5 font-normal">Consumer</th>
                  <th class="text-left px-3 py-1.5 font-normal">Tag</th>
                  <th class="text-left px-3 py-1.5 font-normal">Attrs</th>
                </tr>
              </thead>
              <tbody>
                {#each cu.consumers as c, idx (c.consumer_module + '@' + c.consumer_version + ':' + idx)}
                  <tr class="border-t border-line/30 hover:bg-bg-elev/30">
                    <td class="px-3 py-1.5 align-top whitespace-nowrap">
                      <a
                        href={`/modules/${encodeURIComponent(c.consumer_module)}/${encodeURIComponent(c.consumer_version)}`}
                        class="text-accent hover:underline"
                      >
                        {c.consumer_module}@{c.consumer_version}
                      </a>
                      {#if c.dev_dependency}
                        <span class="ml-1 text-[10px] text-fg-mute italic" title="dev_dependency=True">dev</span>
                      {/if}
                      {#if c.isolate}
                        <span class="ml-1 text-[10px] text-fg-mute italic" title="isolate=True">iso</span>
                      {/if}
                    </td>
                    <td class="px-3 py-1.5 align-top text-fg whitespace-nowrap">.{c.tag_name}</td>
                    <td class="px-3 py-1.5 align-top text-fg-mute break-all">
                      {#if c.tag_attrs && Object.keys(c.tag_attrs).length > 0}
                        {#each Object.entries(c.tag_attrs) as [k, v], j (k + j)}
                          <span class="inline-block mr-2">
                            <span class="text-fg-mute">{k}=</span><span class="text-fg">{JSON.stringify(v)}</span>
                          </span>
                        {/each}
                      {:else}
                        —
                      {/if}
                    </td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/each}
      </section>
    {/if}
  {/if}
</div>
