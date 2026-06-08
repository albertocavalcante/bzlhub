<!--
  MarkdownDoc — renders a Bazel doc string as CommonMark.

  Bazel's convention (formalized by Stardoc) is that the `doc=` field
  on rule(), provider(), attr.*, etc. is CommonMark. Today we render
  it as whitespace-pre-wrap plain text, which loses links, lists, code
  spans, headers, and emphasis. This component fixes that.

  Trust model
  -----------
  bzlhub is operator-controlled: the operator chooses which modules
  to ingest via /api/v1/actions/bump or `bzlhub ingest`. Doc strings flow into
  reports from the .bzl source we evaluate with the interpreter — so
  the XSS surface is "did I ingest a module that wrote evil HTML into
  a doc=string?" That requires the operator to ingest a hostile
  module, which is the same trust violation as the interpreter
  evaluating that module's code in the first place.

  Even so: defense in depth. Raw HTML tokens are stripped before the
  generated Markdown HTML is sanitized. The `{@html}` directive below
  only receives that sanitized output.
-->
<script lang="ts">
  import { renderMarkdownSafe } from '$lib/markdown/render';

  interface Props {
    /** Raw doc string from the ModuleReport / AttrSpec / ProviderSpec. */
    source: string | undefined;
    /**
     * When true (default), normalize indentation by stripping common
     * leading whitespace before parsing — handles the case where a
     * Bazel doc= literal is indented to match the surrounding
     * Starlark code, which CommonMark would otherwise treat as a
     * code block.
     */
    dedent?: boolean;
    /** Tailwind class applied to the wrapping div. */
    class?: string;
  }

  let { source, dedent = true, class: klass = '' }: Props = $props();

  const html = $derived.by(() => {
    return renderMarkdownSafe(source, { dedent });
  });
</script>

<!--
  The `prose` family is Tailwind's typography plugin if loaded;
  otherwise the inline @apply rules below provide a baseline so
  rendered docs aren't visually broken on installs without it.
-->
<div class="md-doc {klass}">{@html html}</div>

<style>
  /*
    Local typography. Scoped via .md-doc so we don't impact anything
    outside this component. Mirrors the existing card-body text size
    and the fg-mute color so docs feel like part of the rule card,
    not a foreign body.
  */
  .md-doc {
    font-size: 13px;
    line-height: 1.55;
    color: var(--color-fg-mute, #444);
  }
  .md-doc :global(p) {
    margin: 0 0 0.5em 0;
  }
  .md-doc :global(p:last-child) {
    margin-bottom: 0;
  }
  .md-doc :global(h1),
  .md-doc :global(h2),
  .md-doc :global(h3),
  .md-doc :global(h4) {
    font-weight: 600;
    color: var(--color-fg, #111);
    margin: 0.8em 0 0.3em 0;
    line-height: 1.25;
  }
  .md-doc :global(h1) { font-size: 1.15em; }
  .md-doc :global(h2) { font-size: 1.08em; }
  .md-doc :global(h3),
  .md-doc :global(h4) { font-size: 1em; }
  .md-doc :global(ul),
  .md-doc :global(ol) {
    margin: 0.3em 0 0.5em 0;
    padding-left: 1.2em;
  }
  .md-doc :global(li) {
    margin: 0.1em 0;
  }
  .md-doc :global(code) {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 0.9em;
    padding: 0.05em 0.3em;
    border-radius: 3px;
    background: var(--color-bg-elev, #f3f3f3);
    color: var(--color-fg, #111);
  }
  .md-doc :global(pre) {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 0.85em;
    padding: 0.6em 0.8em;
    border-radius: 4px;
    background: var(--color-bg-elev, #f3f3f3);
    overflow-x: auto;
    margin: 0.5em 0;
  }
  .md-doc :global(pre code) {
    padding: 0;
    background: transparent;
  }
  .md-doc :global(a) {
    color: var(--color-accent, #1a4fbf);
    text-decoration: none;
  }
  .md-doc :global(a:hover) {
    text-decoration: underline;
  }
  .md-doc :global(blockquote) {
    border-left: 3px solid var(--color-line, #ddd);
    padding-left: 0.8em;
    margin: 0.5em 0;
    color: var(--color-fg-dim, #666);
  }
  .md-doc :global(strong) {
    font-weight: 600;
    color: var(--color-fg, #111);
  }
  .md-doc :global(em) {
    font-style: italic;
  }
  .md-doc :global(hr) {
    border: 0;
    border-top: 1px solid var(--color-line, #ddd);
    margin: 0.8em 0;
  }
  .md-doc :global(table) {
    border-collapse: collapse;
    margin: 0.5em 0;
    font-size: 0.95em;
  }
  .md-doc :global(th),
  .md-doc :global(td) {
    border: 1px solid var(--color-line, #ddd);
    padding: 0.3em 0.6em;
    text-align: left;
  }
</style>
