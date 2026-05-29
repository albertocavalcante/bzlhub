<script lang="ts">
  import type { Hit } from '$api/types';
  import { renderSnippet } from '$lib/search/snippet';
  import HermeticityBadge from './HermeticityBadge.svelte';

  let { hit }: { hit: Hit } = $props();

  const kindLabel: Record<Hit['match_kind'], string> = {
    module: 'mod',
    rule: 'rule',
    provider: 'prov',
    macro: 'macro',
    repository_rule: 'repo',
  };

  const kindColor: Record<Hit['match_kind'], string> = {
    module: 'var(--color-accent)',
    rule: 'var(--color-kind-rule)',
    provider: 'var(--color-kind-provider)',
    macro: 'var(--color-kind-macro)',
    repository_rule: 'var(--color-kind-repository-rule)',
  };

  const snippetHTML = $derived(renderSnippet(hit.snippet));
</script>

<div class="row group flex items-center gap-3 px-3 py-1.5 -mx-3 rounded-md border border-transparent hover:bg-bg-elev hover:border-line transition-colors">
<a
  href={`/modules/${encodeURIComponent(hit.module)}/${encodeURIComponent(hit.version)}`}
  class="flex items-center gap-3 flex-1 min-w-0"
>
  <span
    class="inline-flex items-center justify-center rounded text-[10px] font-medium uppercase tracking-wide w-12 h-5"
    style="background: color-mix(in oklch, {kindColor[hit.match_kind]} 14%, transparent); color: {kindColor[hit.match_kind]}; border: 1px solid color-mix(in oklch, {kindColor[hit.match_kind]} 32%, transparent);"
  >
    {kindLabel[hit.match_kind]}
  </span>

  <div class="min-w-0 flex-1 flex flex-col gap-0.5">
    <div class="flex items-baseline gap-2 min-w-0">
      <span class="font-mono text-[13px] text-fg truncate">
        {hit.match_name ?? hit.module}
      </span>
      <span class="text-[12px] text-fg-dim truncate">
        in <span class="text-fg-mute">{hit.module}</span>@{hit.version}
      </span>
      {#if hit.attr}
        <!--
          Attribute-search hits carry the matched attr name. Render
          as a small chip so the reader immediately sees which attr
          the rule exposes, not just that the rule was matched.
        -->
        <span
          class="text-[10px] font-mono rounded bg-accent/10 text-accent px-1.5 py-0.5"
          data-testid="hit-attr"
        >
          attr: {hit.attr}
        </span>
      {/if}
      {#if hit.file}
        <span class="text-[12px] text-fg-mute truncate font-mono" data-testid="hit-file">
          {hit.file}
        </span>
      {/if}
    </div>
    {#if hit.snippet}
      <div class="text-[12px] text-fg-mute truncate font-mono">
        <!-- snippetHTML is rendered via {@html} after escaping in client.ts -->
        {@html snippetHTML}
      </div>
    {/if}
  </div>

  {#if hit.hermeticity && hit.hermeticity.length > 0}
    <div class="flex items-center gap-1 shrink-0">
      {#each hit.hermeticity.slice(0, 2) as cls (cls)}
        <HermeticityBadge class={cls} compact />
      {/each}
      {#if hit.hermeticity.length > 2}
        <span class="text-[10px] text-fg-dim">+{hit.hermeticity.length - 2}</span>
      {/if}
    </div>
  {/if}
</a>

<!--
  Code-nav side-link. Sibling to the module-page link rather than
  nested inside it (HTML disallows <a> inside <a>), so clicking the
  badge opens code-nav while clicking anywhere else on the row still
  lands on the module page. Hidden until row hover to keep the search
  results visually quiet. Gated on hit.has_source_index so modules
  with empty SCIP (C-library wrappers like zlib that ship zero .bzl
  files) don't surface a link that lands on a friendly 404.
  Pre-migration rows / older API responses where the field is missing
  fall through to truthy so legacy UX doesn't regress.
-->
{#if hit.has_source_index !== false}
  <a
    href={`/modules/${encodeURIComponent(hit.module)}/${encodeURIComponent(hit.version)}/code-nav/`}
    class="code-nav-link shrink-0 text-[11px] font-medium text-fg-mute hover:text-accent px-2 py-1 rounded transition-colors"
    data-testid="code-nav-link"
    aria-label="Browse {hit.module}@{hit.version} source"
  >
    Code →
  </a>
{/if}
</div>
