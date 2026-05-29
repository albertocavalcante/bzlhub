<script lang="ts">
  import { compatCheck, type CompatResult } from '$api/client';
  import MarkdownDoc from '$components/MarkdownDoc.svelte';
  import { moduleVersionHref } from '$lib/links';
  import { page } from '$app/state';
  import { readParam, writeParam, boolField } from '$lib/url-state';

  // Input state. Default placeholder shows the minimum-viable shape;
  // most users will paste their real file over it.
  const SAMPLE = `module(name = "my_workspace", version = "0.1.0")

bazel_dep(name = "rules_go", version = "0.40.0")
bazel_dep(name = "bazel_skylib", version = "1.5.0")
bazel_dep(name = "rules_python", version = "0.27.0")
`;

  // The MODULE.bazel body itself is NOT in the URL — a whole file
  // doesn't fit in a query string, and Plan 14 explicitly carves out
  // complex inputs to POST bodies (acknowledged trade-off: this page
  // is not fully shareable). The two toggle settings ARE shareable
  // so they go in the URL.
  let body = $state(SAMPLE);
  let includeDev = $state<boolean>(readParam(page.url, 'dev', boolField));
  let breakingOnly = $state<boolean>(readParam(page.url, 'breaking', boolField));
  let result = $state<CompatResult | null>(null);
  let loading = $state(false);
  let error = $state<string | null>(null);

  // URL ↔ state for the toggles.
  $effect(() => {
    const d = readParam(page.url, 'dev', boolField);
    if (d !== includeDev) includeDev = d;
  });
  $effect(() => {
    const b = readParam(page.url, 'breaking', boolField);
    if (b !== breakingOnly) breakingOnly = b;
  });
  $effect(() => {
    writeParam(page.url, 'dev', includeDev, boolField);
  });
  $effect(() => {
    writeParam(page.url, 'breaking', breakingOnly, boolField);
  });

  async function run() {
    if (!body.trim()) return;
    loading = true;
    error = null;
    result = null;
    try {
      result = await compatCheck(body, { includeDev });
    } catch (e: unknown) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      loading = false;
    }
  }

  function copyPlan() {
    if (!result?.plan_markdown) return;
    void navigator.clipboard.writeText(result.plan_markdown);
  }

  function downloadPlan() {
    if (!result?.plan_markdown) return;
    const blob = new Blob([result.plan_markdown], { type: 'text/markdown' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = 'compat-plan.md';
    a.click();
    URL.revokeObjectURL(url);
  }

  // Plan 06: download the migrate.sh script. Server emits it only
  // when at least one finding has a clean buildozer codemod — UI
  // hides the button otherwise.
  function downloadMigrateShell() {
    if (!result?.plan_shell) return;
    const blob = new Blob([result.plan_shell], { type: 'text/x-shellscript' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = 'migrate.sh';
    a.click();
    URL.revokeObjectURL(url);
  }

  const visibleDeps = $derived(
    breakingOnly && result
      ? result.deps.filter((d) => d.breaking_count > 0)
      : (result?.deps ?? []),
  );

  // File-drop input — operators routinely paste their MODULE.bazel
  // straight from the editor; supporting drag-and-drop too matches
  // peer-registry analyzer UX.
  async function onFileDrop(e: DragEvent) {
    e.preventDefault();
    const f = e.dataTransfer?.files?.[0];
    if (!f) return;
    body = await f.text();
  }
</script>

<svelte:head>
  <title>Compatibility check — canopy</title>
</svelte:head>

<div class="flex flex-col gap-6">
  <header class="flex flex-col gap-2">
    <h1 class="font-mono text-2xl text-fg tracking-tight">compatibility check</h1>
    <p class="text-[13px] text-fg-mute max-w-2xl">
      Paste a <code class="font-mono text-fg">MODULE.bazel</code>. Canopy diffs
      every <code class="font-mono text-fg">bazel_dep</code> against the latest
      indexed version and reports what breaks. Stays local — no network calls
      from your input.
    </p>
  </header>

  <section class="flex flex-col gap-2">
    <label class="text-[11px] uppercase tracking-wide text-fg-dim" for="module-bazel">
      MODULE.bazel
    </label>
    <textarea
      id="module-bazel"
      bind:value={body}
      ondragover={(e) => e.preventDefault()}
      ondrop={onFileDrop}
      spellcheck="false"
      class="font-mono text-[12px] bg-bg-elev border border-line rounded-md p-3 min-h-[260px] resize-y focus:outline-none focus:border-accent"
    ></textarea>
    <div class="flex items-center gap-3 text-[12px] text-fg-mute">
      <label class="flex items-center gap-1.5 cursor-pointer">
        <input type="checkbox" bind:checked={includeDev} class="accent-accent" />
        include dev dependencies
      </label>
      <button
        type="button"
        onclick={run}
        disabled={loading || !body.trim()}
        class="ml-auto px-3 py-1.5 rounded-md border border-accent/60 text-accent hover:bg-accent/10 disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer text-[12px] font-mono"
      >
        {loading ? 'analyzing…' : 'analyze'}
      </button>
    </div>
  </section>

  {#if error}
    <div
      class="rounded-md border border-err/30 bg-err/10 px-3 py-2 text-sm text-err"
      role="alert"
    >
      {error}
    </div>
  {/if}

  {#if result}
    <section
      class="flex items-center gap-4 text-[12px] text-fg-mute border-y border-line py-3"
      data-testid="compat-summary"
    >
      <span>
        <span class="text-fg font-mono">{result.summary.total_deps}</span> dep{result.summary.total_deps === 1 ? '' : 's'} analyzed
      </span>
      {#if result.summary.breaking_deps > 0}
        <span>
          <span class="text-err font-mono">{result.summary.breaking_deps}</span> with breaking changes
        </span>
      {/if}
      {#if result.summary.missing_from_corpus > 0}
        <span>
          <span class="text-warn font-mono">{result.summary.missing_from_corpus}</span> not in canopy
        </span>
      {/if}
      {#if result.summary.already_latest > 0}
        <span>
          <span class="text-fg font-mono">{result.summary.already_latest}</span> already on latest
        </span>
      {/if}
      <span class="ml-auto flex items-center gap-2">
        <label class="flex items-center gap-1.5 cursor-pointer">
          <input type="checkbox" bind:checked={breakingOnly} class="accent-accent" />
          breaking only
        </label>
        <button
          type="button"
          onclick={copyPlan}
          class="px-2 py-1 rounded border border-line hover:border-accent hover:text-accent cursor-pointer"
          title="Copy markdown plan to clipboard"
        >copy plan</button>
        <button
          type="button"
          onclick={downloadPlan}
          class="px-2 py-1 rounded border border-line hover:border-accent hover:text-accent cursor-pointer"
          title="Download markdown plan"
        >download .md</button>
        {#if result.plan_shell}
          <button
            type="button"
            onclick={downloadMigrateShell}
            class="px-2 py-1 rounded border border-accent/60 text-accent hover:bg-accent/10 cursor-pointer"
            title="Download migrate.sh — applies the codemod-able findings via buildozer. Review before running with --apply."
            aria-label="Download buildozer migration script"
          >download migrate.sh</button>
        {/if}
      </span>
    </section>

    <section class="flex flex-col gap-2" data-testid="compat-deps">
      {#each visibleDeps as d (d.name)}
        <article
          class="border rounded-md px-3 py-2 flex flex-col gap-1.5
            {d.breaking_count > 0 ? 'border-err/40 bg-err/5' : 'border-line bg-bg-elev/40'}"
        >
          <div class="flex items-baseline gap-3 flex-wrap text-[12px]">
            <a
              href={d.in_corpus ? moduleVersionHref(d.name, d.to_version ?? d.from_version) : `/modules/${encodeURIComponent(d.name)}`}
              class="font-mono text-[13px] text-fg hover:text-accent"
            >
              {d.name}
            </a>
            <span class="font-mono text-fg-dim">
              {d.from_version}
              {#if d.to_version && d.to_version !== d.from_version}
                → <span class="text-fg">{d.to_version}</span>
              {/if}
            </span>
            <span class="ml-auto flex items-center gap-2">
              {#if !d.in_corpus}
                <span
                  class="text-[10px] uppercase tracking-wide text-warn font-mono px-1.5 py-0.5 rounded border border-warn/40 bg-warn/10"
                  title="canopy hasn't ingested this module yet"
                >not indexed</span>
              {:else if d.same_version}
                <span class="text-[10px] uppercase tracking-wide text-fg-dim font-mono">
                  on latest
                </span>
              {:else if d.breaking_count > 0}
                <span
                  class="text-[10px] uppercase tracking-wide text-err font-mono"
                  title="number of structural-break findings from the diff"
                >
                  {d.breaking_count} breaking
                </span>
              {:else}
                <span class="text-[10px] uppercase tracking-wide text-fg-dim font-mono">
                  clean
                </span>
              {/if}
            </span>
          </div>
          {#if d.findings && d.findings.length > 0}
            <ul class="flex flex-col gap-1 mt-1">
              {#each d.findings as f, i (i)}
                <li class="text-[12px] text-fg-mute">
                  <span class="font-mono text-err">{f.kind}</span>
                  {#if f.symbol}
                    <span class="font-mono text-fg">{f.symbol}</span>
                  {/if}
                  {#if f.detail}
                    <span class="text-fg-dim">· <span class="font-mono">{f.detail}</span></span>
                  {/if}
                  <span class="ml-1">{f.reason}</span>
                  {#if f.hint}
                    <div class="text-fg-dim mt-0.5 ml-3">↳ {f.hint}</div>
                  {/if}
                  {#if f.codemod}
                    <div class="mt-0.5 ml-3 flex items-baseline gap-2">
                      <code class="font-mono text-[11px] text-accent break-all">{f.codemod}</code>
                      <button
                        type="button"
                        class="text-[10px] text-fg-dim hover:text-accent transition-colors shrink-0"
                        title="copy codemod to clipboard"
                        aria-label="copy codemod"
                        onclick={() => void navigator.clipboard.writeText(f.codemod ?? '')}
                      >copy</button>
                    </div>
                  {/if}
                </li>
              {/each}
            </ul>
          {/if}
        </article>
      {/each}
    </section>

    <details class="flex flex-col gap-2 border border-line rounded-md">
      <summary class="px-3 py-2 cursor-pointer text-[12px] text-fg-mute list-none">
        Markdown plan preview
      </summary>
      <div class="px-3 pb-3 border-t border-line/60">
        <MarkdownDoc source={result.plan_markdown} />
      </div>
    </details>
  {/if}
</div>
