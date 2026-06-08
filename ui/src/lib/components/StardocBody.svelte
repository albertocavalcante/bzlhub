<!--
  StardocBody — renders a presentation-ready ParsedDoc.

  The backend (bzlhub's internal/docview package) ships:
   - the section structure (Summary / Description / Args / Returns / ...)
   - per-Ref resolved Hrefs (already filtered for splice-safety)
   - a deduplicated Chips list

  This component is display-only: it iterates and renders. It does
  not compute hrefs, dedup lists, or decide what to skip. Splicing
  is mechanical — for each Ref with a non-empty Href we substitute
  [Text](Href) into the source field at Offset, processed end-to-
  start so insertions don't shift earlier indices.
-->
<script lang="ts">
  import type { ParsedDoc } from '$api/types';
  import MarkdownDoc from './MarkdownDoc.svelte';
  import HoverCard from './HoverCard.svelte';
  import { moduleNameFromHref } from './hover-card';

  interface Props {
    /** Parsed Stardoc-shape body (from server's parsed_docs map). */
    parsed?: ParsedDoc;
    /**
     * Raw doc string to use when `parsed` is absent. Lets callers
     * keep their existing wiring during incremental rollout — pass
     * both and the component prefers `parsed`.
     */
    raw?: string;
  }

  let { parsed, raw }: Props = $props();

  // linkify is a pure splice: for each Ref of the named field that
  // has a server-resolved Href, replace Text at Offset with the
  // Markdown link form. No URL composition, no edge-case decisions —
  // the backend already excluded refs that would corrupt the splice.
  function linkify(source: string | undefined, field: string): string | undefined {
    if (!source || !parsed?.Refs) return source;
    const fieldRefs = parsed.Refs.filter((r) => r.Field === field && r.Splice && r.Href);
    if (fieldRefs.length === 0) return source;
    // Sort by Offset DESC so each insertion's longer-than-original
    // replacement doesn't shift earlier refs.
    fieldRefs.sort((a, b) => b.Offset - a.Offset);
    let out = source;
    for (const r of fieldRefs) {
      const before = out.slice(0, r.Offset);
      const after = out.slice(r.Offset + r.Text.length);
      out = `${before}[${r.Text}](${r.Href})${after}`;
    }
    return out;
  }

  // When parsed is missing we still want the doc to render — fall
  // back to passing the raw string through MarkdownDoc.
  const hasStructured = $derived(
    !!parsed &&
      (parsed.Summary ||
        parsed.Description ||
        (parsed.Args?.length ?? 0) > 0 ||
        parsed.Returns ||
        (parsed.Examples?.length ?? 0) > 0 ||
        parsed.Deprecated ||
        parsed.Note),
  );
</script>

{#if !hasStructured}
  <!-- Fallback: render raw doc body unparsed. -->
  {#if raw}
    <MarkdownDoc source={raw} />
  {/if}
{:else if parsed}
  <div class="stardoc flex flex-col gap-3">
    {#if parsed.Deprecated}
      <!--
        Deprecated banner up top — it's the most important signal:
        if the symbol is deprecated, everything else is contextual.
      -->
      <div class="rounded border border-warn/40 bg-warn/10 px-3 py-2">
        <p class="text-[10px] uppercase tracking-wide text-warn font-medium mb-1">
          deprecated
        </p>
        <MarkdownDoc source={linkify(parsed.Deprecated, 'Deprecated')} />
      </div>
    {/if}

    {#if parsed.Summary}
      <div class="text-[13px] text-fg leading-snug">
        <MarkdownDoc source={linkify(parsed.Summary, 'Summary')} />
      </div>
    {/if}

    {#if parsed.Description}
      <MarkdownDoc source={linkify(parsed.Description, 'Description')} />
    {/if}

    {#if parsed.Args && parsed.Args.length > 0}
      <!--
        Args table — the headline win of Phase 3. Without parsing,
        macros render as just a list of names; with the parser we
        get per-arg name/type/doc rows.
      -->
      <div class="overflow-x-auto">
        <table class="w-full text-[12px] font-mono">
          <thead>
            <tr class="text-[10px] uppercase tracking-wide text-fg-dim">
              <th class="text-left font-medium py-1 pr-3">arg</th>
              <th class="text-left font-medium py-1 pr-3">type</th>
              <th class="text-left font-medium py-1">doc</th>
            </tr>
          </thead>
          <tbody>
            {#each parsed.Args as a (a.Name)}
              <tr class="border-t border-line/40">
                <td class="py-1.5 pr-3 text-fg">{a.Name}</td>
                <td class="py-1.5 pr-3 text-fg-mute">{a.Type ?? '—'}</td>
                <td class="py-1.5 text-fg-mute leading-snug">
                  {#if a.Doc}<MarkdownDoc source={linkify(a.Doc, `Args[${a.Name}].Doc`)} />{/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}

    {#if parsed.Returns}
      <div class="rounded border border-line/40 bg-bg-elev/40 px-3 py-2">
        <p class="text-[10px] uppercase tracking-wide text-fg-dim font-medium mb-1">
          returns{#if parsed.Returns.Type}
            <span class="ml-2 text-fg-mute font-mono normal-case">{parsed.Returns.Type}</span>
          {/if}
        </p>
        {#if parsed.Returns.Doc}<MarkdownDoc source={linkify(parsed.Returns.Doc, 'Returns.Doc')} />{/if}
      </div>
    {/if}

    {#if parsed.Yields}
      <div class="rounded border border-line/40 bg-bg-elev/40 px-3 py-2">
        <p class="text-[10px] uppercase tracking-wide text-fg-dim font-medium mb-1">
          yields{#if parsed.Yields.Type}
            <span class="ml-2 text-fg-mute font-mono normal-case">{parsed.Yields.Type}</span>
          {/if}
        </p>
        {#if parsed.Yields.Doc}<MarkdownDoc source={linkify(parsed.Yields.Doc, 'Yields.Doc')} />{/if}
      </div>
    {/if}

    {#if parsed.Examples && parsed.Examples.length > 0}
      <div class="flex flex-col gap-2">
        <p class="text-[10px] uppercase tracking-wide text-fg-dim font-medium">
          example{parsed.Examples.length > 1 ? 's' : ''}
        </p>
        {#each parsed.Examples as ex, i (i)}
          <pre
            class="rounded border border-line/60 bg-bg-elev/50 px-3 py-2 text-[12px] font-mono overflow-x-auto whitespace-pre">{ex.Code}</pre>
        {/each}
      </div>
    {/if}

    {#if parsed.Note}
      <div class="rounded border border-accent/30 bg-accent/5 px-3 py-2">
        <p class="text-[10px] uppercase tracking-wide text-accent font-medium mb-1">
          note
        </p>
        <MarkdownDoc source={linkify(parsed.Note, 'Note')} />
      </div>
    {/if}

    {#if parsed.Chips && parsed.Chips.length > 0}
      <div class="flex flex-wrap gap-1.5 items-baseline">
        <span class="text-[10px] uppercase tracking-wide text-fg-dim font-medium">
          referenced
        </span>
        {#each parsed.Chips as chip (chip.href)}
          {@const mod = moduleNameFromHref(chip.href)}
          {#if mod}
            <HoverCard moduleName={mod}>
              <a
                href={chip.href}
                class="font-mono text-[11px] px-1.5 py-0.5 rounded border border-line/60 bg-bg-elev/40 text-fg-mute hover:text-accent hover:border-accent/40"
                title={chip.title}
              >
                {chip.label}
              </a>
            </HoverCard>
          {:else}
            <a
              href={chip.href}
              class="font-mono text-[11px] px-1.5 py-0.5 rounded border border-line/60 bg-bg-elev/40 text-fg-mute hover:text-accent hover:border-accent/40"
              title={chip.title}
            >
              {chip.label}
            </a>
          {/if}
        {/each}
      </div>
    {/if}

    {#if parsed.Raises && parsed.Raises.length > 0}
      <!--
        Raises is rare in Starlark but the parser supports it for
        completeness; render compactly as a definition list.
      -->
      <div class="text-[12px]">
        <p class="text-[10px] uppercase tracking-wide text-fg-dim font-medium mb-1">
          raises
        </p>
        <ul class="font-mono">
          {#each parsed.Raises as r (r.Type)}
            <li class="text-fg-mute">
              <span class="text-fg">{r.Type}</span>{#if r.Doc} — {r.Doc}{/if}
            </li>
          {/each}
        </ul>
      </div>
    {/if}
  </div>
{/if}
