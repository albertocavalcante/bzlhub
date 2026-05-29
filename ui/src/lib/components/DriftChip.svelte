<script lang="ts">
  import type { DriftSummary } from '$lib/api/client';
  import { chipState } from './drift-chip';

  /**
   * DriftChip renders the cached drift signal for a module version
   * as a small inline chip. Implements Plan 19 Idea A.
   *
   * Signal-by-absence: unknown, in-sync, and upstream-error all
   * render as NOTHING (no DOM at all). Visible chips are the
   * exception, and they MEAN something.
   *
   * All derivation lives in `drift-chip.ts` for direct vitest
   * coverage without the Svelte vite plugin — matching the
   * hover-card.ts precedent.
   *
   * Props:
   *   drift  — the api.DriftSummary payload (always required).
   *   module — when provided, chip clicks to /drift?module=<name>,
   *            taking the user to the per-module drift view.
   *            When absent, falls back to the generic /drift page.
   *   href   — manual escape hatch when callers need a custom
   *            target (deep-linked drift filters, share URLs);
   *            overrides the module-built URL.
   */

  let {
    drift,
    module: moduleName,
    href,
  }: { drift: DriftSummary; module?: string; href?: string } = $props();

  const state = $derived(chipState(drift));

  const linkHref = $derived(
    href ?? (moduleName ? `/drift?module=${encodeURIComponent(moduleName)}` : '/drift'),
  );
</script>

{#if state.visible}
  <a
    href={linkHref}
    title={state.title}
    data-status={state.status}
    data-behind={drift.behind ?? undefined}
    class="inline-flex items-center rounded text-[11px] font-medium leading-tight whitespace-nowrap px-1.5 py-0.5 hover:opacity-90 transition-opacity"
    style="background: color-mix(in oklch, {state.colorVar} 14%, transparent); color: {state.colorVar}; border: 1px solid color-mix(in oklch, {state.colorVar} 32%, transparent);"
  >
    {state.label}
  </a>
{/if}
