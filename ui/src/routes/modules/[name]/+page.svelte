<script lang="ts">
  // /modules/<name> — indexed-versions listing.
  //
  // Built specifically to close the friendly-404's "see indexed versions
  // of X →" link (internal/server/codenav.go's not-indexed template).
  // Without this page that link 404'd — exactly the dead-end the
  // friendly template was supposed to *prevent*.
  //
  // The page is also a useful direct-access surface: pasting the
  // module name without a version no longer falls through to the SPA
  // catchall + browser-rendered "Not Found".

  import { page } from '$app/state';
  import { listVersions, type VersionEntry } from '$api/client';
  import { formatBytes, relativeTime } from '$lib/time';

  let entries = $state<VersionEntry[] | null>(null);
  let loading = $state(true);
  let error = $state<string | null>(null);

  $effect(() => {
    const name = page.params.name;
    if (!name) return;

    const ctl = new AbortController();
    loading = true;
    error = null;
    entries = null;

    listVersions(name, ctl.signal)
      .then((r) => {
        if (ctl.signal.aborted) return;
        entries = r.entries ?? [];
      })
      .catch((e) => {
        if (ctl.signal.aborted) return;
        error = e instanceof Error ? e.message : String(e);
      })
      .finally(() => {
        if (!ctl.signal.aborted) loading = false;
      });

    return () => ctl.abort();
  });
</script>

<svelte:head>
  <title>{page.params.name} — bzlhub</title>
</svelte:head>

<div class="flex flex-col gap-6">
  <nav class="text-[12px] font-mono text-fg-dim">
    <a href="/" class="hover:text-accent">bzlhub</a> /
    <span class="text-fg-mute">modules</span> /
    <span class="text-fg">{page.params.name}</span>
  </nav>

  <header class="flex flex-col gap-2 pb-3 border-b border-line">
    <h1 class="font-mono text-2xl text-fg tracking-tight">{page.params.name}</h1>
    <p class="text-[12px] text-fg-mute">indexed versions in this bzlhub</p>
  </header>

  {#if loading}
    <div class="flex flex-col gap-2">
      {#each Array.from({ length: 5 }) as _, i (i)}
        <div class="skeleton h-10 w-full"></div>
      {/each}
    </div>
  {:else if error}
    <div class="rounded-md border border-err/30 bg-err/10 px-3 py-2 text-sm text-err">
      {error}
    </div>
  {:else if entries === null || entries.length === 0}
    <div class="text-center py-16 text-fg-dim">
      <p class="font-mono text-lg">{page.params.name}</p>
      <p class="mt-2">no versions of this module are indexed.</p>
      <p class="text-[12px] mt-4">
        try <a href="/?q={encodeURIComponent(page.params.name ?? '')}" class="text-accent hover:underline">searching</a>
        for a different name, or ingest a version via the bzlhub CLI / <code>/api/v1/actions/bump</code>.
      </p>
    </div>
  {:else}
    <p class="text-[11px] text-fg-mute font-mono">{entries.length} versions · newest first</p>
    <ul class="flex flex-col gap-1.5">
      {#each entries as e (e.version)}
        <li
          class="flex items-baseline gap-3 px-3 py-2 -mx-3 rounded-md border border-transparent hover:bg-bg-elev hover:border-line"
        >
          <a
            href={e.href}
            class="font-mono text-[13px] text-fg hover:text-accent"
          >
            {e.version}
          </a>
          {#if e.cadence_label}
            <!--
              Cadence superscript — gap vs the next-older row's
              ingest timestamp. Useful for "how often does this
              module ship?" Tooltip clarifies that this is bzlhub's
              ingest cadence, not upstream publish cadence (those
              can diverge if bzlhub is behind upstream).
            -->
            <sup
              class="text-[10px] text-fg-dim font-mono"
              title="time between this row and the next-older row's ingest timestamps; not necessarily upstream publish cadence"
            >
              {e.cadence_label}
            </sup>
          {/if}
          <span class="flex-1"></span>
          {#if e.is_stub}
            <span
              class="text-[10px] uppercase tracking-wide text-fg-dim font-mono"
              title="placeholder version — MODULE.bazel had no real version declared"
            >
              stub
            </span>
          {/if}
          {#if e.yanked_reason}
            <span
              class="text-[10px] uppercase tracking-wide text-err font-mono px-1.5 py-0.5 rounded border border-err/40 bg-err/10"
              title={`yanked upstream: ${e.yanked_reason}`}
            >
              yanked
            </span>
          {/if}
          {#if e.compat_level}
            <span
              class="text-[10px] text-fg-dim font-mono"
              title={`compatibility_level ${e.compat_level}`}
            >
              L{e.compat_level}
            </span>
          {/if}
          {#if e.tarball_size}
            <span
              class="text-[10px] text-fg-dim font-mono"
              title={`compressed tarball: ${e.tarball_size.toLocaleString()} bytes`}
            >
              {formatBytes(e.tarball_size)}
            </span>
          {/if}
          {#if e.pin_count && e.pin_count > 0}
            <!--
              Adoption chip: how many consumers pin exactly this
              (module, version). The dominant version (pin_pct >= 50)
              gets accent treatment so the eye lands on the
              majority-pinned row without scanning every value.
            -->
            <span
              class="text-[10px] font-mono px-1.5 py-0.5 rounded border {e.pin_pct && e.pin_pct >= 50 ? 'text-accent border-accent/40 bg-accent/10' : 'text-fg-dim border-line'}"
              title={e.pin_pct ? `${e.pin_count} consumer${e.pin_count === 1 ? '' : 's'} pin this version (${e.pin_pct}% of pins across all versions)` : `${e.pin_count} consumer${e.pin_count === 1 ? '' : 's'} pin this version`}
            >
              {e.pin_count} pin{e.pin_count === 1 ? '' : 's'}
            </span>
          {/if}
          {#if e.ingested_at}
            <span
              class="text-[11px] font-mono text-fg-dim"
              title="ingested at {e.ingested_at}"
            >
              {relativeTime(e.ingested_at)}
            </span>
          {/if}
          {#if e.diff_href}
            <a
              href={e.diff_href}
              class="text-[11px] font-medium text-fg-mute hover:text-accent px-2 py-1 rounded transition-colors"
              title="Diff {e.diff_from_version} → {e.version}"
            >
              diff
            </a>
          {/if}
          <a
            href={e.code_nav_href}
            class="text-[11px] font-medium text-fg-mute hover:text-accent px-2 py-1 rounded transition-colors"
            title="Browse source for {page.params.name}@{e.version}"
          >
            Code →
          </a>
        </li>
      {/each}
    </ul>
  {/if}
</div>
