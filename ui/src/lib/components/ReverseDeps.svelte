<!--
  ReverseDeps — "modules in the index that depend on this one."

  Symmetric counterpart to the closure graph: closure shows what
  THIS module brings in; reverse-deps shows what brings IT in.
  Together they answer the two questions Bazel maintainers ask
  before bumping a module: "what will I have to update?" (forward)
  and "what else will need updating downstream?" (reverse).

  Lazy-fetched on first expand of the section disclosure — the
  server walk is O(corpus) so we don't preload it on every page
  view.
-->
<script lang="ts">
  import { paths } from '$lib/api/paths';
  import { base } from '$app/paths';
  import { moduleVersionHref } from '$lib/links';

  interface ReverseDep {
    name: string;
    version: string;
  }
  interface Props {
    module: string;
    version: string;
  }

  let { module, version }: Props = $props();

  let state = $state<
    | { kind: 'idle' }
    | { kind: 'loading' }
    | { kind: 'loaded'; deps: ReverseDep[] }
    | { kind: 'error'; message: string }
  >({ kind: 'idle' });

  async function load() {
    if (state.kind !== 'idle') return;
    state = { kind: 'loading' };
    try {
      const url = `${base}${paths.closure.reverseDeps(module, version)}`;
      const res = await fetch(url);
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const data = await res.json();
      state = { kind: 'loaded', deps: data.deps ?? [] };
    } catch (e) {
      state = { kind: 'error', message: e instanceof Error ? e.message : String(e) };
    }
  }
</script>

<details
  class="border border-line rounded-md bg-bg-elev/40"
  ontoggle={(e) => {
    if ((e.target as HTMLDetailsElement).open) void load();
  }}
>
  <summary
    class="cursor-pointer px-3 py-2 text-[12px] flex items-baseline gap-2 list-none"
  >
    <span class="text-xs uppercase tracking-wide text-fg-dim">used by</span>
    <span class="text-fg-dim">— modules in this index that depend on
      <span class="font-mono text-fg-mute">{module}@{version}</span></span>
  </summary>

  <div class="px-3 pb-3 border-t border-line/60">
    {#if state.kind === 'loading'}
      <p class="text-[11px] text-fg-dim italic mt-2">scanning corpus…</p>
    {:else if state.kind === 'error'}
      <p class="text-[11px] text-err mt-2">Couldn't load reverse deps: {state.message}</p>
    {:else if state.kind === 'loaded'}
      {#if state.deps.length === 0}
        <p class="text-[11px] text-fg-dim italic mt-2">
          no other indexed module depends on this exact coordinate
        </p>
      {:else}
        <ul class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-1.5 font-mono text-[13px] mt-2">
          {#each state.deps as d (d.name + d.version)}
            <li class="flex items-baseline gap-1.5 text-fg-mute">
              <a
                href={moduleVersionHref(d.name, d.version)}
                class="text-fg hover:text-accent hover:underline"
              >{d.name}</a>
              <span class="text-fg-dim">@</span>
              <a
                href={moduleVersionHref(d.name, d.version)}
                class="hover:text-accent hover:underline"
              >{d.version}</a>
            </li>
          {/each}
        </ul>
        <p class="text-[10px] text-fg-dim mt-2">
          {state.deps.length} downstream module{state.deps.length === 1 ? '' : 's'}
        </p>
      {/if}
    {/if}
  </div>
</details>
