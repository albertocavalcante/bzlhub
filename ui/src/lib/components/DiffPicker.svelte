<!--
  DiffPicker — compact "compare with…" dropdown.

  Use case: from the per-version page, let users diff the current
  version against any other indexed version without going back to
  the versions list. The picked version is always emitted as
  `from`; the page's own version is the `to` side. Users who want
  the inverse direction can swap on the diff page itself.

  Display-only: it just navigates to diffHref(...). The list of
  options comes from the server-shaped VersionEntry[]; this
  component doesn't filter, dedupe, or sort.
-->
<script lang="ts">
  import { goto } from '$app/navigation';
  import type { VersionEntry } from '$api/client';
  import { diffHref } from '$lib/links';

  interface Props {
    module: string;
    /** The version on this page (used as `to`). */
    current: string;
    /** Full set of indexed versions for this module. */
    entries: VersionEntry[];
    /** Optional CSS class applied to the wrapper. */
    class?: string;
  }

  let { module, current, entries, class: klass = '' }: Props = $props();

  // Versions other than the one we're on. Stubs stay in the list
  // (a user might legitimately want to inspect a stub diff) but
  // get an inline label so they don't look like real releases.
  const options = $derived(entries.filter((e) => e.version !== current));

  function onChange(ev: Event) {
    const v = (ev.target as HTMLSelectElement).value;
    if (!v) return;
    goto(diffHref(module, v, current));
  }
</script>

{#if options.length > 0}
  <label class="text-[11px] text-fg-dim flex items-center gap-1.5 {klass}">
    diff with
    <select
      onchange={onChange}
      class="rounded border border-line bg-bg-elev px-2 py-1 text-[11px] font-mono text-fg outline-none focus:border-accent"
      value=""
    >
      <option value="">pick a version…</option>
      {#each options as o (o.version)}
        <option value={o.version}>
          {o.version}{o.is_stub ? ' (stub)' : ''}
        </option>
      {/each}
    </select>
  </label>
{/if}
