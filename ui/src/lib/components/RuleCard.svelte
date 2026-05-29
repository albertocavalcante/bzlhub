<script lang="ts">
  import type { ParsedDoc, RuleSpec } from '$api/types';
  import { codeNavFileHref } from '$lib/links';
  import MarkdownDoc from './MarkdownDoc.svelte';
  import StardocBody from './StardocBody.svelte';

  // RuleCard needs the owning module coordinate to compose a code-nav
  // link for `rule.provenance`. Passed in by the parent — kept as
  // explicit props (rather than pulling from $page.params) so the card
  // is also drop-in usable from non-route contexts (e.g. the diff page).
  let {
    rule,
    module,
    version,
    parsedDoc,
  }: {
    rule: RuleSpec;
    module: string;
    version: string;
    // Optional parsed-doc body for this rule, lifted from the
    // page-level `parsed_docs` map. When present, StardocBody renders
    // the structured form (summary / desc / args / returns / example).
    // When absent, we fall through to the raw MarkdownDoc(rule.doc)
    // path so older callers / older API responses still work.
    parsedDoc?: ParsedDoc;
  } = $props();
  let open = $state(false);

  // Auto-expand + scroll into view when the page lands with a matching
  // hash. Accepts BOTH the canopy-internal prefixed form
  // (#rule-cc_binary, used by the diff page's deep-links) AND the
  // bare-name form (#cc_binary), which is Stardoc's convention and
  // what authors write in their doc= strings via [text](#sibling).
  // Without the bare-name match, intra-doc cross-references in
  // rendered Markdown wouldn't open the target card.
  $effect(() => {
    if (typeof window === 'undefined') return;
    const prefixed = `#rule-${rule.name}`;
    const bare = `#${rule.name}`;
    const sync = () => {
      const h = window.location.hash;
      if (h === prefixed || h === bare) {
        open = true;
        // Allow the open transition to render before scrolling.
        queueMicrotask(() => {
          document.getElementById(`rule-${rule.name}`)?.scrollIntoView({ block: 'start' });
        });
      }
    };
    sync();
    window.addEventListener('hashchange', sync);
    return () => window.removeEventListener('hashchange', sync);
  });
</script>

<!--
  Empty bare-name anchor preceding the card. Stardoc convention is
  `[text](#symbol_name)` — when a rule's doc string cross-references
  a sibling symbol, that link expects an element with `id="symbol_name"`.
  The card itself keeps `id="rule-{name}"` for backward compat with
  internal deep-links from the diff page. scroll-mt-20 on the anchor
  matches the card so smooth-scroll lands at the same offset.
-->
<!-- Bare-name scroll-anchor (#cc_binary). <span> not <a> because we
     never navigate from here; <a> without href is the svelte-check a11y
     warning. <span id="..."> is a valid named anchor in HTML5. -->
<span id={rule.name} class="scroll-mt-20" aria-hidden="true"></span>
<article
  id={`rule-${rule.name}`}
  class="border border-line rounded-md bg-bg-elev/50 scroll-mt-20"
>
  <header
    class="flex items-baseline gap-3 px-3 py-2 cursor-pointer"
    onclick={() => (open = !open)}
    onkeydown={(e) => {
      if (e.key === 'Enter' || e.key === ' ') {
        e.preventDefault();
        open = !open;
      }
    }}
    role="button"
    tabindex="0"
    aria-expanded={open}
  >
    <span class="font-mono text-[13px] text-fg shrink-0">
      {rule.name}
    </span>
    {#if rule.executable}
      <span class="text-[10px] text-fg-dim uppercase tracking-wide">exec</span>
    {/if}
    {#if rule.test}
      <span class="text-[10px] text-fg-dim uppercase tracking-wide">test</span>
    {/if}
    {#if rule.private}
      <span class="text-[10px] text-fg-dim uppercase tracking-wide">private</span>
    {/if}
    <a
      href={codeNavFileHref(module, version, rule.provenance.file, rule.provenance.start_row)}
      class="ml-auto text-[11px] text-fg-dim hover:text-accent font-mono truncate"
      title="Open in code-nav"
      onclick={(e) => {
        // The expand toggle owns the row's click; the link must NOT
        // bubble the click up to the header's onclick. Stopping here
        // lets users hit the file path to jump to code-nav while the
        // rest of the row still expands the schema.
        e.stopPropagation();
      }}
    >
      {rule.provenance.file}:{rule.provenance.start_row}
    </a>
    <!-- Plan 07: cross-corpus consumer view. Standalone page until
         a Consumers tab lands; the link surfaces "who uses this?"
         without expanding the rule card. -->
    <a
      href={`/modules/${encodeURIComponent(module)}/${encodeURIComponent(version)}/consumers/${encodeURIComponent(rule.name)}`}
      class="text-[11px] text-fg-dim hover:text-accent font-mono"
      title="Find every call site of this rule across canopy's indexed corpus"
      aria-label={`who uses ${rule.name}`}
      onclick={(e) => e.stopPropagation()}
    >
      consumers →
    </a>
    <span class="text-fg-dim text-xs ml-1">
      {open ? '−' : '+'}
    </span>
  </header>

  {#if open}
    <div class="px-3 pb-3 pt-1 border-t border-line/60 flex flex-col gap-3">
      {#if parsedDoc}
        <!--
          Phase-3 path: render the Stardoc-parsed form. Lets a real
          `Args:` block in the doc string surface as a real
          parameter table (separate from the rule's attribute schema
          below — args and attrs are different concepts).
        -->
        <StardocBody parsed={parsedDoc} raw={rule.doc} />
      {:else if rule.doc}
        <!--
          Fallback: server didn't include a parsed_docs entry for
          this rule (older payload, or empty docstring). Render the
          raw doc through MarkdownDoc.
        -->
        <MarkdownDoc source={rule.doc} />
      {/if}

      {#if rule.attrs && rule.attrs.length > 0}
        <!--
          Provenance hint: name how this attrs slice was produced.
          'literal' / undefined → no tag (the boring default).
          'symbol_fold' → resolved by following module-local bindings.
          'load_resolve' → resolved by following load() chains.
          'interpreted'  → resolved by actually running the .bzl in
                           a sandboxed Bazel-Starlark interpreter.
          Tags are intentionally small + grey so they don't distract
          when scanning attrs — they're for the "wait, where did
          these come from?" moment.
        -->
        {#if rule.attrs_extraction_method && rule.attrs_extraction_method !== 'literal'}
          <div class="text-[10px] text-fg-dim font-mono" data-testid="attrs-provenance">
            attrs resolved via <span class="text-fg-mute">{rule.attrs_extraction_method}</span>
            {#if rule.attrs_extraction_method === 'symbol_fold'}
              <span class="text-fg-dim/70">— followed module-local bindings</span>
            {:else if rule.attrs_extraction_method === 'load_resolve'}
              <span class="text-fg-dim/70">— followed load() chains</span>
            {:else if rule.attrs_extraction_method === 'interpreted'}
              <span class="text-fg-dim/70">— evaluated .bzl in sandboxed Starlark</span>
            {/if}
          </div>
        {/if}
        <div class="overflow-x-auto">
          <table class="w-full text-[12px] font-mono">
            <thead>
              <tr class="text-[10px] uppercase tracking-wide text-fg-dim">
                <th class="text-left font-medium py-1 pr-3">attr</th>
                <th class="text-left font-medium py-1 pr-3">type</th>
                <th class="text-left font-medium py-1 pr-3">req</th>
                <th class="text-left font-medium py-1 pr-3">default</th>
                <th class="text-left font-medium py-1">doc</th>
              </tr>
            </thead>
            <tbody>
              {#each rule.attrs as a (a.name)}
                <tr class="border-t border-line/40">
                  <td class="py-1.5 pr-3 text-fg">{a.name}</td>
                  <td class="py-1.5 pr-3 text-fg-mute">{a.type ?? '—'}</td>
                  <td class="py-1.5 pr-3">
                    {#if a.mandatory}<span class="text-warn">yes</span>{:else}<span class="text-fg-dim">no</span>{/if}
                  </td>
                  <td class="py-1.5 pr-3 text-fg-mute">{a.default ?? '—'}</td>
                  <td class="py-1.5 text-fg-mute leading-snug">
                    {#if a.doc}<MarkdownDoc source={a.doc} />{/if}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {:else}
        <div
          class="rounded border border-line/60 bg-bg/40 p-3 flex flex-col gap-2"
        >
          <div class="flex items-baseline gap-2">
            <span
              class="text-[10px] uppercase tracking-wide font-medium text-warn"
              >dynamic schema</span
            >
            <span class="text-[11px] text-fg-dim font-mono"
              >attrs not statically extractable</span
            >
          </div>
          <p class="text-[12px] text-fg-mute leading-relaxed">
            All three extraction tiers tried and gave up on this rule's
            <code class="text-fg">attrs</code>: the AST walk found no literal
            dict, same-file symbol-fold couldn't resolve the expression, and the
            sandboxed Starlark interpreter either declined to evaluate this file
            or evaluated it without registering the rule.
          </p>
          <p class="text-[11px] text-fg-dim leading-relaxed">
            See <code class="text-fg-mute">{rule.provenance.file}:{rule.provenance.start_row}</code>
            for the source. The full catalogue of "why does this still happen?"
            lives in the
            <a
              href="https://github.com/albertocavalcante/assay/blob/main/interp/LIMITATIONS.md"
              target="_blank"
              rel="noopener noreferrer"
              class="underline hover:text-fg"
            >assay/interp limitations doc</a> — common causes are
            <code class="text-fg-mute">repository_rule()</code> (interpreter
            doesn't expose it), external <code class="text-fg-mute">load()</code>
            symbols used at module-load time, or
            <code class="text-fg-mute">native.*</code> calls whose return value
            is consumed during eval.
          </p>
        </div>
      {/if}
    </div>
  {/if}
</article>
