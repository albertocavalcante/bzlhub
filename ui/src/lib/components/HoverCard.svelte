<!--
  HoverCard — cross-module preview popover.

  Wraps a link that points to /modules/<name>; on hover OR keyboard
  focus, fetches the module's summary and renders a small card with
  name + latest version + repo label + version count + a Browse →
  affordance. 150ms open-delay so casual mouse-overs don't trigger
  fetches; 300ms close-delay so the user can move into the card
  without it disappearing.

  Accessibility: pointer + keyboard parity (focusin triggers the
  same fetch); ESC closes; the card is a tooltip (no role=dialog, no
  focus trap). aria-describedby links the wrapped link to the card.

  Cache: per-page Map<moduleName, Promise<Preview>> dedupes
  concurrent hovers on the same target and avoids redundant network.

  Positioning: picks above/below by available room then clamps the
  card into the viewport. Pure math; no floating-ui dep.
-->
<script lang="ts">
  import { onDestroy } from 'svelte';
  import { page } from '$app/state';
  import { ModuleSummaryNotFoundError, type ModuleSummary } from '$lib/api/client';
  import { previewCache, nextHoverCardId } from './hover-card';
  import DriftChip from './DriftChip.svelte';

  let {
    moduleName,
    children,
  }: {
    moduleName: string;
    children: import('svelte').Snippet;
  } = $props();

  // Suppress the hover when the target IS the page's own module —
  // a chip pointing back into the current module (self-references in
  // a rule's own Stardoc body produce these) would otherwise pop a
  // card showing the page the user is already on. Reactive so
  // client-side navigation between module pages stays correct.
  const isSelfReference = $derived(page.params?.name === moduleName);

  type LoadState =
    | { kind: 'idle' }
    | { kind: 'pending' }
    | { kind: 'loading' }
    | { kind: 'loaded'; preview: ModuleSummary }
    | { kind: 'notfound' }
    | { kind: 'error'; message: string };

  let loadState: LoadState = $state({ kind: 'idle' });
  let open = $state(false);
  let placement = $state<{ x: number; y: number; above: boolean }>({
    x: 0,
    y: 0,
    above: false,
  });

  let wrapperEl: HTMLSpanElement | undefined = $state();
  // ID for aria-describedby. Suffix from a counter to keep
  // multi-instance pages collision-free.
  const cardId = `hovercard-${nextHoverCardId()}`;

  // Timers — null when no pending action. We hold both so a quick
  // re-hover during the close-delay cancels the close cleanly.
  let openTimer: ReturnType<typeof setTimeout> | null = null;
  let closeTimer: ReturnType<typeof setTimeout> | null = null;

  const OPEN_DELAY_MS = 150;
  const CLOSE_DELAY_MS = 300;

  function startOpen() {
    cancelClose();
    if (open || loadState.kind !== 'idle') return;
    loadState = { kind: 'pending' };
    openTimer = setTimeout(triggerLoad, OPEN_DELAY_MS);
  }

  function cancelOpen() {
    if (openTimer) {
      clearTimeout(openTimer);
      openTimer = null;
    }
    if (loadState.kind === 'pending') {
      loadState = { kind: 'idle' };
    }
  }

  function startClose() {
    cancelOpen();
    if (closeTimer) return;
    closeTimer = setTimeout(() => {
      closeTimer = null;
      open = false;
      // Stay in loaded/notfound/error so a re-hover within the page
      // gets the cached result instantly via the dedupe Map.
    }, CLOSE_DELAY_MS);
  }

  function cancelClose() {
    if (closeTimer) {
      clearTimeout(closeTimer);
      closeTimer = null;
    }
  }

  function closeNow() {
    cancelOpen();
    cancelClose();
    open = false;
  }

  async function triggerLoad() {
    openTimer = null;
    if (!wrapperEl) return;
    placement = computePlacement(wrapperEl);
    open = true;
    loadState = { kind: 'loading' };
    try {
      const preview = await previewCache(moduleName);
      loadState = { kind: 'loaded', preview };
    } catch (e) {
      if (e instanceof ModuleSummaryNotFoundError) {
        loadState = { kind: 'notfound' };
      } else {
        loadState = { kind: 'error', message: e instanceof Error ? e.message : String(e) };
      }
    }
  }

  function onKeydown(e: KeyboardEvent) {
    if (e.key === 'Escape' && open) {
      e.stopPropagation();
      closeNow();
    }
  }

  // Card geometry constants. CARD_WIDTH matches the rendered `w-72`
  // class (18rem at default tailwind = 288px). CARD_HEIGHT_EST is an
  // upper bound for the loaded state; the worst case is a card that
  // briefly extends a few px past the viewport edge before the
  // browser scrolls focus into view.
  const CARD_WIDTH_PX = 288;
  const CARD_HEIGHT_EST_PX = 140;
  const VIEWPORT_MARGIN_PX = 4;

  // computePlacement returns the (x, y) offset (wrapper-relative)
  // for the absolutely-positioned card. Strategy:
  //   1. Pick above/below by whichever side has more room (so
  //      targets near either the top OR the bottom edge get the
  //      side that actually fits — the prior "below by default"
  //      logic could overflow when both fit poorly).
  //   2. Right-align if the card would extend past the right edge.
  //   3. Clamp the final absolute Y into the viewport so a tall
  //      card in a short window can't escape.
  function computePlacement(wrapper: HTMLElement): {
    x: number;
    y: number;
    above: boolean;
  } {
    const rect = wrapper.getBoundingClientRect();
    const wrapperHeight = rect.height || 16;

    // X axis: shift left if the card would overflow the right edge.
    let x = 0;
    const overshootRight = rect.left + CARD_WIDTH_PX - window.innerWidth;
    if (overshootRight > 0) {
      x = -overshootRight - VIEWPORT_MARGIN_PX;
    }

    // Y axis: prefer the side with more vertical room. Equal-room
    // → below (better for keyboard scroll affordance — tab order
    // continues downward).
    const roomAbove = rect.top - VIEWPORT_MARGIN_PX;
    const roomBelow = window.innerHeight - rect.bottom - VIEWPORT_MARGIN_PX;
    const above = roomAbove > roomBelow && roomBelow < CARD_HEIGHT_EST_PX;

    let y = above
      ? -(CARD_HEIGHT_EST_PX + VIEWPORT_MARGIN_PX)
      : wrapperHeight + VIEWPORT_MARGIN_PX;

    // Defensive clamp: ensure the card's top + bottom both stay in
    // the viewport even if both above/below have insufficient room
    // (very short viewport, very tall card). Translates the card up
    // or down to fit; the connection to the target is visually
    // weaker but no scroll/overflow.
    const absTop = rect.top + y;
    const absBottom = absTop + CARD_HEIGHT_EST_PX;
    if (absTop < VIEWPORT_MARGIN_PX) {
      y += VIEWPORT_MARGIN_PX - absTop;
    } else if (absBottom > window.innerHeight - VIEWPORT_MARGIN_PX) {
      y -= absBottom - (window.innerHeight - VIEWPORT_MARGIN_PX);
    }

    return { x, y, above };
  }

  onDestroy(() => {
    cancelOpen();
    cancelClose();
  });
</script>

<!-- The wrapper is an inline span so the surrounding chip flow is
     unchanged. position:relative so the card's absolute coords
     anchor here. The card itself lives outside the link <a> tag
     (HTML disallows nested interactive content). The pointer/focus
     handlers here OBSERVE the wrapped link's hover state to show a
     tooltip — the span isn't itself a separate interactive target.
     The wrapped <a> remains the focusable element and handles
     click/keyboard activation. -->
{#if isSelfReference}
  <!-- Self-reference: don't wrap. The user is already on this
       module's page; popping a card for the page they're reading
       would be noise. Bare passthrough so layout stays identical. -->
  {@render children()}
{:else}
<!-- svelte-ignore a11y_no_static_element_interactions -->
<span
  bind:this={wrapperEl}
  class="hover-card-wrapper relative inline-flex"
  onpointerenter={startOpen}
  onpointerleave={startClose}
  onfocusin={startOpen}
  onfocusout={startClose}
  onkeydown={onKeydown}
>
  <span aria-describedby={open ? cardId : undefined}>{@render children()}</span>

  {#if open}
    <div
      id={cardId}
      role="tooltip"
      class="hover-card absolute z-50 w-72 rounded-md border border-line bg-bg shadow-lg p-3 flex flex-col gap-2 text-[12px]"
      style:left="{placement.x}px"
      style:top="{placement.y}px"
      onpointerenter={cancelClose}
      onpointerleave={startClose}
    >
      {#if loadState.kind === 'loading'}
        <div class="text-fg-mute">Loading {moduleName}…</div>
      {:else if loadState.kind === 'loaded'}
        {@const p = loadState.preview}
        <div class="flex items-baseline justify-between gap-2">
          <span class="font-mono text-[13px] text-fg truncate">{p.name}</span>
          <span class="text-[11px] text-fg-mute font-mono">@{p.latest_version}</span>
        </div>
        <div class="flex items-baseline gap-2 text-[11px] text-fg-mute">
          {#if p.version_count > 1}
            <span class="font-mono">{p.version_count} versions</span>
          {/if}
          {#if p.repo_label}
            <span class="font-mono truncate" title={p.homepage}>{p.repo_label}</span>
          {/if}
          <!--
            Drift chip in the popover meta line (Plan 19 Idea A).
            Silent for the unknown / in-sync default; visible only
            when a drift source has populated the cache. Clicks
            navigate to the drift filter for this module.
          -->
          <DriftChip drift={p.drift} module={p.name} />
        </div>
        <a
          href={`/modules/${encodeURIComponent(p.name)}/${encodeURIComponent(p.latest_version)}`}
          class="text-[11px] font-mono text-accent hover:underline self-start"
        >
          Browse →
        </a>
      {:else if loadState.kind === 'notfound'}
        <div class="text-fg-mute">
          <span class="font-mono text-fg">{moduleName}</span> isn't indexed in this canopy.
        </div>
      {:else if loadState.kind === 'error'}
        <div class="text-err text-[11px]" title={loadState.message}>
          Couldn't load preview.
        </div>
      {/if}
    </div>
  {/if}
</span>
{/if}

<style>
  /* Card animation only when reduced-motion isn't set. */
  @keyframes fade-in {
    from { opacity: 0; transform: translateY(2px); }
    to { opacity: 1; transform: translateY(0); }
  }
  .hover-card {
    animation: fade-in 0.12s ease-out;
  }
  @media (prefers-reduced-motion: reduce) {
    .hover-card {
      animation: none;
    }
  }
</style>
