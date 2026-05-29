<script lang="ts">
  import type { HermeticityClass } from '$api/types';

  let { class: cls, compact = false }: { class: HermeticityClass; compact?: boolean } = $props();

  // Each class maps to a CSS variable + short label. The dot keeps the
  // badge readable even for color-blind users — the label is the source
  // of truth, the color is reinforcement.
  const META: Record<
    HermeticityClass,
    { label: string; color: string; tip: string }
  > = {
    'pure-starlark': {
      label: 'pure-starlark',
      color: 'var(--color-herm-pure)',
      tip: 'No network fetches, no system exec. Most reproducible.',
    },
    'prebuilt-binaries-pinned': {
      label: 'prebuilt-pinned',
      color: 'var(--color-herm-pinned)',
      tip: 'Ships or fetches prebuilt binaries with SHA pinned.',
    },
    'build-from-source': {
      label: 'build-from-source',
      color: 'var(--color-herm-fromsrc)',
      tip: 'Compiles sources; no prebuilt binaries.',
    },
    'network-fetch-pinned': {
      label: 'fetch-pinned',
      color: 'var(--color-herm-fetch)',
      tip: 'Network fetch at fetch time with integrity hash.',
    },
    'network-fetch-unpinned': {
      label: 'fetch-unpinned',
      color: 'var(--color-herm-unpinned)',
      tip: 'Network fetch without literal integrity hash. Review.',
    },
    'requires-system-tools': {
      label: 'system-tools',
      color: 'var(--color-herm-systool)',
      tip: 'Runs docker/git/python/etc. from host PATH.',
    },
    'repository-rule-arbitrary-code': {
      label: 'repo-rule-exec',
      color: 'var(--color-herm-reprule)',
      tip: 'Runs arbitrary commands via ctx.execute.',
    },
  };

  const meta = $derived(META[cls]);
</script>

<span
  class="inline-flex items-center gap-1.5 rounded text-[11px] font-medium leading-tight whitespace-nowrap"
  class:py-0.5={!compact}
  class:px-1.5={!compact}
  class:py-0={compact}
  class:px-1={compact}
  style="background: color-mix(in oklch, {meta.color} 14%, transparent); color: {meta.color}; border: 1px solid color-mix(in oklch, {meta.color} 32%, transparent);"
  title={meta.tip}
>
  <span
    class="inline-block w-1.5 h-1.5 rounded-full"
    style="background: {meta.color};"
  ></span>
  {meta.label}
</span>
