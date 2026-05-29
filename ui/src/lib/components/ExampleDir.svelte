<!--
  ExampleDir — inline-rendered contents of one example/ directory.

  Lazy-fetches /api/v1/modules/{m}/versions/{v}/example-files?dir=X on expand.
  Renders text files as <pre><code> blocks; binaries (or anything
  too large) get a "view in code-nav" link in place of the body.

  Why lazy-load: example directories sometimes have a dozen files
  totaling 100KB+ — too much to bundle eagerly into every module
  page load. The user explicitly opens this section when they want
  to see scaffolding code.
-->
<script lang="ts">
  import { paths } from '$lib/api/paths';
  import { base } from '$app/paths';
  import { codeNavFileHref } from '$lib/links';

  // Shiki is heavy (~500KB) but already bundled for other canopy
  // surfaces. Lazy-import here so the homepage bundle stays untouched
  // — readers who never open an example pay nothing.
  type Highlighter = (code: string, opts: { lang: string; theme: string }) => Promise<string>;
  let codeToHtml: Highlighter | null = null;
  async function highlight(code: string, lang: string): Promise<string> {
    if (!codeToHtml) {
      const m = await import('shiki');
      codeToHtml = m.codeToHtml as Highlighter;
    }
    try {
      return await codeToHtml(code, { lang, theme: 'github-light' });
    } catch {
      // Unknown lang — fall back to plain HTML-escaped <pre>. Better
      // than failing the whole render for one unrecognized extension.
      return `<pre><code>${escapeHtml(code)}</code></pre>`;
    }
  }
  function escapeHtml(s: string): string {
    return s
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;');
  }

  // langForPath maps a file path → shiki language ID. Bazel files
  // (BUILD, BUILD.bazel, MODULE.bazel, .bzl, WORKSPACE) all map to
  // starlark; the rest follow standard extensions. Unknown paths
  // get 'text' which shiki renders as plain.
  function langForPath(p: string): string {
    const lower = p.toLowerCase();
    const base = lower.substring(lower.lastIndexOf('/') + 1);
    if (base === 'build' || base === 'build.bazel' || base === 'module.bazel' ||
        base === 'workspace' || base === 'workspace.bazel') return 'starlark';
    const dot = lower.lastIndexOf('.');
    if (dot < 0) return 'text';
    const ext = lower.substring(dot + 1);
    const map: Record<string, string> = {
      bzl: 'starlark',
      py: 'python',
      sh: 'bash',
      md: 'markdown',
      yaml: 'yaml',
      yml: 'yaml',
      json: 'json',
      toml: 'toml',
      go: 'go',
      ts: 'typescript',
      js: 'javascript',
      bazelrc: 'shellscript',
      bazelversion: 'text',
    };
    return map[ext] ?? 'text';
  }

  interface ExampleFile {
    path: string;
    size: number;
    bytes?: string;
    truncated?: boolean;
  }

  interface ExampleDirContents {
    dir: string;
    files: ExampleFile[];
    truncated?: boolean;
  }

  interface Props {
    module: string;
    version: string;
    /** Top-level example dir name (e.g. "example", "examples"). */
    dir: string;
  }

  let { module, version, dir }: Props = $props();

  // Three-state machine: idle → loading → loaded | error. On expand
  // we kick off the fetch once and cache the result.
  let state = $state<
    | { kind: 'idle' }
    | { kind: 'loading' }
    | { kind: 'loaded'; contents: ExampleDirContents }
    | { kind: 'error'; message: string }
  >({ kind: 'idle' });

  async function load() {
    if (state.kind !== 'idle') return;
    state = { kind: 'loading' };
    try {
      const url = new URL(
        `${base}${paths.exampleFiles(module, version)}`,
        window.location.origin,
      );
      url.searchParams.set('dir', dir);
      const res = await fetch(url);
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const contents = (await res.json()) as ExampleDirContents;
      state = { kind: 'loaded', contents };
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
  <summary class="cursor-pointer px-3 py-2 text-[13px] flex items-baseline gap-2 list-none">
    <span class="font-mono text-fg">{dir}/</span>
    <span class="text-[11px] text-fg-dim">expand to view files</span>
    <a
      href={codeNavFileHref(module, version, dir)}
      class="ml-auto text-[11px] text-fg-dim hover:text-accent font-mono"
      title="Open in code-nav"
      onclick={(e) => e.stopPropagation()}
    >
      browse →
    </a>
  </summary>

  <div class="px-3 pb-3 border-t border-line/60">
    {#if state.kind === 'loading'}
      <p class="text-[11px] text-fg-dim italic mt-2">loading…</p>
    {:else if state.kind === 'error'}
      <p class="text-[11px] text-err mt-2">Couldn't load example: {state.message}</p>
    {:else if state.kind === 'loaded'}
      {#if state.contents.files.length === 0}
        <p class="text-[11px] text-fg-dim italic mt-2">no files</p>
      {:else}
        <div class="flex flex-col gap-3 mt-2">
          {#each state.contents.files as f (f.path)}
            <div>
              <div class="flex items-baseline gap-2 mb-1">
                <a
                  href={codeNavFileHref(module, version, f.path)}
                  class="text-[12px] font-mono text-fg-mute hover:text-accent"
                >
                  {f.path}
                </a>
                <span class="text-[10px] text-fg-dim">{formatBytes(f.size)}</span>
              </div>
              {#if f.bytes !== undefined}
                {#await highlight(f.bytes, langForPath(f.path))}
                  <!-- shiki loads lazily on first expand; show plain
                       text while it resolves so the user isn't
                       blocked. -->
                  <pre
                    class="rounded border border-line/40 bg-bg-elev/60 px-3 py-2 text-[11px] font-mono overflow-x-auto whitespace-pre">{f.bytes}</pre>
                {:then html}
                  <!--
                    shiki returns a styled <pre> with inline styles —
                    drop into the layout via {@html} and constrain
                    overflow via the wrapping div.
                  -->
                  <div
                    class="shiki-wrap rounded border border-line/40 overflow-x-auto text-[11px]"
                  >{@html html}</div>
                {:catch}
                  <pre
                    class="rounded border border-line/40 bg-bg-elev/60 px-3 py-2 text-[11px] font-mono overflow-x-auto whitespace-pre">{f.bytes}</pre>
                {/await}
              {:else}
                <p class="text-[11px] text-fg-dim italic">
                  {f.truncated
                    ? 'too large to inline — open in code-nav for the full file'
                    : 'binary or unsupported type — open in code-nav to view'}
                </p>
              {/if}
            </div>
          {/each}
          {#if state.contents.truncated}
            <p class="text-[11px] text-fg-dim italic">
              more files in this directory — open code-nav for the full tree
            </p>
          {/if}
        </div>
      {/if}
    {/if}
  </div>
</details>

<style>
  /*
    Shiki emits inline styles for the highlighted lines but leaves
    the wrapping <pre> with its default browser margin + no
    horizontal scroll. Override at the .shiki-wrap container so the
    block fits inside the example-card frame.
  */
  .shiki-wrap :global(pre) {
    margin: 0;
    padding: 0.5rem 0.75rem;
    background: var(--color-bg-elev, #f6f6f6);
    overflow-x: auto;
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  }
  .shiki-wrap :global(pre code) {
    font-family: inherit;
    font-size: inherit;
  }
</style>

<script lang="ts" module>
  // Tiny human-readable byte formatter. Kept module-scoped so each
  // ExampleDir instance shares the same function reference.
  export function formatBytes(n: number): string {
    if (n < 1024) return `${n} B`;
    if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
    return `${(n / (1024 * 1024)).toFixed(1)} MB`;
  }
</script>
