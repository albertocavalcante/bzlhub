<script lang="ts">
  import { page } from '$app/state';
  import { moduleVersion } from '$lib/state/moduleVersion.svelte';

  let { children } = $props();

  // Single source of truth for the (module, version) report — child
  // tabs (Overview today; Documentation/Testing in Phase B) read
  // from the same store so we don't fetch twice. The class store
  // dedupes by (name, version) so navigating between tabs of the
  // same version is a no-op.
  $effect(() => {
    const name = page.params.name;
    const version = page.params.version;
    if (!name || !version) return;
    const ctl = moduleVersion.load(name, version);
    return () => ctl.abort();
  });

  // Active-tab matching is path-prefix based. The "Code" link points
  // at the Go-served /code-nav/ route, which lives outside the
  // SvelteKit app — clicking it leaves the SPA, so we only need the
  // active-state styling for SPA-internal tabs (Overview).
  function activeIf(matcher: (path: string) => boolean): string {
    return matcher(page.url.pathname)
      ? 'border-accent text-fg'
      : 'border-transparent text-fg-mute hover:text-fg';
  }

  // Per-tab path matchers. Overview is the exact base; docs/testing
  // are suffix routes. Code is an outbound link (Go-served) so no
  // matcher is needed — the user has already left the SPA.
  const overviewBase = $derived(
    `/modules/${encodeURIComponent(page.params.name ?? '')}/${encodeURIComponent(page.params.version ?? '')}`,
  );
  function isOverview(path: string): boolean {
    const p = path.replace(/\/$/, '');
    return p === overviewBase;
  }
  function isDocs(path: string): boolean {
    return path.startsWith(overviewBase + '/docs');
  }
  function isTesting(path: string): boolean {
    return path.startsWith(overviewBase + '/testing');
  }
  function isExternal(path: string): boolean {
    return path.startsWith(overviewBase + '/external');
  }
  function isAirgap(path: string): boolean {
    return path.startsWith(overviewBase + '/airgap');
  }
</script>

<div class="flex flex-col gap-6">
  <!--
    Tab nav lives at the layout level so child routes inherit the
    same chrome. Underline-on-active styling matches the rest of
    bzlhub's tab patterns (drift dashboard, etc.).
  -->
  <nav
    class="border-b border-line flex items-center gap-1 text-[13px]"
    data-testid="module-version-tabs"
  >
    <a
      href={overviewBase}
      class="px-3 py-2 border-b-2 transition-colors {activeIf(isOverview)}"
    >
      Overview
    </a>
    <a
      href={`${overviewBase}/docs`}
      class="px-3 py-2 border-b-2 transition-colors {activeIf(isDocs)}"
    >
      Documentation
    </a>
    <a
      href={`${overviewBase}/code-nav/`}
      class="px-3 py-2 border-b-2 border-transparent text-fg-mute hover:text-fg transition-colors"
      title="Browse source — leaves the SPA, served by bzlhub's Go handler"
    >
      Code
    </a>
    <a
      href={`${overviewBase}/testing`}
      class="px-3 py-2 border-b-2 transition-colors {activeIf(isTesting)}"
    >
      Testing
    </a>
    <a
      href={`${overviewBase}/external`}
      class="px-3 py-2 border-b-2 transition-colors {activeIf(isExternal)}"
      title="External URL surface — every URL the module's repo rules + extensions would fetch"
      data-sveltekit-preload-data="hover"
    >
      External
    </a>
    <a
      href={`${overviewBase}/airgap`}
      class="px-3 py-2 border-b-2 transition-colors {activeIf(isAirgap)}"
      title="Airgap surface — every URL the full transitive bazel_deps closure would fetch"
      data-sveltekit-preload-data="hover"
    >
      Airgap
    </a>
  </nav>

  {@render children?.()}
</div>
