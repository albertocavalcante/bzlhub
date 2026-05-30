<!--
  /about — narrative anchor for first-time visitors.

  Content lives in $lib/content/about.md (single source of truth,
  version-controlled). Loaded via Vite's `?raw` suffix and rendered
  through MarkdownDoc, the same CommonMark pipeline that renders
  rule + provider doc strings — so typography matches the rest of
  the site without bespoke CSS here.

  Voice + content rules: docs/plans/65-about-and-status-content-spec.md.
-->
<script lang="ts">
  import MarkdownDoc from '$components/MarkdownDoc.svelte';
  import aboutSource from '$lib/content/about.md?raw';
</script>

<article class="mx-auto max-w-[720px] py-4">
  <MarkdownDoc source={aboutSource} dedent={false} class="md-about" />
</article>

<style>
  /*
    /about is a long-form page; uplevel typography from MarkdownDoc's
    default (which is sized for inline doc strings inside rule cards).
    Scoped to .md-about so other MarkdownDoc consumers are unaffected.
  */
  article :global(.md-about) {
    font-size: 15px;
    line-height: 1.7;
    color: var(--color-fg, #111);
  }
  article :global(.md-about h1) {
    font-size: 1.8em;
    margin: 0 0 0.6em 0;
  }
  article :global(.md-about h2) {
    font-size: 1.25em;
    margin: 1.4em 0 0.4em 0;
  }
  article :global(.md-about p),
  article :global(.md-about ul),
  article :global(.md-about ol) {
    margin: 0 0 1em 0;
  }
</style>
