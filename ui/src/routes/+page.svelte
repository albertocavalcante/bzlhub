<script lang="ts">
  import SearchBar from '$components/SearchBar.svelte';
  import ResultRow from '$components/ResultRow.svelte';
  import HermeticityBadge from '$components/HermeticityBadge.svelte';
  import { getHistory, listModules, search, type CorpusStats, type ModuleSummary } from '$api/client';
  import type { AuditEvent, HermeticityClass, SearchResults } from '$api/types';
  import { moduleHref, moduleVersionHref, codeNavRootHref } from '$lib/links';
  import { relativeTime } from '$lib/time';
  import { page } from '$app/state';
  import { readParam, writeParam, stringField, stringList } from '$lib/url-state';

  // URL-bound search state (Plan 14):
  //   ?q=<text>                     — search query (replaceState — typing)
  //   ?hermeticity=class1,class2    — hermeticity filter chips (push)
  let query = $state<string>(readParam(page.url, 'q', stringField));
  let results = $state<SearchResults | null>(null);
  let loading = $state(false);
  let error = $state<string | null>(null);
  let elapsedMs = $state(0);
  // Hermeticity filter — codec emits a comma-separated list; mirror
  // it into the Set the rest of the page already uses.
  let activeFilters = $state<Set<HermeticityClass>>(
    new Set(readParam(page.url, 'hermeticity', stringList) as HermeticityClass[]),
  );

  // URL → state
  $effect(() => {
    const q = readParam(page.url, 'q', stringField);
    if (q !== query) query = q;
  });
  $effect(() => {
    const list = readParam(page.url, 'hermeticity', stringList);
    // Cheap equality check: same length + same members.
    const cur = Array.from(activeFilters).sort();
    const next = [...list].sort();
    if (cur.length !== next.length || cur.some((v, i) => v !== next[i])) {
      activeFilters = new Set(list as HermeticityClass[]);
    }
  });

  // State → URL. Typing uses replace-style history (avoid flooding
  // back stack); chip toggles use push (back button undoes).
  $effect(() => {
    writeParam(page.url, 'q', query, stringField, { history: 'replace' });
  });
  $effect(() => {
    writeParam(page.url, 'hermeticity', Array.from(activeFilters), stringList);
  });

  // Cancel in-flight requests on new input. Keeps the trailing race-condition
  // away when a slow query finishes after a faster newer one.
  let inflight: AbortController | null = null;
  let debounceTimer: ReturnType<typeof setTimeout> | null = null;

  const ALL_CLASSES: HermeticityClass[] = [
    'pure-starlark',
    'prebuilt-binaries-pinned',
    'build-from-source',
    'network-fetch-pinned',
    'network-fetch-unpinned',
    'requires-system-tools',
    'repository-rule-arbitrary-code',
  ];

  // parseQuery extracts a known prefix (attr: / rule: / provider: /
  // macro: / repo_rule: / module_extension:) into a separate field.
  // Examples:
  //   "attr:srcs"               → { text: "",          attr: "srcs" }
  //   "rule:cc_binary"          → { text: "cc_binary", kind: "rule" }
  //   "provider:CcInfo"         → { text: "CcInfo",    kind: "provider" }
  //   "cc_binary"               → { text: "cc_binary" }
  //
  // attr: keeps its existing "this is the attribute NAME" semantic
  // (the trailing free-text becomes the secondary filter — works
  // alongside attr:). The new kind: prefixes use the trailing free-
  // text as the symbol NAME to look up (exact match).
  type ParsedQuery = {
    text: string;
    attr?: string;
    kind?: 'rule' | 'provider' | 'macro' | 'repo_rule' | 'module_extension';
  };
  const KIND_PREFIXES: Record<string, ParsedQuery['kind']> = {
    rule: 'rule',
    provider: 'provider',
    macro: 'macro',
    repo_rule: 'repo_rule',
    module_extension: 'module_extension',
  };
  function parseQuery(raw: string): ParsedQuery {
    // attr: keeps its existing attr-name-extraction semantic.
    const attrM = raw.match(/^attr:([A-Za-z_][A-Za-z0-9_]*)\s*(.*)$/);
    if (attrM) {
      return { text: attrM[2].trim(), attr: attrM[1] };
    }
    // kind: prefix → set kind, treat the trailing token as the
    // symbol name.
    const kindM = raw.match(/^([a-z_]+):([A-Za-z_][A-Za-z0-9_]*)\s*$/);
    if (kindM && KIND_PREFIXES[kindM[1]]) {
      return { text: kindM[2], kind: KIND_PREFIXES[kindM[1]] };
    }
    return { text: raw };
  }

  // Reactive: re-search whenever query or filters change. The 50ms debounce
  // keeps the network calm during fast typing.
  $effect(() => {
    const raw = query.trim();
    const filters = Array.from(activeFilters);

    if (debounceTimer) clearTimeout(debounceTimer);
    if (inflight) inflight.abort();
    if (raw.length === 0) {
      results = null;
      error = null;
      loading = false;
      return;
    }

    const parsed = parseQuery(raw);
    // attr-only and kind-only queries can have empty text; FTS path
    // (no facet at all) needs non-empty text.
    if (!parsed.attr && !parsed.kind && parsed.text.length === 0) {
      results = null;
      loading = false;
      return;
    }

    loading = true;
    debounceTimer = setTimeout(async () => {
      const ctl = new AbortController();
      inflight = ctl;
      const t0 = performance.now();
      try {
        const res = await search(
          { q: parsed.text, attr: parsed.attr, kind: parsed.kind, hermeticity: filters, limit: 50 },
          ctl.signal,
        );
        if (ctl.signal.aborted) return;
        results = res;
        error = null;
        elapsedMs = Math.round(performance.now() - t0);
      } catch (e: unknown) {
        if (ctl.signal.aborted) return;
        error = e instanceof Error ? e.message : String(e);
        results = null;
      } finally {
        if (inflight === ctl) loading = false;
      }
    }, 50);
  });

  function toggleFilter(c: HermeticityClass) {
    if (activeFilters.has(c)) {
      activeFilters.delete(c);
    } else {
      activeFilters.add(c);
    }
    activeFilters = new Set(activeFilters); // trigger reactivity
  }

  // "Recently indexed" section, populated on first mount from
  // /api/v1/activity/history filtered to bump_success. Surfaces the corpus on the
  // empty-state page so users landing fresh see something to click
  // instead of just a search hint. Only re-fetched when a new
  // audit_recorded event fires (debounced); a static-once-per-load
  // shape would be fine too but the SSE wire is already there.
  let recentBumps = $state<AuditEvent[]>([]);

  // Corpus snapshot for the home-page dashboard: total modules /
  // versions and the top-5 most-depended-on modules. Computed
  // from listModules() — the same shape /modules already renders
  // — so it's a single extra fetch on home load.
  let modules = $state<ModuleSummary[]>([]);
  let corpusStats = $state<CorpusStats | null>(null);

  $effect(() => {
    let aborted = false;
    const ctl = new AbortController();
    void getHistory({ kind: ['bump_success'], limit: 8 }, ctl.signal)
      .then((r) => {
        if (!aborted) recentBumps = r.events ?? [];
      })
      .catch(() => {
        // Silent — recent-bumps is decorative; we'd rather render
        // the bare empty-state than show a noisy error on home.
      });
    void listModules(ctl.signal)
      .then((r) => {
        if (aborted) return;
        modules = r.modules ?? [];
        corpusStats = r.corpus_stats ?? null;
      })
      .catch(() => {
        // Silent — dashboard is decorative; fall back to bare
        // empty-state on failure.
      });
    return () => {
      aborted = true;
      ctl.abort();
    };
  });

  // Stats are pure read-shape derivations off `modules`; not
  // business logic. UI uses .length and a sort — the kind of
  // display-side reshape the "thin frontend" rule explicitly
  // allows when the input is already in the response.
  // Prefer server-shipped stats; fall back to local derivation for
  // backends that haven't shipped corpus_stats yet (older canopy).
  const totalModules = $derived(corpusStats?.modules ?? modules.length);
  const totalVersions = $derived(
    corpusStats?.versions ?? modules.reduce((acc, m) => acc + (m.version_count ?? 0), 0),
  );
  const documentedSymbols = $derived(corpusStats?.documented_symbols ?? 0);
  const topUsed = $derived(
    modules
      .filter((m) => (m.usage_count ?? 0) > 0)
      .slice()
      .sort((a, b) => (b.usage_count ?? 0) - (a.usage_count ?? 0))
      .slice(0, 5),
  );

</script>

<svelte:head>
  <title>canopy — search</title>
</svelte:head>

<div class="mx-auto w-full max-w-3xl flex flex-col gap-6 pt-8 md:pt-16">
  <section class="flex flex-col gap-3">
    <SearchBar bind:value={query} placeholder="search modules, rules, providers… (try attr:srcs or rule:cc_binary)" />

    <div class="flex flex-wrap items-center gap-1.5">
      <span class="text-[10px] uppercase tracking-wider text-fg-dim/70 mr-1">hermeticity</span>
      {#each ALL_CLASSES as cls (cls)}
        {@const active = activeFilters.has(cls)}
        <button
          type="button"
          onclick={() => toggleFilter(cls)}
          class="cursor-pointer outline-offset-2 transition-opacity"
          class:opacity-100={active}
          class:opacity-50={!active}
        >
          <HermeticityBadge class={cls} compact />
        </button>
      {/each}
      {#if activeFilters.size > 0}
        <button
          type="button"
          onclick={() => (activeFilters = new Set())}
          class="text-[11px] text-fg-dim hover:text-fg ml-1 cursor-pointer"
        >
          clear
        </button>
      {/if}
    </div>
  </section>

  <section>
    {#if error}
      <div
        class="rounded-md border border-err/30 bg-err/10 px-3 py-2 text-sm text-err"
        role="alert"
      >
        {error}
      </div>
    {:else if loading && !results}
      <ul class="flex flex-col">
        {#each Array.from({ length: 6 }) as _, i (i)}
          <li class="flex items-center gap-3 py-2 -mx-3 px-3">
            <div class="skeleton w-12 h-5"></div>
            <div class="flex-1 flex flex-col gap-1.5">
              <div class="skeleton h-3.5 w-1/3"></div>
              <div class="skeleton h-3 w-2/3"></div>
            </div>
          </li>
        {/each}
      </ul>
    {:else if results && (results.hits ?? []).length === 0}
      <div class="text-center py-16 text-fg-dim">
        <p class="font-mono text-sm">no hits for <span class="text-fg">{query}</span></p>
        <p class="text-xs mt-2">
          try a different term, or run <kbd>canopy ingest &lt;module-dir&gt;</kbd> first
        </p>
      </div>
    {:else if results}
      <div class="flex items-baseline justify-between mb-2 px-3">
        <span class="text-xs text-fg-mute font-mono">
          {results.total} hit{results.total === 1 ? '' : 's'}
          <span class="text-fg-dim">·</span>
          {elapsedMs}ms
        </span>
        {#if loading}
          <span class="text-xs text-fg-dim font-mono">searching…</span>
        {/if}
      </div>
      <ul class="flex flex-col">
        {#each (results.hits ?? []) as hit, i (i)}
          <li>
            <ResultRow {hit} />
          </li>
        {/each}
      </ul>
    {:else}
      <!--
        Empty-state dashboard. Operator-facing snapshot of the
        corpus: indexed counts, the most-depended-on modules, and
        the recent-ingest stream. Goal: a fresh landing should feel
        like a registry dashboard, not a blank search box.
      -->
      <div class="flex flex-col gap-10 py-8">
        <div class="text-center text-fg-dim">
          <p class="font-mono text-sm">type to search</p>
          <p class="text-xs mt-3">
            tries <span class="text-fg-mute">cc_binary</span>,
            <span class="text-fg-mute">execute</span>, or
            <span class="text-fg-mute">rules_</span>
            <span class="text-fg-dim">·</span>
            <a href="/modules" class="text-fg-mute hover:text-accent">browse all modules →</a>
          </p>
        </div>

        {#if totalModules > 0}
          <!--
            Corpus stats strip. Counters + a "drift →" link pointing
            to the operator dashboard. Documented-symbols tile only
            renders when the server actually shipped the value (so
            backwards-compat with older canopy backends works).
          -->
          <section class="grid grid-cols-2 sm:grid-cols-4 gap-3 text-[12px] font-mono">
            <a
              href="/modules"
              class="flex flex-col gap-0.5 rounded-md border border-line bg-bg-elev/40 px-3 py-2 hover:border-accent/60"
            >
              <span class="text-[10px] uppercase tracking-wide text-fg-dim">modules</span>
              <span class="text-fg text-[18px]">{totalModules}</span>
            </a>
            <div class="flex flex-col gap-0.5 rounded-md border border-line bg-bg-elev/40 px-3 py-2">
              <span class="text-[10px] uppercase tracking-wide text-fg-dim">versions</span>
              <span class="text-fg text-[18px]">{totalVersions}</span>
            </div>
            {#if corpusStats}
              <div class="flex flex-col gap-0.5 rounded-md border border-line bg-bg-elev/40 px-3 py-2">
                <span class="text-[10px] uppercase tracking-wide text-fg-dim">documented symbols</span>
                <span class="text-fg text-[18px]">{documentedSymbols.toLocaleString()}</span>
              </div>
            {/if}
            <a
              href="/drift"
              class="flex flex-col gap-0.5 rounded-md border border-line bg-bg-elev/40 px-3 py-2 hover:border-accent/60"
            >
              <span class="text-[10px] uppercase tracking-wide text-fg-dim">drift</span>
              <span class="text-fg text-[14px]">vs upstream →</span>
            </a>
          </section>
        {/if}

        {#if topUsed.length > 0}
          <section class="flex flex-col gap-2">
            <h2 class="text-[10px] uppercase tracking-wide text-fg-dim font-mono">
              most depended on
            </h2>
            <ul class="flex flex-col">
              {#each topUsed as m (m.name)}
                <li
                  class="flex items-baseline gap-3 px-3 py-1.5 -mx-3 rounded-md hover:bg-bg-elev/60 text-[13px] font-mono"
                >
                  <a
                    href={moduleHref(m.name)}
                    class="text-fg hover:text-accent truncate"
                  >
                    {m.name}
                  </a>
                  <span class="text-[11px] text-fg-dim">latest {m.latest_version}</span>
                  {#if m.latest_ingested_at}
                    <span class="text-[11px] text-fg-dim" title={m.latest_ingested_at}>
                      · {relativeTime(m.latest_ingested_at)}
                    </span>
                  {/if}
                  <span class="ml-auto text-[11px] text-accent" title="modules in this index that depend on {m.name}">
                    used by {m.usage_count}
                  </span>
                </li>
              {/each}
            </ul>
          </section>
        {/if}

        {#if recentBumps.length > 0}
          <section class="flex flex-col gap-2">
            <h2 class="text-[10px] uppercase tracking-wide text-fg-dim font-mono">
              recently indexed
            </h2>
            <ul class="flex flex-col">
              {#each recentBumps as ev (ev.id)}
                {#if ev.module && ev.version}
                  <li
                    class="flex items-baseline gap-3 px-3 py-1.5 -mx-3 rounded-md hover:bg-bg-elev/60 text-[13px] font-mono"
                  >
                    <a
                      href={moduleVersionHref(ev.module, ev.version)}
                      class="text-fg hover:text-accent truncate"
                    >
                      {ev.module}<span class="text-fg-dim">@</span>{ev.version}
                    </a>
                    <span class="text-[11px] text-fg-dim" title={ev.timestamp}>
                      {relativeTime(ev.timestamp)}
                    </span>
                    <a
                      href={codeNavRootHref(ev.module, ev.version)}
                      class="ml-auto text-[11px] text-fg-mute hover:text-accent px-2 py-0.5 rounded"
                    >
                      Code →
                    </a>
                  </li>
                {/if}
              {/each}
            </ul>
          </section>
        {/if}
      </div>
    {/if}
  </section>
</div>
