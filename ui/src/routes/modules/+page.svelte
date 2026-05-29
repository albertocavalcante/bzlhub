<script lang="ts">
  // /modules — corpus browse page.
  //
  // The discoverability surface canopy was missing: land here, see
  // every indexed module in one grid, click through. Pairs with
  // search (which requires a query) as the "explore" half of the
  // discovery story.

  import { listModules, type ModuleSummary } from '$api/client';
  import { codeNavRootHref, moduleHref, moduleVersionHref } from '$lib/links';
  import { relativeTime } from '$lib/time';
  import { page } from '$app/state';
  import { readParam, writeParam, stringField, boolField } from '$lib/url-state';
  import ShareLink from '$components/ShareLink.svelte';
  import DriftChip from '$components/DriftChip.svelte';

  let modules = $state<ModuleSummary[] | null>(null);
  let loading = $state(true);
  let error = $state<string | null>(null);

  // URL-bound view state (Plan 14):
  //   ?q=<text>     — name-substring filter (replaceState — typing)
  //   ?source=true  — hide modules with no source index
  //   ?sort=<key>   — sort selector (default 'usage' omitted)
  let filter = $state<string>(readParam(page.url, 'q', stringField));
  let onlyWithSource = $state<boolean>(readParam(page.url, 'source', boolField));
  type SortKey = 'name' | 'usage' | 'maintainers' | 'versions';
  // sortField codec is overkill here (no asc/desc, just a fixed
  // enum), so use stringField with 'usage' as the implicit default
  // (omitted from URL). Cast at read since stringField doesn't
  // constrain the SortKey union.
  function readSort(): SortKey {
    const raw = readParam(page.url, 'sort', stringField);
    return raw === 'name' || raw === 'maintainers' || raw === 'versions' ? raw : 'usage';
  }
  let sortKey = $state<SortKey>(readSort());

  // URL → state
  $effect(() => {
    const q = readParam(page.url, 'q', stringField);
    if (q !== filter) filter = q;
  });
  $effect(() => {
    const s = readParam(page.url, 'source', boolField);
    if (s !== onlyWithSource) onlyWithSource = s;
  });
  $effect(() => {
    const k = readSort();
    if (k !== sortKey) sortKey = k;
  });

  // State → URL. Filter text uses replace-style history so typing
  // doesn't flood the back stack; toggles + sort use push so the
  // back button undoes committed choices.
  $effect(() => {
    writeParam(page.url, 'q', filter, stringField, { history: 'replace' });
  });
  $effect(() => {
    writeParam(page.url, 'source', onlyWithSource, boolField);
  });
  $effect(() => {
    // 'usage' is the default — omit from URL by setting empty string,
    // any other value lands as ?sort=<key>.
    writeParam(page.url, 'sort', sortKey === 'usage' ? '' : sortKey, stringField);
  });

  $effect(() => {
    const ctl = new AbortController();
    loading = true;
    error = null;
    listModules(ctl.signal)
      .then((r) => {
        if (!ctl.signal.aborted) modules = r.modules;
      })
      .catch((e) => {
        if (!ctl.signal.aborted) error = e instanceof Error ? e.message : String(e);
      })
      .finally(() => {
        if (!ctl.signal.aborted) loading = false;
      });
    return () => ctl.abort();
  });

  const filtered = $derived<ModuleSummary[]>(
    (modules ?? [])
      .filter((m) => m.name.includes(filter.toLowerCase()))
      .filter((m) => !onlyWithSource || m.has_source_index)
      .slice()
      .sort((a, b) => compareBy(a, b, sortKey)),
  );

  // Sort comparator. Counts sort DESC (popularity), name sorts ASC
  // (alphabetical). Ties on count fall through to name so the
  // ordering stays deterministic — otherwise modules with the same
  // usage count would shuffle on every render.
  function compareBy(a: ModuleSummary, b: ModuleSummary, key: SortKey): number {
    switch (key) {
      case 'usage':
        return (b.usage_count ?? 0) - (a.usage_count ?? 0) || a.name.localeCompare(b.name);
      case 'maintainers':
        return (b.maintainer_count ?? 0) - (a.maintainer_count ?? 0) || a.name.localeCompare(b.name);
      case 'versions':
        return b.version_count - a.version_count || a.name.localeCompare(b.name);
      case 'name':
      default:
        return a.name.localeCompare(b.name);
    }
  }

  // Compact hostname-only display for the homepage chip. Falls back
  // to the raw URL when not parseable (rare — BCR enforces a real
  // URL when this field is set).
  function moduleHomepageDisplay(u: string): string {
    try {
      return new URL(u).hostname;
    } catch {
      return u;
    }
  }
</script>

<svelte:head>
  <title>modules — canopy</title>
</svelte:head>

<div class="flex flex-col gap-6">
  <nav class="text-[12px] font-mono text-fg-dim">
    <a href="/" class="hover:text-accent">canopy</a> /
    <span class="text-fg">modules</span>
  </nav>

  <header class="flex items-baseline justify-between gap-3 pb-3 border-b border-line flex-wrap">
    <div class="flex flex-col gap-2">
      <h1 class="font-mono text-2xl text-fg tracking-tight">modules</h1>
      <p class="text-[12px] text-fg-mute">
        every module indexed in this canopy — click through for versions, schemas, source.
      </p>
    </div>
    <ShareLink />
  </header>

  <div class="flex items-center gap-3 flex-wrap">
    <input
      bind:value={filter}
      type="text"
      placeholder="filter by name…"
      class="flex-1 min-w-[12rem] rounded-md border border-line bg-bg-elev px-3 py-2 text-[13px] font-mono text-fg outline-none focus:border-accent"
      autocomplete="off"
      spellcheck="false"
    />
    <!--
      Sort selector: alphabetical by default (matches the prior
      behavior), or one of the numeric facets (DESC). Bound to a
      string keystate, mapped in compareBy.
    -->
    <label class="text-[11px] text-fg-dim flex items-center gap-1.5">
      sort by
      <select
        bind:value={sortKey}
        class="rounded border border-line bg-bg-elev px-2 py-1 text-[11px] font-mono text-fg outline-none focus:border-accent"
      >
        <option value="name">name</option>
        <option value="usage">used by (popularity)</option>
        <option value="maintainers">maintainers</option>
        <option value="versions">versions</option>
      </select>
    </label>
    <label class="text-[11px] text-fg-dim flex items-center gap-1.5 cursor-pointer">
      <input
        type="checkbox"
        bind:checked={onlyWithSource}
        class="accent-accent"
      />
      with source only
    </label>
    {#if modules}
      <span class="text-[11px] font-mono text-fg-dim ml-auto">
        {filtered.length} of {modules.length}
      </span>
    {/if}
  </div>

  {#if error}
    <div class="rounded-md border border-err/30 bg-err/10 px-3 py-2 text-sm text-err">
      {error}
    </div>
  {:else if loading}
    <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-2">
      {#each Array.from({ length: 9 }) as _, i (i)}
        <div class="skeleton h-16 w-full"></div>
      {/each}
    </div>
  {:else if modules === null || modules.length === 0}
    <div class="text-center py-16 text-fg-dim">
      <p class="font-mono text-lg">no modules indexed yet</p>
      <p class="text-[12px] mt-4">
        run <code class="text-fg">canopy ingest</code> or POST to
        <code class="text-fg">/api/v1/actions/bump</code> to populate the corpus.
      </p>
    </div>
  {:else if filtered.length === 0}
    <p class="text-center py-12 text-fg-dim font-mono text-sm">
      no modules match <span class="text-fg">{filter}</span>
    </p>
  {:else}
    <ul class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-2 font-mono">
      {#each filtered as m (m.name)}
        <li
          class="border border-line rounded-md bg-bg-elev/50 hover:bg-bg-elev hover:border-line-strong transition-colors"
        >
          <a
            href={moduleHref(m.name)}
            class="flex flex-col gap-1 px-3 py-2.5 group"
          >
            <span class="flex items-baseline gap-2">
              <span class="text-[13px] text-fg group-hover:text-accent truncate">
                {m.name}
              </span>
              {#if m.is_new}
                <span
                  class="text-[9px] uppercase tracking-wider font-mono px-1.5 py-0.5 rounded bg-accent/15 text-accent border border-accent/30"
                  title="first version, ingested in the last 7 days"
                >
                  new
                </span>
              {/if}
            </span>
            <div class="flex items-baseline gap-2 text-[11px] text-fg-mute">
              <span class="text-fg-dim">latest</span>
              <span class="text-fg-mute">{m.latest_version}</span>
              <!--
                Drift chip: silent for in-sync / unknown / empty, ↑N
                / ⚠ yanked / local for actionable states. Clicks to
                /drift?module=<name>. See $components/DriftChip and
                $lib/components/drift-chip.ts. Plan 19 Idea A.
              -->
              <DriftChip drift={m.drift} module={m.name} />
              {#if m.latest_ingested_at}
                <span class="text-fg-dim" title="last ingested {m.latest_ingested_at}">
                  · {relativeTime(m.latest_ingested_at)}
                </span>
              {/if}
              {#if m.version_count > 1}
                <span class="text-fg-dim ml-auto">·</span>
                <span class="text-fg-dim">{m.version_count} versions</span>
              {/if}
            </div>
            <!--
              Registry-metadata row. Only renders when at least one
              metadata field is populated (modules that haven't been
              re-bumped since the mirror-enrichment landing show no
              row — cleaner than empty placeholders). Homepage is
              just hostname for compactness; full URL lives on the
              detail page.
            -->
            {#if m.repo_label || m.homepage || (m.maintainer_count ?? 0) > 0 || (m.usage_count ?? 0) > 0}
              <div class="flex items-baseline gap-2 text-[10px] text-fg-dim flex-wrap">
                {#if m.repo_label}
                  <!--
                    Server-derived "owner/repo" label is more informative
                    than the homepage hostname (which is "github.com" for
                    nearly every BCR module). Fall back to hostname when
                    repo_label is empty (non-github homepages, missing
                    metadata).
                  -->
                  <span class="truncate" title={m.homepage ?? m.repo_label}>{m.repo_label}</span>
                {:else if m.homepage}
                  <span class="truncate" title={m.homepage}>{moduleHomepageDisplay(m.homepage)}</span>
                {/if}
                {#if (m.maintainer_count ?? 0) > 0}
                  <span>{m.maintainer_count} maintainer{m.maintainer_count === 1 ? '' : 's'}</span>
                {/if}
                {#if (m.usage_count ?? 0) > 0}
                  <!--
                    'Used by N' is the popularity hint — modules at
                    the bottom of the dep graph (skylib, platforms)
                    score high; leaf modules score 0. Pushed to the
                    right with ml-auto so a glance down the column
                    aligns the numbers.
                  -->
                  <span class="ml-auto text-accent" title="modules in this index that depend on {m.name}">
                    used by {m.usage_count}
                  </span>
                {/if}
              </div>
            {/if}
          </a>
          <div class="flex border-t border-line/40 text-[11px] font-medium">
            <a
              href={moduleVersionHref(m.name, m.latest_version)}
              class="flex-1 px-3 py-1.5 text-fg-mute hover:text-accent text-center"
            >
              schema
            </a>
            {#if m.latest_diff_href}
              <a
                href={m.latest_diff_href}
                class="flex-1 px-3 py-1.5 text-fg-mute hover:text-accent text-center border-l border-line/40"
                title="structured diff of the latest bump"
              >
                diff
              </a>
            {/if}
            {#if m.has_source_index}
              <a
                href={codeNavRootHref(m.name, m.latest_version)}
                class="flex-1 px-3 py-1.5 text-fg-mute hover:text-accent text-center border-l border-line/40"
              >
                code →
              </a>
            {:else}
              <!--
                Modules whose source tarball ships no Starlark files
                (C library wrappers like zlib, header-only libraries
                wrapped for Bazel, etc.) get a static badge instead
                of a Code → link. Clicking through would land on an
                empty file tree — better to surface the fact here.
              -->
              <span
                class="flex-1 px-3 py-1.5 text-fg-dim text-center border-l border-line/40 italic cursor-default"
                title="This module's source tarball ships no Starlark files (.bzl/BUILD/etc.) — nothing to navigate."
              >
                no source
              </span>
            {/if}
          </div>
        </li>
      {/each}
    </ul>
  {/if}
</div>
