<!--
  ClosureGraph — Mermaid visualization of a module's direct
  bazel_dep closure.

  V1 scope: ONE-level direct deps only. Each declared bazel_dep
  shows up as a node connected to the root. Doesn't yet walk the
  full transitive closure (that requires resolving each dep's
  metadata, which is a separate fetch + a layout-density concern —
  full trees over big closures get unreadable fast).

  Why client-side rendering: Mermaid takes a text spec and produces
  SVG in the browser. Lazy-loaded via dynamic import so the
  ~700KB lib only ships on the module page that actually uses it
  (and only on the first user interaction with this section).

  Why visual at all: text dep lists scale poorly past ~10 entries
  — a reader scrolls past them. A grid renders the same info but
  hides the parent-child relationship; the graph is the lossless
  view that survives N=30+ deps and stays legible because the
  layout engine pushes leaves to the edges.
-->
<script lang="ts">
  import { paths } from '$lib/api/paths';
  import { base } from '$app/paths';
  import { onMount } from 'svelte';

  interface ClosureNode {
    name: string;
    version: string;
    external?: boolean;
  }
  interface ClosureEdge {
    from: string;
    to: string;
  }
  interface ClosureGraphResponse {
    root: string;
    nodes: ClosureNode[];
    edges: ClosureEdge[];
    max_depth_reached?: boolean;
  }

  interface Props {
    /** Root module name. Used for the API call + axis labeling. */
    rootName: string;
    /** Root module version. Same. */
    rootVersion: string;
  }

  let { rootName, rootVersion }: Props = $props();

  // Track the rendered container so the mount effect can target it
  // exactly — mermaid.run() walks all `.mermaid` blocks by default,
  // which would re-render other graphs on the same page.
  let container: HTMLDivElement | undefined = $state();
  let renderError = $state<string | null>(null);
  let graph = $state<ClosureGraphResponse | null>(null);
  let fetchError = $state<string | null>(null);
  let isEmpty = $state(false);

  // Ingest-missing state. Tracks across MULTIPLE passes — each
  // pass bumps the visible-but-not-yet-indexed externals, which
  // reveals their OWN transitive deps, which become the next pass's
  // targets. Loop continues until either:
  //   - externals hits 0 (closure fully indexed)
  //   - a pass bumps nothing (stable; usually means everything left
  //     failed to bump — broken upstream coordinates we can't fix)
  //   - max-iterations cap fires (safety against pathological
  //     closures or upstream errors that masquerade as new work)
  const ingestMaxPasses = 5;
  let ingesting = $state(false);
  let ingestPass = $state(0);
  let ingestProgress = $state(0);
  let ingestResult = $state<{ bumped: number; failed: number } | null>(null);

  async function ingestMissing() {
    if (!graph || ingesting) return;
    ingesting = true;
    ingestPass = 0;
    ingestProgress = 0;
    ingestResult = null;

    // Subscribe to per-module bump progress across ALL passes. Each
    // successful Bump emits a module_indexed event; counter ticks
    // on every event during the run. Reset between passes would be
    // possible but the cumulative counter reads more naturally
    // — "ingesting… K bumps so far" — for a multi-pass operation.
    const es = new EventSource(paths.activity.events());
    es.addEventListener('module_indexed', () => {
      ingestProgress += 1;
    });

    const totals = { bumped: 0, failed: 0 };
    try {
      for (let pass = 1; pass <= ingestMaxPasses; pass++) {
        ingestPass = pass;
        const url = `${base}${paths.actions.ingestMissing(rootName, rootVersion)}`;
        const res = await fetch(url, { method: 'POST' });
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        const passResult = (await res.json()) as { bumped: number; failed: number };
        totals.bumped += passResult.bumped;
        totals.failed += passResult.failed;
        // Stable state: a pass bumped nothing. Usually means the
        // remaining externals all failed (broken upstream paths),
        // OR the closure is genuinely complete.
        if (passResult.bumped === 0) break;
        // Refetch + render between passes so the user watches the
        // graph evolve. If this pass exposed no new externals
        // (closure now stable), the next iteration's POST will
        // return bumped=0 and we'll bail out cleanly.
        await refreshGraph();
        if (graph && graph.nodes.every((n) => !n.external)) break;
      }
      ingestResult = totals;
      // Final refresh so the displayed graph matches the final state
      // (including any leftover externals that couldn't be bumped).
      await refreshGraph();
    } catch (e) {
      fetchError = e instanceof Error ? e.message : String(e);
    } finally {
      es.close();
      ingesting = false;
    }
  }

  async function refreshGraph() {
    try {
      const url = `${base}${paths.closure.graph(rootName, rootVersion)}`;
      const res = await fetch(url);
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      graph = (await res.json()) as ClosureGraphResponse;
      // Rerender mermaid with the new spec.
      if (graph.nodes.length > 1 && container) {
        const { default: mermaid } = await import('mermaid');
        const { svg, bindFunctions } = await mermaid.render(
          `closure-${rootName}-${Date.now()}`,
          buildSpec(graph),
        );
        container.innerHTML = svg;
        bindFunctions?.(container);
      }
    } catch (e) {
      renderError = e instanceof Error ? e.message : String(e);
    }
  }

  // Build the Mermaid spec from a server-fetched graph. External
  // nodes get a separate classDef so they render in a muted style;
  // root gets the accent color. Node ids are sanitized from
  // "name@version" so dashes / underscores / periods work.
  function buildSpec(g: ClosureGraphResponse): string {
    const lines: string[] = ['graph TD'];
    for (const n of g.nodes) {
      const id = sanitize(`${n.name}@${n.version}`);
      const label = `${n.name}<br/>${n.version}`;
      const cls = `${n.name}@${n.version}` === g.root
        ? ':::root'
        : n.external
          ? ':::external'
          : '';
      lines.push(`  ${id}["${escapeLabel(label)}"]${cls}`);
    }
    for (const e of g.edges) {
      lines.push(`  ${sanitize(e.from)} --> ${sanitize(e.to)}`);
    }
    lines.push('classDef root fill:#1a4fbf,stroke:#1a4fbf,color:#fff');
    lines.push('classDef external fill:#f5f5f5,stroke:#bbb,color:#888,stroke-dasharray:4');
    return lines.join('\n');
  }

  function sanitize(key: string): string {
    return 'n_' + key.replace(/[^a-zA-Z0-9]/g, '_');
  }

  function escapeLabel(s: string): string {
    return s.replace(/"/g, '&quot;');
  }

  // Two-phase render on mount: fetch the full graph spec from the
  // server (cheap — single store walk), then lazy-load mermaid and
  // render. Both phases run sequentially because mermaid only needs
  // to load if there's something to render.
  onMount(async () => {
    if (!container) return;
    // Phase 1: fetch.
    try {
      const url = `${base}${paths.closure.graph(rootName, rootVersion)}`;
      const res = await fetch(url);
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const data = (await res.json()) as ClosureGraphResponse;
      graph = data;
      if (data.nodes.length <= 1 && data.edges.length === 0) {
        // Only the root node, no deps — nothing graph-worthy to show.
        isEmpty = true;
        return;
      }
    } catch (e) {
      fetchError = e instanceof Error ? e.message : String(e);
      return;
    }
    // Phase 2: render.
    try {
      const { default: mermaid } = await import('mermaid');
      mermaid.initialize({
        startOnLoad: false,
        theme: 'default',
        securityLevel: 'strict',
        flowchart: { padding: 8, htmlLabels: true },
      });
      const { svg, bindFunctions } = await mermaid.render(
        `closure-${rootName}-${Date.now()}`,
        buildSpec(graph!),
      );
      if (container) {
        container.innerHTML = svg;
        bindFunctions?.(container);
      }
    } catch (e) {
      renderError = e instanceof Error ? e.message : String(e);
    }
  });
</script>

<div class="rounded-md border border-line bg-bg-elev/40 px-3 py-3">
  {#if fetchError}
    <p class="text-[11px] text-err">Failed to fetch closure graph: {fetchError}</p>
  {:else if renderError}
    <p class="text-[11px] text-err">Failed to render closure graph: {renderError}</p>
  {:else if isEmpty}
    <p class="text-[11px] text-fg-dim italic">no transitive dependencies</p>
  {/if}
  <div bind:this={container} class="closure-graph overflow-x-auto" data-testid="closure-graph"></div>
  {#if graph}
    {@const externalCount = graph.nodes.filter((n) => n.external).length}
    <div class="flex items-baseline gap-3 mt-2 flex-wrap">
      <p class="text-[10px] text-fg-dim">
        {graph.nodes.length} modules · {graph.edges.length} edges
        {#if externalCount > 0}
          · <span class="italic">{externalCount} external (dashed)</span>
        {/if}
        {#if graph.max_depth_reached}
          · <span class="italic">depth-capped at 10</span>
        {/if}
      </p>
      {#if externalCount > 0}
        <!--
          One-click action to fill in the missing pieces. POSTs the
          dedicated /ingest-missing endpoint which loops Bump per
          external coordinate server-side. Subscribed-but-not-shown
          here: each child Bump emits its own module_indexed event
          on /api/v1/activity/events, which the diff/drift pages already
          consume. After the call returns we refetch the closure so
          the UI flips dashed→solid for the now-known nodes.
        -->
        <button
          type="button"
          class="ml-auto text-[11px] underline text-accent hover:text-fg disabled:text-fg-dim disabled:cursor-not-allowed"
          disabled={ingesting}
          onclick={ingestMissing}
          data-testid="ingest-missing"
        >
          {#if ingesting}
            ingesting… pass {ingestPass}/{ingestMaxPasses} ({ingestProgress} bumps)
          {:else}
            ingest closure (recursive)
          {/if}
        </button>
      {/if}
    </div>
    {#if ingestResult}
      <p class="text-[10px] text-fg-dim mt-1">
        {#if ingestResult.bumped > 0}<span class="text-accent">+{ingestResult.bumped} bumped</span>{/if}
        {#if ingestResult.failed > 0}
          {#if ingestResult.bumped > 0} · {/if}
          <span class="text-err">{ingestResult.failed} failed</span>
        {/if}
      </p>
    {/if}
  {/if}
</div>

<style>
  /*
    Mermaid emits inline styles; nudge the rendered SVG to fit the
    container width without losing aspect ratio. The graph naturally
    grows in height with more deps; let it.
  */
  .closure-graph :global(svg) {
    max-width: 100%;
    height: auto;
  }
</style>
