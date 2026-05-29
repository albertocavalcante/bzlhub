<!--
  ModuleNotFound — the friendly-404 surface for /modules/[name]/[version]
  when the requested coordinate isn't in canopy's index.

  On mount it preflights two things in parallel:
    1. /api/v1/system/bcr-probe — does the configured upstream registry have
       this exact (m, v)? Or the module at any version?
    2. /api/v1/system/features  — is ingest write enabled at all?

  The render branches on those answers so the user always sees a
  concrete next step:
    - BCR has the version + writes enabled → "Ingest from BCR" button
    - BCR has the module but not this version → suggest latest_version
    - BCR doesn't have the module at all → suggest browsing canopy's
      own index, with a BCR-search link as a last resort
    - Upstream registry unreachable → "registry temporarily
      unreachable" with retry

  All copy avoids raw HTTP error strings — the user sees explanations,
  not stack traces. Raw errors only surface as collapsible details.
-->
<script lang="ts">
  import { goto } from '$app/navigation';
  import {
    BcrUnreachableError,
    bumpModule,
    BumpUpstreamError,
    getBcrProbe,
    getFeatures,
    ingestRecursive,
    IngestDisabledError,
    IngestRateLimitedError,
    type BcrProbeResult,
    type FeatureSnapshot,
  } from '$api/client';
  import { moduleVersionHref } from '$lib/links';

  interface Props {
    name: string;
    version: string;
    /** Called after a successful ingest so the parent can swap the 404
     *  view for the real ModuleReport without a full navigation. */
    onresolved?: () => void;
  }

  let { name, version, onresolved }: Props = $props();

  // ── Preflight state ───────────────────────────────────────────────────
  type ProbeState =
    | { kind: 'probing' }
    | { kind: 'ready'; probe: BcrProbeResult; features: FeatureSnapshot }
    | { kind: 'upstream-error'; message: string }
    | { kind: 'transport-error'; message: string };

  let probeState = $state<ProbeState>({ kind: 'probing' });

  // Re-probe whenever (name, version) changes. The effect cleans up
  // its own AbortController so rapid nav doesn't cause stale writes.
  $effect(() => {
    const ctl = new AbortController();
    probeState = { kind: 'probing' };
    (async () => {
      try {
        const [probe, features] = await Promise.all([
          getBcrProbe(name, version, ctl.signal),
          getFeatures(ctl.signal),
        ]);
        if (ctl.signal.aborted) return;
        probeState = { kind: 'ready', probe, features };
      } catch (e) {
        if (ctl.signal.aborted) return;
        if (e instanceof BcrUnreachableError) {
          probeState = { kind: 'upstream-error', message: e.message };
        } else {
          probeState = {
            kind: 'transport-error',
            message: e instanceof Error ? e.message : String(e),
          };
        }
      }
    })();
    return () => ctl.abort();
  });

  // ── Ingest state machine ─────────────────────────────────────────────
  // Bump = synchronous fetch + mirror + assay + SCIP + insert for THIS
  // coordinate. One round-trip, and on success the module is fully
  // queryable via /api/v1/modules — that's what flips the page from 404 to
  // the real report. The recursive walker is fired in the background
  // afterward (best-effort) so the bazel_dep closure lands in the
  // mirror for future cross-module navigation.
  type IngestState =
    | { kind: 'idle' }
    | { kind: 'running' }
    | { kind: 'closure-running' } // root done, closure walking in background
    | { kind: 'done' }
    | { kind: 'error'; message: string; retryAfter: number };

  let ingestState = $state<IngestState>({ kind: 'idle' });

  async function startIngest() {
    ingestState = { kind: 'running' };
    try {
      // Step 1: Bump the root so it becomes queryable. This is the
      // step that actually fixes the 404 — without it, the recursive
      // walker only writes the mirror and leaves canopy's SQLite
      // index untouched, so /api/v1/modules keeps 404-ing.
      await bumpModule({ module: name, version });

      // The page can swap to the real report now. We notify before
      // kicking off the closure so the user isn't waiting on
      // background work to see their result.
      onresolved?.();
      ingestState = { kind: 'closure-running' };

      // Step 2: Best-effort closure walk for cross-module navigation.
      // The walker only mirrors (doesn't extract assay reports), but
      // having tarballs on disk means a follow-up `canopy ingest` can
      // hydrate dep coords later without re-fetching from BCR. We
      // don't await this — the user already has what they came for.
      ingestRecursive({ module: name, version })
        .catch(() => {
          // Background failure is fine — the root is already indexed.
          // We don't surface this to the user because they're
          // already looking at the resolved module page.
        })
        .finally(() => {
          ingestState = { kind: 'done' };
        });
    } catch (e) {
      if (e instanceof IngestDisabledError) {
        ingestState = {
          kind: 'error',
          message:
            'Ingest writes are disabled on this canopy. The operator can flip CANOPY_INGEST_WRITE_ENABLED.',
          retryAfter: 0,
        };
      } else if (e instanceof IngestRateLimitedError) {
        ingestState = {
          kind: 'error',
          message: `Rate-limited. Try again in ${e.retryAfterSec}s.`,
          retryAfter: e.retryAfterSec,
        };
      } else if (e instanceof BumpUpstreamError) {
        ingestState = { kind: 'error', message: e.message, retryAfter: 0 };
      } else {
        ingestState = {
          kind: 'error',
          message: e instanceof Error ? e.message : String(e),
          retryAfter: 0,
        };
      }
    }
  }

  // Cap suggested-versions list so a module with many tagged versions
  // (rules_python has ~80) doesn't dominate the page.
  const VISIBLE_VERSIONS = 8;
  function suggestedVersions(probe: BcrProbeResult): string[] {
    if (!probe.versions_available) return [];
    // Newest first (BCR-canonical order is oldest first; reverse for
    // display). Cap with a "+N more" tail.
    return probe.versions_available.slice().reverse().slice(0, VISIBLE_VERSIONS);
  }

  function gotoLatest(probe: BcrProbeResult) {
    if (!probe.latest_version) return;
    void goto(moduleVersionHref(probe.module, probe.latest_version));
  }
</script>

<div class="text-center py-24 flex flex-col items-center gap-4 max-w-xl mx-auto">
  <p class="font-mono text-lg text-fg-dim">
    <span class="text-fg">{name}</span><span class="text-fg-dim">@</span><span class="text-fg">{version}</span>
  </p>
  <p class="text-fg-dim text-sm">not in this canopy index</p>

  {#if probeState.kind === 'probing'}
    <div class="flex items-center gap-2 text-xs text-fg-dim" data-testid="probe-loading">
      <span class="animate-pulse">●</span>
      checking upstream registry…
    </div>
  {:else if probeState.kind === 'upstream-error'}
    <!--
      Probe itself failed — we can't tell the user whether the
      coordinate exists. Lead with the human explanation and tuck
      the raw error in <details> for operators.
    -->
    <div class="rounded-md border border-warn/30 bg-warn/10 px-4 py-3 text-sm text-fg w-full">
      <p class="font-medium">Upstream registry is temporarily unreachable.</p>
      <p class="text-xs text-fg-dim mt-1">
        canopy can't confirm whether this coordinate exists. Try again in a moment.
      </p>
      <details class="text-xs text-fg-dim mt-2">
        <summary class="cursor-pointer hover:text-fg-mute">details</summary>
        <pre class="font-mono text-[11px] mt-1 whitespace-pre-wrap break-all">{probeState.message}</pre>
      </details>
    </div>
  {:else if probeState.kind === 'transport-error'}
    <div class="rounded-md border border-err/30 bg-err/10 px-4 py-3 text-sm text-err w-full">
      <p>Couldn't reach canopy's API to check this coordinate.</p>
      <details class="text-xs text-fg-dim mt-2">
        <summary class="cursor-pointer hover:text-err">details</summary>
        <pre class="font-mono text-[11px] mt-1 whitespace-pre-wrap break-all">{probeState.message}</pre>
      </details>
    </div>
  {:else if probeState.kind === 'ready'}
    {@const probe = probeState.probe}
    {@const features = probeState.features}

    {#if probe.version_exists}
      <!-- GREEN PATH — BCR has this exact coordinate. -->
      <div class="text-sm text-fg-dim">
        ✓ available on the upstream registry
      </div>
      {#if features.ingest_write_enabled && ingestState.kind === 'idle'}
        <button
          type="button"
          onclick={startIngest}
          class="mt-1 inline-flex items-center gap-2 rounded-md border border-accent/40 bg-accent/10 px-4 py-2 text-sm font-mono text-accent hover:bg-accent/20 hover:border-accent transition-colors"
          data-testid="ingest-button"
        >
          ↓ Ingest from BCR
        </button>
        <p class="text-xs text-fg-dim">
          will fetch this module and its bazel_dep closure from the configured upstream
        </p>
      {:else if !features.ingest_write_enabled}
        <p class="text-xs text-fg-dim italic">
          this canopy doesn't allow web-driven ingest — use the canopy CLI
        </p>
      {/if}
    {:else if probe.module_exists}
      <!-- AMBER PATH — module is on BCR but the version isn't. -->
      <div class="rounded-md border border-warn/30 bg-warn/10 px-4 py-3 text-sm text-fg w-full text-left">
        <p>
          <span class="font-mono text-fg">{name}</span> is on the upstream
          registry, but <span class="font-mono">{version}</span> isn't a published version.
        </p>
        {#if probe.latest_version}
          <div class="mt-3 flex flex-wrap items-baseline gap-2">
            <button
              type="button"
              onclick={() => gotoLatest(probe)}
              class="inline-flex items-center gap-1.5 rounded-md border border-accent/40 bg-accent/10 px-3 py-1.5 text-xs font-mono text-accent hover:bg-accent/20"
              data-testid="goto-latest"
            >
              → go to <strong>{probe.latest_version}</strong> (latest)
            </button>
          </div>
        {/if}
        {#if probe.versions_available && probe.versions_available.length > 0}
          <p class="text-xs text-fg-dim mt-3">also published:</p>
          <ul class="flex flex-wrap gap-1.5 mt-1">
            {#each suggestedVersions(probe) as v (v)}
              <li>
                <a
                  href={moduleVersionHref(probe.module, v)}
                  class="inline-block font-mono text-[11px] text-fg-mute hover:text-accent border border-line rounded px-1.5 py-0.5"
                >
                  {v}
                </a>
              </li>
            {/each}
            {#if probe.versions_available.length > VISIBLE_VERSIONS}
              <li class="text-[11px] text-fg-dim self-center">
                +{probe.versions_available.length - VISIBLE_VERSIONS} more
              </li>
            {/if}
          </ul>
        {/if}
      </div>
    {:else}
      <!-- RED PATH — BCR doesn't have this module at all (typo, vendor split, private). -->
      <div class="rounded-md border border-err/30 bg-err/10 px-4 py-3 text-sm text-fg w-full text-left">
        <p>No module named <span class="font-mono text-fg">{name}</span> on the upstream registry.</p>
        <p class="text-xs text-fg-dim mt-2">
          Common causes: vendor-rename (e.g. <code>rules_js</code> is published as
          <code class="text-fg">aspect_rules_js</code>), private fork, or typo.
        </p>
        <div class="flex gap-3 text-xs mt-3">
          <a href="/modules" class="underline hover:text-fg" data-testid="browse-modules">
            browse modules canopy already indexes
          </a>
          <a
            href={`https://registry.bazel.build/search?q=${encodeURIComponent(name)}`}
            target="_blank"
            rel="noopener noreferrer"
            class="underline hover:text-fg"
          >search BCR for "{name}" ↗</a>
        </div>
      </div>
    {/if}

    <!-- Ingest progress / outcome lives below the probe-driven copy
         so the user sees both the why and the what-happens. -->
    {#if ingestState.kind === 'running'}
      <div class="flex flex-col items-center gap-2 text-sm" data-testid="ingest-progress">
        <div class="flex items-center gap-2">
          <span class="animate-pulse text-accent">●</span>
          <span class="font-mono">fetching tarball + extracting…</span>
        </div>
        <span class="text-xs text-fg-dim">this usually takes a few seconds</span>
      </div>
    {:else if ingestState.kind === 'closure-running'}
      <!--
        Bump succeeded → the parent already swapped to the real
        report; this branch only renders briefly during the redirect.
      -->
      <p class="text-sm text-accent" data-testid="ingest-running-closure">
        ✓ module indexed — fetching closure for cross-module navigation…
      </p>
    {:else if ingestState.kind === 'done'}
      <p class="text-sm text-fg-mute" data-testid="ingest-done">
        ✓ module + closure indexed
      </p>
    {:else if ingestState.kind === 'error'}
      <div class="rounded-md border border-err/30 bg-err/10 px-4 py-3 text-sm w-full text-left">
        <p class="text-err">Ingest failed.</p>
        <details class="text-xs text-fg-dim mt-2">
          <summary class="cursor-pointer hover:text-err">details</summary>
          <pre class="font-mono text-[11px] mt-1 whitespace-pre-wrap break-all">{ingestState.message}</pre>
        </details>
        {#if ingestState.retryAfter === 0}
          <button
            type="button"
            onclick={startIngest}
            class="text-xs underline hover:text-fg mt-2"
          >
            retry
          </button>
        {/if}
      </div>
    {/if}

    <p class="text-[11px] text-fg-dim/70 mt-3">
      checked against <code class="font-mono">{probe.registry_url}</code>
    </p>
  {/if}

  <p class="text-xs mt-4 text-fg-dim/70">
    or run <kbd>canopy ingest &lt;module-dir&gt;</kbd> with the source on disk
  </p>
</div>
