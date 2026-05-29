// Pure logic + cache helpers backing HoverCard.svelte.
//
// Split out of the .svelte file so vitest (which doesn't have the
// Svelte plugin wired) can unit-test the cache + parser without
// transforming the component module.

import {
  getModuleSummary,
  ModuleSummaryNotFoundError,
  type ModuleSummary,
} from '$lib/api/client';

// Module-scoped dedupe cache. Concurrent hovers on the same target
// share a single in-flight Promise; resolved values stick around for
// the page lifetime so re-hover is instant.
const cache = new Map<string, Promise<ModuleSummary>>();

export function previewCache(name: string): Promise<ModuleSummary> {
  let p = cache.get(name);
  if (!p) {
    p = getModuleSummary(name).catch((e) => {
      // Cache NotFound so we don't retry on every hover. Other
      // errors expire so transient failures get a re-fetch.
      if (!(e instanceof ModuleSummaryNotFoundError)) {
        cache.delete(name);
      }
      throw e;
    });
    cache.set(name, p);
  }
  return p;
}

// moduleNameFromHref extracts the module-name segment from a
// /modules/<name>... href. Returns null when the href doesn't match
// the shape (e.g. code-nav deep links to same-module files), so
// callers can fall through to a bare link.
export function moduleNameFromHref(href: string | undefined | null): string | null {
  if (!href) return null;
  const m = href.match(/^\/modules\/([^/?#]+)/);
  if (!m) return null;
  try {
    return decodeURIComponent(m[1]);
  } catch {
    return m[1];
  }
}

// nextHoverCardId returns a unique number per call. Used to give
// each HoverCard a stable aria-describedby target on the same page.
let idCounter = 0;
export function nextHoverCardId(): number {
  return ++idCounter;
}

// Exported for tests that need to start from a clean cache.
export function _resetPreviewCacheForTesting(): void {
  cache.clear();
}
