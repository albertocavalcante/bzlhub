// Shared loader for the per-(module, version) report.
//
// The per-version page splits into multiple tabs (Overview /
// Documentation / Code / Testing — see I8). Each tab needs the same
// report payload; this store keeps one in-memory copy keyed by
// (module, version) so navigating between tabs doesn't re-fetch.
//
// SPA-only (svelte.config.js: ssr = false), so a module-level
// singleton is safe — there's no cross-request leakage to worry about.

import { getModule, ModuleNotFoundError } from '$lib/api/client';
import type { ModuleReport } from '$lib/api/types';

class ModuleVersionStore {
  report = $state<ModuleReport | null>(null);
  loading = $state(true);
  // 'not_found' is the dedicated state the per-version page renders
  // ModuleNotFound for (preflight + ingest UX); any other non-empty
  // string is a fatal error message.
  error = $state<string | null>(null);

  // Cache key for the most recent successful load. Used so a child
  // tab calling load() with the same coords as the layout's load()
  // doesn't trigger a redundant fetch.
  private loadedKey = '';
  // Identifies the in-flight request; allows abort on coord change.
  private inflight: AbortController | null = null;

  load(name: string, version: string): AbortController {
    const key = `${name}@${version}`;
    if (this.loadedKey === key && this.report) {
      // Already have the right report — no-op. Returns a fresh
      // controller anyway so the caller can chain abort logic
      // uniformly.
      return new AbortController();
    }
    // Coord changed: abort any in-flight request and reset state so
    // the previous report doesn't briefly flash for the new coords.
    if (this.inflight) this.inflight.abort();
    const ctl = new AbortController();
    this.inflight = ctl;
    this.loading = true;
    this.error = null;
    this.report = null;

    void getModule(name, version, ctl.signal)
      .then((r) => {
        if (ctl.signal.aborted) return;
        this.report = r;
        this.loadedKey = key;
      })
      .catch((e: unknown) => {
        if (ctl.signal.aborted) return;
        if (e instanceof ModuleNotFoundError) {
          this.error = 'not_found';
        } else {
          this.error = e instanceof Error ? e.message : String(e);
        }
      })
      .finally(() => {
        if (!ctl.signal.aborted) this.loading = false;
        if (this.inflight === ctl) this.inflight = null;
      });
    return ctl;
  }

  // ModuleNotFound resolution path: an ingest just completed; force
  // a refetch even if the coords haven't changed.
  refresh(name: string, version: string) {
    this.loadedKey = '';
    this.load(name, version);
  }
}

export const moduleVersion = new ModuleVersionStore();
