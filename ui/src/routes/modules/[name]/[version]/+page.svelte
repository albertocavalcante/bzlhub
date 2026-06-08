<script lang="ts">
  import { page } from '$app/state';
  import {
    getClosureGraph,
    getModuleSummary,
    listVersions,
    type ClosureGraphResponse,
    type DriftSummary,
    type VersionEntry,
  } from '$api/client';
  import { moduleVersion } from '$lib/state/moduleVersion.svelte';
  import ClosureGraph from '$components/ClosureGraph.svelte';
  import DiffPicker from '$components/DiffPicker.svelte';
  import DriftChip from '$components/DriftChip.svelte';
  import InstallSnippet from '$components/InstallSnippet.svelte';
  import ReverseDeps from '$components/ReverseDeps.svelte';
  import HermeticityBadge from '$components/HermeticityBadge.svelte';
  import MarkdownDoc from '$components/MarkdownDoc.svelte';
  import ModuleNotFound from '$components/ModuleNotFound.svelte';
  import {
    codeNavFileHref,
    diffVsUpstreamHref,
    displayVersion,
    isNavigableVersion,
    isStubVersion,
    moduleVersionHref,
  } from '$lib/links';
  import { formatBytes } from '$lib/time';

  // Curated palette for the GitHub languages bar. Keys match the
  // names GitHub returns from /languages; anything missing falls
  // back to a neutral grey. We deliberately don't pull
  // linguist-colors here — small fixed set keeps the bundle lean.
  const LANG_COLORS: Record<string, string> = {
    Go: '#00ADD8',
    Starlark: '#76d275',
    Python: '#3572A5',
    'C++': '#f34b7d',
    C: '#555555',
    Java: '#b07219',
    Kotlin: '#A97BFF',
    JavaScript: '#f1e05a',
    TypeScript: '#3178c6',
    Rust: '#dea584',
    Shell: '#89e051',
    'Objective-C': '#438eff',
    Swift: '#F05138',
    Ruby: '#701516',
    Scala: '#c22d40',
    Dockerfile: '#384d54',
  };
  function languageColor(lang: string): string {
    return LANG_COLORS[lang] ?? '#6e7681';
  }

  // Report state is owned by the layout's shared store so sibling
  // tabs (Documentation/Testing in Phase B) read the same payload
  // without re-fetching. Derived locals here keep the existing
  // template syntax (`report`, `loading`, `error`) unchanged.
  const report = $derived(moduleVersion.report);
  const loading = $derived(moduleVersion.loading);
  const error = $derived(moduleVersion.error);

  // Other indexed versions of this same module — drives the
  // "diff with…" picker in the header. Fetched once per module
  // name (not per version), so navigating between versions
  // doesn't re-fetch the same list.
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
        // Silent: the picker just won't render. Module load
        // continues independently.
      });
    return () => ctl.abort();
  });

  // ModuleNotFound calls this once an ingest completes successfully,
  // letting the page swap from 404 to the real report without a full
  // navigation. The shared store handles refresh + abort semantics.
  function onIngestResolved() {
    const name = page.params.name;
    const version = page.params.version;
    if (name && version) moduleVersion.refresh(name, version);
  }

  // Anchor compatibility for the I8 tab split: every symbol card
  // (rules, providers, macros, repo_rules, module_extensions,
  // toolchains) used to live on this page, so deep links like
  // /modules/m/v#cc_binary or /modules/m/v#repo-rule-X still arrive
  // here. The Documentation tab now owns those anchors — redirect
  // there preserving the fragment so the browser still scrolls to
  // the target.
  //
  // We don't try to differentiate symbol-anchors from arbitrary
  // hashes: Overview itself doesn't define any anchors that would
  // collide, so "any non-empty hash" is the safe trigger. Runs on
  // mount only (re-running on every report change would fight the
  // user's manual scroll positioning).
  $effect(() => {
    if (typeof window === 'undefined') return;
    const hash = window.location.hash;
    if (!hash || hash === '#') return;
    const name = page.params.name;
    const version = page.params.version;
    if (!name || !version) return;
    const target = `/modules/${encodeURIComponent(name)}/${encodeURIComponent(version)}/docs${hash}`;
    // Replace rather than push so the user's back button still goes
    // to the previous page, not back to the same module's Overview.
    window.location.replace(target);
  });

  // Dependencies card mode: 'direct' shows the report's bazel_deps
  // (already in memory), 'transitive' shows the MVS-walked closure
  // (fetched lazily on first switch). Cached in $state so toggling
  // back and forth doesn't re-fetch.
  let depsMode = $state<'direct' | 'transitive'>('direct');
  let closureGraph = $state<ClosureGraphResponse | null>(null);
  let closureLoading = $state(false);
  let closureError = $state<string | null>(null);

  // Trigger the closure fetch when user first toggles to transitive.
  // Subsequent toggles reuse the cached graph. New page navigation
  // resets via $effect on report identity.
  $effect(() => {
    if (depsMode !== 'transitive' || closureGraph || closureLoading) return;
    const name = report?.name;
    const version = report?.version;
    if (!name || !version) return;
    closureLoading = true;
    closureError = null;
    const ctl = new AbortController();
    void getClosureGraph(name, version, ctl.signal)
      .then((g) => {
        closureGraph = g;
      })
      .catch((e: unknown) => {
        closureError = e instanceof Error ? e.message : String(e);
      })
      .finally(() => {
        closureLoading = false;
      });
    return () => ctl.abort();
  });

  // Drift surfaced from ModuleSummary (M1 backend; Plan 19 Idea A).
  // Fetched in parallel with the report so the title-strip chip is
  // never the long pole. Missing/error → no chip (DriftChip handles
  // silent rendering); we don't surface fetch errors to the user
  // because the drift signal is not load-bearing for the page.
  let drift = $state<DriftSummary | null>(null);

  $effect(() => {
    const name = report?.name;
    if (!name) {
      drift = null;
      return;
    }
    const ctl = new AbortController();
    void getModuleSummary(name, ctl.signal)
      .then((m) => {
        drift = m.drift ?? null;
      })
      .catch(() => {
        drift = null;
      });
    return () => ctl.abort();
  });

  // Reset closure state when navigating between modules / versions.
  $effect(() => {
    void report?.name;
    void report?.version;
    closureGraph = null;
    closureError = null;
    depsMode = 'direct';
  });

  // Transitive node list excludes the root itself + sorts for
  // scannability. External nodes (deps not indexed in this bzlhub)
  // surface at the end with a dim style.
  const transitiveDeps = $derived.by(() => {
    if (!closureGraph) return [] as ClosureGraphResponse['nodes'];
    return closureGraph.nodes
      .filter((n) => !(n.name === report?.name && n.version === report?.version))
      .slice()
      .sort((a, b) => {
        if (!!a.external !== !!b.external) return a.external ? 1 : -1;
        return a.name.localeCompare(b.name);
      });
  });
  // Render an absolute URL's hostname (e.g. https://github.com/foo/bar
  // → github.com) so the homepage chip stays compact. Falls back to
  // the raw URL when it isn't parseable — preferable to swallowing
  // the link entirely.
  function displayHostname(u: string): string {
    try {
      return new URL(u).hostname;
    } catch {
      return u;
    }
  }

  // BCR's metadata.json repository entries are scheme-prefixed like
  // "github:owner/repo" — not real URLs. Expand the few recognized
  // schemes into clickable URLs; pass anything else through as-is
  // (the user can still copy it, and we don't want to invent links
  // for schemes we don't actually know how to resolve).
  function repoUrl(repo: string): string {
    if (repo.startsWith('github:')) return `https://github.com/${repo.slice('github:'.length)}`;
    if (repo.startsWith('gitlab:')) return `https://gitlab.com/${repo.slice('gitlab:'.length)}`;
    if (repo.startsWith('https://') || repo.startsWith('http://')) return repo;
    return repo;
  }
</script>

<svelte:head>
  <title>{report ? `${report.name}@${report.version}` : 'module'} — bzlhub</title>
</svelte:head>

{#if loading}
  <div class="flex flex-col gap-6">
    <div class="skeleton h-9 w-72"></div>
    <div class="flex gap-2">
      <div class="skeleton h-5 w-32"></div>
      <div class="skeleton h-5 w-24"></div>
    </div>
    <div class="skeleton h-64 w-full"></div>
  </div>
{:else if error === 'not_found'}
  <!--
    Everything about the 404 experience — preflight probe, ingest
    state machine, suggestions for typos / version mismatches —
    lives in ModuleNotFound. Page only knows when the component
    resolves so it can re-fetch the module in place.
  -->
  <ModuleNotFound
    name={page.params.name ?? ''}
    version={page.params.version ?? ''}
    onresolved={onIngestResolved}
  />
{:else if error}
  <div
    class="rounded-md border border-err/30 bg-err/10 px-3 py-2 text-sm text-err"
    role="alert"
  >
    {error}
  </div>
{:else if report}
  <article class="flex flex-col gap-8">
    <nav
      class="text-[11px] text-fg-dim font-mono flex items-center gap-1.5"
      aria-label="breadcrumb"
    >
      <a href="/" class="hover:text-fg transition-colors">bzlhub</a>
      <span class="text-fg-dim/60">/</span>
      <span class="text-fg-mute">modules</span>
      <span class="text-fg-dim/60">/</span>
      <span class="text-fg-mute">{report.name}</span>
      <span class="text-fg-dim/60">/</span>
      <span class="text-fg">{report.version || 'HEAD'}</span>
    </nav>
    <header class="flex flex-col gap-3 pb-4 border-b border-line">
      <div class="flex items-baseline gap-3 flex-wrap">
        <h1 class="font-mono text-2xl text-fg tracking-tight">
          {report.name}<span class="text-fg-dim">@</span>{report.version || 'HEAD'}
        </h1>
        <!--
          Inline drift chip on the title strip (Plan 19 Idea A).
          Silent when the cached drift is unknown / in-sync / empty,
          which is the default until a drift source (bcrmirror,
          sync-runner) is wired. Clicks through to
          /drift?module=<name>.
        -->
        {#if drift}
          <DriftChip {drift} module={report.name} />
        {/if}
        {#if report.compatibility_level !== undefined && report.compatibility_level !== null}
          <span class="text-[11px] text-fg-dim font-mono">
            compat L{report.compatibility_level}
          </span>
        {/if}
        {#if report.tarball_size}
          <span
            class="text-[11px] text-fg-dim font-mono"
            title="compressed source tarball: {report.tarball_size.toLocaleString()} bytes"
          >
            {formatBytes(report.tarball_size)}
          </span>
        {/if}
      </div>

      {#if report.hermeticity?.classes?.length || report.assets?.license_name || report.assets?.license_path}
        <div class="flex items-center gap-2 flex-wrap">
          {#if report.hermeticity?.classes}
            {#each report.hermeticity.classes as cls (cls)}
              <HermeticityBadge class={cls} />
            {/each}
          {/if}
          <!--
            License badge: surface the detected SPDX name when we
            recognized it, otherwise just "view license" so users
            still get a one-click path to the raw file. Click lands
            on code-nav at the LICENSE file.
          -->
          {#if report.assets?.license_path && page.params.name && page.params.version}
            <a
              href={codeNavFileHref(page.params.name, page.params.version, report.assets.license_path)}
              class="inline-flex items-center gap-1 text-[11px] font-mono rounded border border-line bg-bg-elev/60 px-2 py-0.5 text-fg-mute hover:border-accent hover:text-accent"
              title="View license source"
              data-testid="license-badge"
            >
              <span class="text-[9px] uppercase tracking-wide text-fg-dim">license</span>
              <span class="text-fg">{report.assets.license_name || 'view'}</span>
            </a>
          {/if}
        </div>
      {/if}

      {#if (report.metadata && (report.metadata?.homepage || (report.metadata?.repository?.length ?? 0) > 0 || (report.metadata?.maintainers?.length ?? 0) > 0)) || (report.usage_count ?? 0) > 0}
        <!--
          Registry-level metadata chips. Surfaces the BCR
          metadata.json fields the local mirror has lifted from
          upstream (homepage / repository / maintainers). When a
          field is absent we just don't render its chip — no need
          for placeholders.
        -->
        <div
          class="flex items-center gap-2 flex-wrap text-[12px]"
          data-testid="registry-metadata"
        >
          {#if report.metadata?.homepage}
            <a
              href={report.metadata?.homepage}
              target="_blank"
              rel="noopener noreferrer"
              class="inline-flex items-center gap-1 text-fg-mute hover:text-accent"
              title="Project homepage"
            >
              <span class="text-[10px] uppercase tracking-wide text-fg-dim">home</span>
              <span class="underline">{displayHostname(report.metadata?.homepage)}</span>
            </a>
          {/if}
          {#each report.metadata?.repository ?? [] as repo (repo)}
            <a
              href={repoUrl(repo)}
              target="_blank"
              rel="noopener noreferrer"
              class="inline-flex items-center gap-1 text-fg-mute hover:text-accent"
              title="Source repository"
            >
              <span class="text-[10px] uppercase tracking-wide text-fg-dim">repo</span>
              <span class="font-mono underline">{repo}</span>
            </a>
          {/each}
          {#if (report.metadata?.maintainers?.length ?? 0) > 0}
            <span class="inline-flex items-baseline gap-1 text-fg-mute">
              <span class="text-[10px] uppercase tracking-wide text-fg-dim">maintainers</span>
              {#each report.metadata?.maintainers ?? [] as m, i (m.name + i)}
                {#if m.github}
                  <a
                    href={`https://github.com/${m.github}`}
                    target="_blank"
                    rel="noopener noreferrer"
                    class="hover:text-accent"
                    title={m.email ?? m.name}
                  >
                    {m.name}
                  </a>
                {:else if m.email}
                  <!--
                    F4 fallback: when there's no GitHub handle but an
                    email is present, link to mailto: so the chip
                    isn't a dead-end.
                  -->
                  <a
                    href={`mailto:${m.email}`}
                    class="hover:text-accent"
                    title={m.email}
                  >
                    {m.name}
                  </a>
                {:else}
                  <span>{m.name}</span>
                {/if}
                {#if i < (report.metadata?.maintainers?.length ?? 0) - 1}<span class="text-fg-dim">,</span>{/if}
              {/each}
            </span>
          {/if}
          {#if (report.usage_count ?? 0) > 0}
            <!--
              Cross-corpus 'used by N' chip — same data and same
              label as the listing card. Lets the detail page
              reuse the popularity hint without a separate fetch.
            -->
            <span
              class="inline-flex items-center gap-1 text-fg-mute"
              title="indexed modules whose bazel_deps reference {report.name}"
              data-testid="usage-count"
            >
              <span class="text-[10px] uppercase tracking-wide text-fg-dim">used by</span>
              <span class="text-accent">{report.usage_count}</span>
            </span>
          {/if}
          {#if report.provenance?.bcr_head_sha}
            <!--
              Cheap BCR provenance (I4): the
              bazelbuild/bazel-central-registry HEAD commit captured
              at Bump time. The 7-char prefix matches GitHub's
              convention; the title shows the full SHA + recorded
              timestamp for verification.
            -->
            <a
              href={report.provenance.url ?? `https://github.com/bazelbuild/bazel-central-registry/tree/${report.provenance.bcr_head_sha}`}
              target="_blank"
              rel="noopener noreferrer"
              class="inline-flex items-center gap-1 text-fg-mute hover:text-accent"
              title={`Ingested from BCR at ${report.provenance.bcr_head_sha} on ${report.provenance.recorded_at}`}
              data-testid="bcr-provenance"
            >
              <span class="text-[10px] uppercase tracking-wide text-fg-dim">bcr</span>
              <span class="font-mono underline">{report.provenance.bcr_head_sha.slice(0, 7)}</span>
            </a>
          {/if}
        </div>
      {/if}

      {#if report.github_meta}
        <!--
          GitHub social signals (I5) + languages bar (I9). Server
          refreshes every 6h via internal/githubmeta + the
          TokenProvider abstraction; UI just renders. The languages
          bar uses GitHub's per-language byte counts to weight each
          segment.
        -->
        {@const ghm = report.github_meta}
        {@const ghHref = `https://github.com/${ghm.owner}/${ghm.repo}`}
        {@const langEntries = Object.entries(ghm.languages ?? {})}
        {@const langTotal = langEntries.reduce((sum, [, n]) => sum + n, 0)}
        <div
          class="flex flex-col gap-2"
          data-testid="github-meta"
        >
          <div class="flex items-center gap-3 flex-wrap text-[12px] text-fg-mute">
            <a
              href={ghHref}
              target="_blank"
              rel="noopener noreferrer"
              class="inline-flex items-center gap-1 hover:text-accent"
              title="View on GitHub"
            >
              <span class="text-[10px] uppercase tracking-wide text-fg-dim">github</span>
              <span class="font-mono">{ghm.owner}/{ghm.repo}</span>
            </a>
            <span class="inline-flex items-center gap-1" title="GitHub stars">
              <span class="text-[10px] uppercase tracking-wide text-fg-dim">★</span>
              <span class="text-fg">{ghm.stars.toLocaleString()}</span>
            </span>
            <span class="inline-flex items-center gap-1" title="GitHub forks">
              <span class="text-[10px] uppercase tracking-wide text-fg-dim">forks</span>
              <span class="text-fg">{ghm.forks.toLocaleString()}</span>
            </span>
            {#if ghm.watchers > 0}
              <span class="inline-flex items-center gap-1" title="GitHub watchers (subscribers)">
                <span class="text-[10px] uppercase tracking-wide text-fg-dim">watchers</span>
                <span class="text-fg">{ghm.watchers.toLocaleString()}</span>
              </span>
            {/if}
            {#if ghm.primary_language}
              <span class="inline-flex items-center gap-1" title="primary language">
                <span class="text-[10px] uppercase tracking-wide text-fg-dim">lang</span>
                <span class="text-fg">{ghm.primary_language}</span>
              </span>
            {/if}
          </div>
          {#if langEntries.length > 0 && langTotal > 0}
            <div class="flex items-center gap-2 text-[10px] text-fg-dim">
              <div
                class="flex h-1.5 w-48 rounded overflow-hidden border border-line"
                title={langEntries
                  .map(([k, n]) => `${k} ${((n / langTotal) * 100).toFixed(1)}%`)
                  .join(' · ')}
              >
                {#each langEntries as [lang, bytes] (lang)}
                  <span
                    class="h-full"
                    style="width: {(bytes / langTotal) * 100}%; background: {languageColor(lang)};"
                  ></span>
                {/each}
              </div>
              <span class="font-mono">
                {#each langEntries.slice(0, 3) as [lang] (lang)}
                  <span class="ml-1">{lang}</span>
                {/each}
              </span>
            </div>
          {/if}
        </div>
      {/if}

      {#if report.bazel_compatibility && report.bazel_compatibility.length > 0}
        <div class="flex items-center gap-2 flex-wrap text-[12px] text-fg-mute">
          <span class="text-[10px] uppercase tracking-wide text-fg-dim">bazel</span>
          {#each report.bazel_compatibility as bc (bc)}
            <code class="text-fg">{bc}</code>
          {/each}
        </div>
      {/if}

      <!--
        Code-nav handoff. Surfaces ONLY when the report carries at
        least one extracted symbol — empty reports (C library
        wrappers like zlib whose tarballs ship no .bzl) would land
        the user on an empty file tree, which is worse than the
        absence of the link. Same signal informs has_source_index on
        the /api/v1/modules listing.
      -->
      {#if (report.rules?.length ?? 0) + (report.providers?.length ?? 0) + (report.macros?.length ?? 0) + (report.repository_rules?.length ?? 0) + (report.module_extensions?.length ?? 0) > 0}
        <div class="flex items-center gap-3 text-[12px] flex-wrap">
          <a
            href="./code-nav/"
            class="font-mono text-accent hover:underline"
          >
            → Browse source
          </a>
          <span class="text-fg-dim">click any symbol to navigate</span>
          {#if report.version && !isStubVersion(report.version)}
            <!--
              F3: diff this version against the same version
              freshly-fetched from upstream BCR. Mirror-integrity
              check ("is what we mirrored still what upstream has?").
              Skipped for stub versions where the diff would be
              meaningless.
            -->
            <a
              href={diffVsUpstreamHref(report.name, report.version)}
              class="ml-auto text-[11px] text-fg-mute hover:text-accent font-mono"
              title="diff this version against the same version freshly-fetched from upstream BCR (mirror-integrity check)"
            >
              vs upstream →
            </a>
          {/if}
          <DiffPicker
            module={page.params.name ?? ''}
            current={page.params.version ?? ''}
            entries={versionEntries}
          />
        </div>
      {:else}
        <div class="flex items-center gap-2 text-[12px]">
          <span class="font-mono text-fg-dim italic">
            no Starlark source in this module
          </span>
          <span class="text-fg-dim">
            — BCR wraps this with metadata stored in the registry, not in the tarball.
          </span>
        </div>
      {/if}
    </header>

    {#if report.version && !isStubVersion(report.version)}
      <!--
        Install-snippet card. Top-of-body so it's the first answer
        to "how do I add this?" Hidden for stub versions (0.0.0,
        etc.) where the snippet would be misleading.
      -->
      <InstallSnippet name={report.name} version={report.version} />
    {/if}

    {#if report.assets?.readme}
      <!--
        README lead. Placed immediately under the header because
        this is the section a reader landing on a module page most
        wants — "what is this thing?" The MarkdownDoc dedent prepass
        handles weird leading indentation; the link-to-source kicker
        gives users an escape hatch to see the raw markdown via
        code-nav (e.g. to copy install snippets verbatim).
      -->
      <section class="flex flex-col gap-2" data-testid="readme-section">
        <div class="flex items-baseline gap-2 text-xs">
          <h2 class="uppercase tracking-wide text-fg-dim">readme</h2>
          {#if report.assets.readme_path && page.params.name && page.params.version}
            <a
              href={codeNavFileHref(page.params.name, page.params.version, report.assets.readme_path)}
              class="text-fg-dim hover:text-accent font-mono"
              title="View raw source"
            >
              ↗ {report.assets.readme_path}
            </a>
          {/if}
        </div>
        <article
          class="rounded-md border border-line bg-bg-elev/40 px-4 py-3"
        >
          <MarkdownDoc source={report.assets.readme} />
        </article>
      </section>
    {/if}

    {#if report.bazel_deps && report.bazel_deps.length > 0}
      <section class="flex flex-col gap-3">
        <div class="flex items-baseline gap-3 flex-wrap">
          <h2 class="text-xs uppercase tracking-wide text-fg-dim">
            dependencies
          </h2>
          <!--
            Direct/transitive toggle. Direct counts come from the
            report; transitive count is "—" until first switch (avoids
            kicking off the closure walk on every page view).
          -->
          <div class="inline-flex rounded-md border border-line overflow-hidden text-[11px] font-mono">
            <button
              type="button"
              class="px-2.5 py-0.5 transition-colors cursor-pointer
                {depsMode === 'direct'
                  ? 'bg-bg-elev text-fg'
                  : 'text-fg-mute hover:text-fg'}"
              onclick={() => (depsMode = 'direct')}
              aria-pressed={depsMode === 'direct'}
            >
              direct · {report.bazel_deps.length}
            </button>
            <button
              type="button"
              class="px-2.5 py-0.5 border-l border-line transition-colors cursor-pointer
                {depsMode === 'transitive'
                  ? 'bg-bg-elev text-fg'
                  : 'text-fg-mute hover:text-fg'}"
              onclick={() => (depsMode = 'transitive')}
              aria-pressed={depsMode === 'transitive'}
              title="MVS-walked closure (lazy-loaded on first click)"
            >
              transitive{#if closureGraph} · {transitiveDeps.length}{/if}
            </button>
          </div>
        </div>

        {#if depsMode === 'direct'}
          <ul class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-1.5 font-mono text-[13px]">
            {#each report.bazel_deps as d (d.name + d.version)}
              <li class="flex items-baseline gap-1.5 text-fg-mute">
                {#if isNavigableVersion(d.version)}
                  <!--
                    Real-version dep: link to the (m, v) module page so users
                    can follow the closure.
                  -->
                  <a
                    href={moduleVersionHref(d.name, d.version)}
                    class="text-fg hover:text-accent hover:underline"
                  >{d.name}</a>
                  <span class="text-fg-dim">@</span>
                  <a
                    href={moduleVersionHref(d.name, d.version)}
                    class="hover:text-accent hover:underline"
                  >{d.version}</a>
                {:else}
                  <!--
                    Compat-only dep with no real version — no link.
                  -->
                  <span class="text-fg">{d.name}</span>
                  <span class="text-fg-dim">@</span>
                  <span class="text-fg-dim">{displayVersion(d.version)}</span>
                {/if}
              </li>
            {/each}
          </ul>
        {:else}
          <!--
            Transitive view: MVS-walked closure list + the existing
            graph component. ClosureGraph still self-fetches; future
            polish could share the fetched payload via a prop.
          -->
          {#if closureLoading && !closureGraph}
            <div class="flex flex-col gap-1.5">
              {#each Array.from({ length: 6 }) as _, i (i)}
                <div class="skeleton h-5 w-full"></div>
              {/each}
            </div>
          {:else if closureError}
            <div class="rounded-md border border-err/30 bg-err/10 px-3 py-2 text-sm text-err">
              {closureError}
            </div>
          {:else if transitiveDeps.length === 0}
            <p class="text-[12px] text-fg-dim font-mono">no transitive deps</p>
          {:else}
            <ul class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-1.5 font-mono text-[13px]">
              {#each transitiveDeps as n (n.name + '@' + n.version)}
                <li class="flex items-baseline gap-1.5"
                    class:text-fg-dim={n.external}
                    class:text-fg-mute={!n.external}>
                  {#if !n.external && isNavigableVersion(n.version)}
                    <a
                      href={moduleVersionHref(n.name, n.version)}
                      class="text-fg hover:text-accent hover:underline"
                    >{n.name}</a>
                    <span class="text-fg-dim">@</span>
                    <a
                      href={moduleVersionHref(n.name, n.version)}
                      class="hover:text-accent hover:underline"
                    >{n.version}</a>
                  {:else}
                    <span class:text-fg={!n.external}>{n.name}</span>
                    <span class="text-fg-dim">@</span>
                    <span>{n.version}</span>
                    {#if n.external}
                      <span
                        class="text-[10px] uppercase tracking-wide text-fg-dim/70"
                        title="not indexed in this bzlhub"
                      >ext</span>
                    {/if}
                  {/if}
                </li>
              {/each}
            </ul>
            <details class="text-[12px]">
              <summary class="cursor-pointer text-fg-dim hover:text-accent">
                show as graph
              </summary>
              <div class="mt-2">
                <ClosureGraph
                  rootName={report.name}
                  rootVersion={report.version ?? ''}
                />
              </div>
            </details>
          {/if}
        {/if}
      </section>
    {/if}

    {#if report.version && page.params.name && page.params.version}
      <!--
        Reverse closure: "modules in this index that depend on me."
        Lazy-loaded via /api/v1/modules/.../closure/reverse-deps on first
        expand — the server scans the whole corpus, so we don't
        pre-fetch on every page view. Symmetric counterpart to the
        forward closure graph above.
      -->
      <section class="flex flex-col gap-2" data-testid="reverse-deps-section">
        <ReverseDeps module={page.params.name} version={page.params.version} />
      </section>
    {/if}

  </article>
{/if}
