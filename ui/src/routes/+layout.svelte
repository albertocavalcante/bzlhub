<script lang="ts">
  import { paths } from '$lib/api/paths';
  import { getFeatures, getUpstreams, type FeatureSnapshot, type UpstreamsResponse } from '$lib/api/client';
  import '../app.css';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { onMount, onDestroy } from 'svelte';

  let { children } = $props();

  // Help-overlay open state. Toggled by `?` key. Closed by Esc /
  // backdrop click. UI-local, not persisted.
  let helpOpen = $state(false);

  // Build metadata fetched once on mount from /api/v1/system/version. The server
  // returns {"version","commit","built_at"} populated via -ldflags. We
  // show only the short version in the footer text; commit + build
  // timestamp surface in the title (hover) attribute.
  //
  // Initial value is `…` rather than a fake version like "v0" — a
  // stale-looking "v0" gives users a false impression about what's
  // deployed while the fetch is in flight. The ellipsis is visually
  // clearly a placeholder.
  let buildVersion = $state('…');
  let buildTitle = $state('');
  let features = $state<FeatureSnapshot | null>(null);

  // Plan 16 F3 (UI follow-up): federation reachability dot. Polls
  // /api/v1/upstreams every 60s — matches the cascade's own probe
  // cadence so we never show data more stale than the server itself
  // has. Hidden entirely when federation isn't configured (empty
  // upstreams array).
  let upstreams = $state<UpstreamsResponse | null>(null);
  let fedPollHandle: ReturnType<typeof setInterval> | undefined;

  // Derived dot color: green = all reachable, amber = mixed, red =
  // all unreachable. Null when federation isn't configured.
  const fedStatus = $derived.by<'all-up' | 'mixed' | 'all-down' | null>(() => {
    if (!upstreams || upstreams.upstreams.length === 0) return null;
    const total = upstreams.upstreams.length;
    const up = upstreams.upstreams.filter((u) => u.reachable).length;
    if (up === total) return 'all-up';
    if (up === 0) return 'all-down';
    return 'mixed';
  });

  const fedColor = $derived(
    fedStatus === 'all-up'
      ? 'var(--color-ok)'
      : fedStatus === 'mixed'
        ? 'var(--color-warn)'
        : 'var(--color-err)',
  );

  const fedTitle = $derived.by(() => {
    if (!upstreams || upstreams.upstreams.length === 0) return '';
    const lines = upstreams.upstreams.map((u) => {
      const status = u.reachable ? 'up  ' : 'down';
      const lat = u.reachable ? ` (${u.last_probe_latency_ms}ms)` : '';
      const err = u.last_probe_error_msg ? ` — ${u.last_probe_error_msg}` : '';
      return `[${status}] ${u.url}${lat}${err}`;
    });
    return `Federation upstreams:\n${lines.join('\n')}`;
  });

  const demoBanner = $derived(features?.demo_mode ? (features.demo_banner?.trim() || 'demo instance') : '');

  async function refreshFeatures() {
    try {
      features = await getFeatures();
    } catch {
      features = null;
    }
  }

  async function refreshUpstreams() {
    try {
      upstreams = await getUpstreams();
    } catch {
      // Network failure → hide the dot rather than show stale state.
      upstreams = null;
    }
  }

  onMount(async () => {
    try {
      const res = await fetch(paths.system.version(), { headers: { accept: 'application/json' } });
      if (!res.ok) {
        buildVersion = '?';
        return;
      }
      const v = (await res.json()) as { version?: string; commit?: string; built_at?: string };
      if (v.version) buildVersion = v.version;
      const parts: string[] = [];
      if (v.commit) parts.push(v.commit);
      if (v.built_at && v.built_at !== 'unknown') parts.push('built ' + v.built_at);
      buildTitle = parts.join(' · ');
    } catch {
      // Network error / non-JSON response: surface `?` so it's clear
      // we tried and failed (a stale "v0" would look like a real version).
      buildVersion = '?';
    }
    // Initial federation snapshot + 60s polling. The server's
    // background probe runs at the same cadence so polling more
    // often would just show repeated state.
    await refreshFeatures();
    await refreshUpstreams();
    fedPollHandle = setInterval(() => void refreshUpstreams(), 60_000);
  });

  onDestroy(() => {
    if (fedPollHandle !== undefined) clearInterval(fedPollHandle);
  });

  // Global keyboard handler. Shortcuts only fire when focus is
  // outside an input/textarea/contenteditable.
  //
  //   /        focus search (also Cmd/Ctrl-K)
  //   m        modules listing
  //   d        drift dashboard
  //   h        history page
  //   ?        toggle help overlay
  //   Esc      close help overlay
  //
  // Focus-search dispatches a custom event so the SearchBar can
  // listen without prop drilling.
  function onKeydown(e: KeyboardEvent) {
    const target = e.target as HTMLElement | null;
    const inField =
      target &&
      (target.tagName === 'INPUT' ||
        target.tagName === 'TEXTAREA' ||
        target.isContentEditable);

    // Esc always closes the help overlay (even while inField — e.g.,
    // user opened help, then started typing, wants out).
    if (e.key === 'Escape' && helpOpen) {
      e.preventDefault();
      helpOpen = false;
      return;
    }
    if (inField) return;

    if (e.key === '/' || (e.key === 'k' && (e.metaKey || e.ctrlKey))) {
      e.preventDefault();
      window.dispatchEvent(new CustomEvent('canopy:focus-search'));
      return;
    }
    if (e.key === '?') {
      e.preventDefault();
      helpOpen = !helpOpen;
      return;
    }
    if (e.metaKey || e.ctrlKey || e.altKey) return;
    if (e.key === 'm') {
      e.preventDefault();
      void goto('/modules');
    } else if (e.key === 'd') {
      e.preventDefault();
      void goto('/drift');
    } else if (e.key === 'h') {
      e.preventDefault();
      void goto('/history');
    }
  }
</script>

<svelte:window on:keydown={onKeydown} />

<div class="min-h-screen flex flex-col">
  <header
    class="sticky top-0 z-20 border-b border-line bg-bg/80 backdrop-blur-md"
  >
    <div class="max-w-[1200px] mx-auto px-6 h-12 flex items-center gap-6">
      <a
        href="/"
        class="font-semibold tracking-tight flex items-center gap-2 hover:text-accent transition-colors"
      >
        <span class="inline-block w-2 h-2 rounded-full bg-accent"></span>
        bzlhub
      </a>
      <nav class="flex items-center gap-4 text-sm text-fg-mute">
        <a
          href="/"
          class="hover:text-fg transition-colors"
          class:text-fg={page.url.pathname === '/'}>search</a
        >
        <a
          href="/modules"
          class="hover:text-fg transition-colors"
          class:text-fg={page.url.pathname.startsWith('/modules')}>modules</a
        >
        <a
          href="/drift"
          class="hover:text-fg transition-colors"
          class:text-fg={page.url.pathname === '/drift'}>drift</a
        >
        <a
          href="/history"
          class="hover:text-fg transition-colors"
          class:text-fg={page.url.pathname === '/history'}>history</a
        >
      </nav>
      <div class="ml-auto text-xs text-fg-dim flex items-center gap-3">
        <span class="flex items-center gap-1.5">
          <kbd>/</kbd>
          <span>to search</span>
        </span>
        <button
          type="button"
          onclick={() => (helpOpen = true)}
          class="flex items-center gap-1.5 hover:text-fg transition-colors cursor-pointer"
          title="keyboard shortcuts (or press ?)"
          aria-label="show keyboard shortcuts"
        >
          <kbd>?</kbd>
          <span>shortcuts</span>
        </button>
      </div>
    </div>
  </header>

  <main class="flex-1 max-w-[1200px] w-full mx-auto px-6 py-8">
    {@render children?.()}
  </main>

  {#if helpOpen}
    <!--
      Help overlay. Single click handler on the backdrop that filters
      by `event.target === event.currentTarget` so clicks inside the
      sheet don't dismiss — replaces the previous stopPropagation on
      a nested <div> with click handler (which svelte-check flagged
      as a11y_no_noninteractive_element_interactions). Keyboard
      parity: Esc closes via the global onKeydown handler at line 67;
      Enter/Space on the focused dialog also close so screen-reader
      users can dismiss without ever leaving the dialog focus ring.
    -->
    <div
      class="fixed inset-0 z-50 bg-black/60 flex items-center justify-center px-4"
      role="dialog"
      aria-modal="true"
      aria-label="keyboard shortcuts"
      tabindex="-1"
      onclick={(e) => {
        if (e.target === e.currentTarget) helpOpen = false;
      }}
      onkeydown={(e) => {
        // Esc is handled by the global window handler. Enter/Space
        // on the backdrop (when focused) closes too so keyboard
        // users have a dismissal mid-dialog when focus is on the
        // overlay rather than on the close button.
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault();
          helpOpen = false;
        }
      }}
    >
      <div
        class="bg-bg-elev border border-line rounded-md shadow-xl max-w-md w-full p-5 flex flex-col gap-3"
        role="document"
      >
        <div class="flex items-baseline justify-between">
          <h2 class="font-mono text-sm text-fg">keyboard shortcuts</h2>
          <button
            type="button"
            class="text-xs text-fg-dim hover:text-fg cursor-pointer"
            onclick={() => (helpOpen = false)}
            aria-label="close"
          >
            esc
          </button>
        </div>
        <dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-[12px] font-mono">
          <dt><kbd>/</kbd></dt>
          <dd class="text-fg-mute">focus search</dd>
          <dt><kbd>⌘</kbd><kbd>K</kbd></dt>
          <dd class="text-fg-mute">focus search</dd>
          <dt><kbd>m</kbd></dt>
          <dd class="text-fg-mute">modules</dd>
          <dt><kbd>d</kbd></dt>
          <dd class="text-fg-mute">drift</dd>
          <dt><kbd>h</kbd></dt>
          <dd class="text-fg-mute">history</dd>
          <dt><kbd>?</kbd></dt>
          <dd class="text-fg-mute">this help</dd>
          <dt><kbd>esc</kbd></dt>
          <dd class="text-fg-mute">close help</dd>
        </dl>
      </div>
    </div>
  {/if}

  <footer class="border-t border-line/70">
    <div
      class="max-w-[1200px] mx-auto px-6 h-12 flex items-center justify-between gap-4 overflow-hidden text-xs text-fg-dim"
    >
      <span class="flex min-w-0 items-center gap-2 overflow-hidden">
        <span class="truncate">bzlhub · self-hosted Bazel registry with introspection</span>
        {#if demoBanner}
          <span
            class="shrink-0 rounded border border-accent/40 bg-accent/10 px-1.5 py-0.5 text-[10px] font-medium uppercase text-accent"
            title={demoBanner}
          >
            {demoBanner}
          </span>
        {/if}
      </span>
      <span class="flex shrink-0 items-center gap-3">
        <a
          href="https://github.com/albertocavalcante/bzlhub"
          target="_blank"
          rel="noopener noreferrer"
          class="hover:text-fg transition-colors"
          title="source on GitHub (MIT)"
        >
          github
        </a>
        <a
          href="https://github.com/albertocavalcante/bzlhub/blob/main/LICENSE"
          target="_blank"
          rel="noopener noreferrer"
          class="hover:text-fg transition-colors"
          title="MIT license"
        >
          MIT
        </a>
        {#if fedStatus !== null}
          <!--
            Federation reachability dot. Plan 16 F3 follow-up.
            Color encodes aggregate state; tooltip lists each
            upstream with reachable/down + latency + last error.
            Hidden entirely when canopy is non-federated.
          -->
          <span
            class="inline-block w-2 h-2 rounded-full"
            style:background={fedColor}
            title={fedTitle}
            aria-label="federation upstream health"
          ></span>
        {/if}
        <span title={buildTitle}>{buildVersion}</span>
      </span>
    </div>
  </footer>
</div>
