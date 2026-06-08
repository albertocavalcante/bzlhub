<script lang="ts">
  import { paths } from '$lib/api/paths';
  import { page } from '$app/state';
  import {
    getAirgapSurface,
    type ClosureSurfaceResponse,
    type ExternalRef,
  } from '$api/client';

  import { readParam, writeParam, stringField } from '$lib/url-state';
  import ShareLink from '$components/ShareLink.svelte';

  let surface = $state<ClosureSurfaceResponse | null>(null);
  let loading = $state(true);
  let error = $state<string | null>(null);

  // URL-bound filters: `?class=<className>&module=<modName>`. Both
  // chip rows toggle independently; empty string = no filter (key
  // absent from URL). See docs/plans/14-permalinks.md for the
  // bidirectional sync pattern below.
  let classFilter = $state<string>(readParam(page.url, 'class', stringField));
  let moduleFilter = $state<string>(readParam(page.url, 'module', stringField));

  // URL → state (handles back/forward + external navigation).
  $effect(() => {
    const c = readParam(page.url, 'class', stringField);
    if (c !== classFilter) classFilter = c;
  });
  $effect(() => {
    const m = readParam(page.url, 'module', stringField);
    if (m !== moduleFilter) moduleFilter = m;
  });

  // State → URL (chip toggles push to history).
  $effect(() => {
    writeParam(page.url, 'class', classFilter, stringField);
  });
  $effect(() => {
    writeParam(page.url, 'module', moduleFilter, stringField);
  });

  $effect(() => {
    const name = page.params.name;
    const version = page.params.version;
    if (!name || !version) return;
    const ctl = new AbortController();
    loading = true;
    error = null;
    getAirgapSurface(name, version, ctl.signal)
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

  // Same ecosystem-class palette as the per-module External tab.
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

  // Class chips, count-desc sorted.
  const classChips = $derived.by(() => {
    const counts = surface?.class_counts ?? {};
    return Object.entries(counts)
      .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
      .map(([cls, n]) => ({ cls, n }));
  });

  // Filtered ref list — by class chip, by module chip, or both.
  const visibleRefs = $derived.by<ExternalRef[]>(() => {
    const refs = surface?.refs ?? [];
    return refs.filter((r) => {
      if (classFilter && r.class !== classFilter) return false;
      // moduleFilter (?module=<bare-name>) filters by the
      // server-emitted source_module ("name@version" string).
      // First-seen-wins under closure dedupe — see Go's AirgapSurface.
      if (moduleFilter) {
        if (!r.source_module) return false;
        const bare = r.source_module.split('@', 1)[0];
        if (bare !== moduleFilter && r.source_module !== moduleFilter) return false;
      }
      return true;
    });
  });

  function setClass(c: string) {
    classFilter = classFilter === c ? '' : c;
  }
  function setModule(m: string) {
    moduleFilter = moduleFilter === m ? '' : m;
  }

  function shortURL(u: string, max = 90): string {
    if (u.length <= max) return u;
    return `${u.slice(0, 50)}…${u.slice(-30)}`;
  }

  const downloaderHref = $derived(
    `${paths.airgap.downloaderConfig(page.params.name ?? '', page.params.version ?? '')}?recursive=true`,
  );

  const moduleMirrorsHref = $derived(
    `${paths.airgap.moduleMirrors(page.params.name ?? '', page.params.version ?? '')}`,
  );

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
  <header class="flex flex-wrap items-start justify-between gap-4">
    <div>
      <h1 class="text-[20px] font-medium">Airgap surface — closure-wide</h1>
      <p class="text-[13px] text-fg-mute mt-1 max-w-[70ch]">
        Every URL the <em>entire transitive</em> <code class="text-fg">bazel_deps</code> closure
        of <code class="text-fg">{surface?.root ?? page.params.name + '@' + page.params.version}</code>
        would fetch, unioned across bzlhub's indexed modules. This is what an
        air-gapped mirror needs to satisfy. Per-module ref counts let you see
        which dependency contributes which classes.
      </p>
    </div>
    {#if surface && surface.refs.length > 0}
      <div class="shrink-0 flex flex-wrap items-center gap-2">
        <a
          href={downloaderHref}
          class="rounded border border-accent/60 text-accent px-3 py-1.5 text-[12px] hover:bg-accent/10 transition-colors"
          title="Bazel --downloader_config (≥9) / --experimental_downloader_config (≤8) rewrite map for this closure. Covers every URL: repo rules, extensions, and registry sources."
          aria-label="Download Bazel downloader-config artifact (covers every URL — repo rules, extensions, and registry sources)"
          download
        >
          Downloader config
        </a>
        <a
          href={moduleMirrorsHref}
          class="rounded border border-accent/60 text-accent px-3 py-1.5 text-[12px] hover:bg-accent/10 transition-colors"
          title="Bazel --module_mirrors (≥8.4) .bazelrc snippet — registry slice only. Pair with the downloader config; the downloader config is the superset."
          aria-label="Download Bazel module-mirrors .bazelrc snippet (registry slice only, Bazel ≥ 8.4)"
          download
        >
          Module mirrors
        </a>
        <ShareLink />
      </div>
    {/if}
  </header>

  {#if loading}
    <div class="text-fg-mute text-[13px]">Loading closure…</div>
  {:else if error}
    <div class="rounded border border-line bg-bg-elev/40 px-4 py-3 text-[13px] text-fg-mute">
      {error}
    </div>
  {:else if !surface}
    <div class="rounded border border-line bg-bg-elev/40 px-4 py-3 text-[13px] text-fg-mute">
      No data.
    </div>
  {:else}
    <section class="flex flex-wrap gap-4 text-[12px] font-mono">
      <div class="rounded border border-line bg-bg-elev/40 px-3 py-2">
        <div class="text-fg-mute text-[10px] uppercase tracking-wide">modules</div>
        <div class="text-fg text-[14px]">{surface.modules.length}</div>
      </div>
      <div class="rounded border border-line bg-bg-elev/40 px-3 py-2">
        <div class="text-fg-mute text-[10px] uppercase tracking-wide">total URLs</div>
        <div class="text-fg text-[14px]">{surface.refs.length}</div>
      </div>
      <div class="rounded border border-line bg-bg-elev/40 px-3 py-2">
        <div class="text-fg-mute text-[10px] uppercase tracking-wide">classes</div>
        <div class="text-fg text-[14px]">{classChips.length}</div>
      </div>
      {#if surface.missing_modules && surface.missing_modules.length > 0}
        <div
          class="rounded border px-3 py-2"
          style:--c="oklch(0.74 0.18 80)"
          style:background="color-mix(in oklab, oklch(0.74 0.18 80) 12%, transparent)"
          style:border-color="color-mix(in oklab, oklch(0.74 0.18 80) 40%, transparent)"
        >
          <div class="text-[10px] uppercase tracking-wide" style:color="oklch(0.74 0.18 80)">
            unindexed closure refs
          </div>
          <div class="text-fg text-[14px]">{surface.missing_modules.length}</div>
        </div>
      {/if}
      {#if surface.max_depth_reached}
        <div
          class="rounded border px-3 py-2 text-[10px]"
          style:--c="oklch(0.6 0.18 25)"
          style:color="oklch(0.6 0.18 25)"
        >
          ⚠ closure walk hit max depth — counts may underrepresent
        </div>
      {/if}
    </section>

    {#if classChips.length > 0}
      <div class="flex flex-wrap gap-1.5">
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
            onclick={() => setClass(cls)}
          >
            <span class="inline-block w-1.5 h-1.5 rounded-full mr-1" style:background={classColor(cls)}></span>
            {cls}
            <span class="text-fg-mute"> · {n}</span>
          </button>
        {/each}
      </div>
    {/if}

    <section class="rounded border border-line overflow-hidden">
      <header class="bg-bg-elev/40 px-3 py-2 text-[11px] uppercase tracking-wide text-fg-mute">
        Closure nodes — {surface.modules.length}
      </header>
      <table class="w-full text-[12px] font-mono">
        <tbody>
          {#each surface.modules as m (m.module + '@' + m.version)}
            <tr
              class="border-t border-line/50 hover:bg-bg-elev/30"
              class:bg-accent={moduleFilter === m.module}
              class:bg-opacity-10={moduleFilter === m.module}
            >
              <td class="px-3 py-2">
                <a
                  href={`/modules/${encodeURIComponent(m.module)}/${encodeURIComponent(m.version)}`}
                  class="text-accent hover:underline"
                >
                  {m.module}@{m.version}
                </a>
                {#if m.external}
                  <span class="ml-2 text-[10px] text-fg-mute italic">(not indexed)</span>
                {/if}
                <button
                  type="button"
                  class="ml-2 text-[10px] text-fg-mute hover:text-accent transition-colors"
                  title={moduleFilter === m.module ? 'clear module filter' : 'filter refs to those contributed by this module'}
                  aria-label={moduleFilter === m.module ? 'clear module filter' : `filter to ${m.module}`}
                  onclick={() => setModule(m.module)}
                >
                  {moduleFilter === m.module ? 'clear filter' : 'filter to this'}
                </button>
              </td>
              <td class="px-3 py-2 text-right text-fg-mute w-24">
                {m.ref_count} URL{m.ref_count === 1 ? '' : 's'}
              </td>
              <td class="px-3 py-2 w-1/2">
                {#if m.class_counts}
                  <div class="flex flex-wrap gap-1">
                    {#each Object.entries(m.class_counts).sort((a, b) => b[1] - a[1]) as [cls, n] (cls)}
                      <span class="inline-flex items-center gap-1 text-[10px] text-fg-mute">
                        <span class="inline-block w-1 h-1 rounded-full" style:background={classColor(cls)}></span>
                        {cls}·{n}
                      </span>
                    {/each}
                  </div>
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </section>

    <section class="rounded border border-line overflow-hidden">
      <header class="bg-bg-elev/40 px-3 py-2 text-[11px] uppercase tracking-wide text-fg-mute">
        Closure URLs — {visibleRefs.length}{classFilter ? ` of ${surface.refs.length}` : ''}
      </header>
      <table class="w-full text-[12px] font-mono">
        <thead class="bg-bg-elev/30 text-fg-mute text-[10px] uppercase tracking-wide">
          <tr>
            <th class="text-left px-3 py-1.5 font-normal">Class</th>
            <th class="text-left px-3 py-1.5 font-normal">URL</th>
            <th class="text-left px-3 py-1.5 font-normal">Mutability</th>
            <th class="text-left px-3 py-1.5 font-normal">Platform</th>
          </tr>
        </thead>
        <tbody>
          {#each visibleRefs as r (r.url + ':' + (r.platform ?? '') + ':' + (r.file ?? ''))}
            {@const mb = mutabilityBadge(r)}
            <tr class="border-t border-line/50 hover:bg-bg-elev/30">
              <td class="px-3 py-2 whitespace-nowrap">
                <span class="inline-flex items-center gap-1">
                  <span class="inline-block w-1.5 h-1.5 rounded-full" style:background={classColor(r.class)}></span>
                  <span class="text-fg">{r.class}</span>
                </span>
              </td>
              <td class="px-3 py-2 break-all">
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
                  <span class="block text-fg-mute text-[10px] mt-0.5">sha256:{r.sha256.slice(0, 12)}…</span>
                {/if}
              </td>
              <td class="px-3 py-2 whitespace-nowrap">
                <span
                  class="inline-block px-1.5 py-0.5 rounded text-[10px]"
                  style:background="color-mix(in oklab, {mb.color} 18%, transparent)"
                  style:color={mb.color}
                >
                  {mb.label}
                </span>
              </td>
              <td class="px-3 py-2 text-fg-mute whitespace-nowrap">{r.platform || 'any'}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </section>
  {/if}
</div>
