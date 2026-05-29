import { goto } from '$app/navigation';
import type { Codec } from './codecs';

/**
 * URL-state utilities: read a typed value from `URL.searchParams`,
 * or write a typed value back via SvelteKit's `goto` (push-style
 * history) or `history.replaceState` (replace-style).
 *
 * Used together with the codecs in `./codecs.ts`. Typical page
 * usage:
 *
 *   import { stringList } from '$lib/url-state/codecs';
 *   import { readParam, writeParam } from '$lib/url-state/url';
 *
 *   let classFilter = $state<string[]>(readParam(page.url, 'class', stringList));
 *   $effect(() => {
 *     writeParam(page.url, 'class', classFilter, stringList);
 *   });
 *
 * History mode:
 *   - 'push' (default): goto() with replaceState=false. Browser back
 *     button undoes the change. Use for committed filter chip
 *     toggles, sort changes, tab switches.
 *   - 'replace': history.replaceState. No history entry. Use for
 *     debounced typing or slider drags — otherwise every keystroke
 *     piles up in the back stack.
 */

/** Read a single param from a URL, applying the codec. */
export function readParam<T>(url: URL, key: string, codec: Codec<T>): T {
  return codec.parse(url.searchParams.get(key));
}

/**
 * Write a value back to the URL via SvelteKit navigation. If the
 * codec serializes to `null` (empty/default), the key is dropped —
 * keeping the URL clean.
 *
 * The new URL is built from the *current* URL (preserving other
 * params + the pathname + the hash). Same-page navigation —
 * `keepFocus: true` and `noScroll: true` so filter toggles don't
 * jump the viewport or lose focus from a typing input.
 */
export function writeParam<T>(
  url: URL,
  key: string,
  value: T,
  codec: Codec<T>,
  options?: { history?: 'push' | 'replace' },
): Promise<void> | void {
  const next = new URL(url.href);
  const serialized = codec.serialize(value);
  if (serialized === null) {
    next.searchParams.delete(key);
  } else {
    next.searchParams.set(key, serialized);
  }
  // Re-serialize so the param ordering is stable (alphabetical).
  // Otherwise re-writing one param shuffles others around and the
  // URL string churns even when the semantic state hasn't.
  sortSearchParams(next);
  if (next.href === url.href) return; // no-op write
  const mode = options?.history ?? 'push';
  if (mode === 'replace') {
    // Avoid goto for replace: SvelteKit's replaceState option still
    // triggers a navigation cycle; history.replaceState is leaner
    // for high-frequency writes (debounced typing).
    if (typeof window !== 'undefined') {
      window.history.replaceState(window.history.state, '', next.pathname + next.search + next.hash);
    }
    return;
  }
  return goto(next.pathname + next.search + next.hash, {
    replaceState: false,
    keepFocus: true,
    noScroll: true,
  });
}

/**
 * Sort URLSearchParams in place by key, alphabetical. URLSearchParams
 * doesn't expose an in-place sort that survives re-stringification on
 * all engines, so we rebuild.
 */
function sortSearchParams(url: URL): void {
  const entries = Array.from(url.searchParams.entries()).sort(([a], [b]) => a.localeCompare(b));
  // Clear + re-add in sorted order
  for (const k of Array.from(url.searchParams.keys())) {
    url.searchParams.delete(k);
  }
  for (const [k, v] of entries) {
    url.searchParams.append(k, v);
  }
}
