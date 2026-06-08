<script lang="ts">
  import { page } from '$app/state';
  import { moduleVersion } from '$lib/state/moduleVersion.svelte';
  import { codeNavFileHref } from '$lib/links';
  import ExampleDir from '$components/ExampleDir.svelte';
  import HermeticityBadge from '$components/HermeticityBadge.svelte';

  // Testing tab surfaces the "how do I exercise / verify this
  // module" content: example projects + hermeticity findings (which
  // are roughly "what would break a hermetic build"). Pulls from the
  // shared layout store so we don't refetch.
  const report = $derived(moduleVersion.report);
  const loading = $derived(moduleVersion.loading);
  const error = $derived(moduleVersion.error);

  // Cap matches the original section: 100 findings is enough to scan
  // without DoS'ing the renderer on modules with thousands.
  const findingsCap = 100;
</script>

<svelte:head>
  <title>
    {report ? `${report.name}@${report.version}` : 'module'} · testing — bzlhub
  </title>
</svelte:head>

{#if loading}
  <div class="skeleton h-64 w-full"></div>
{:else if error}
  <div
    class="rounded-md border border-err/30 bg-err/10 px-3 py-2 text-sm text-err"
    role="alert"
  >
    {error}
  </div>
{:else if report}
  {@const hasExamples = (report.assets?.example_dirs?.length ?? 0) > 0}
  {@const hasFindings = (report.hermeticity?.findings?.length ?? 0) > 0}

  {#if !hasExamples && !hasFindings}
    <p class="text-[13px] text-fg-dim italic">
      No example dirs or hermeticity findings recorded for this version.
    </p>
  {/if}

  <article class="flex flex-col gap-8">
    {#if hasExamples && page.params.name && page.params.version}
      <!--
        Examples surface. Each root-level example dir is rendered as
        a collapsible <ExampleDir> card; expanding lazily fetches and
        inlines file contents (capped per-file + per-dir on the
        server). The full tree is still one click away via the
        "browse →" link to code-nav.
      -->
      <section class="flex flex-col gap-3" data-testid="examples-section">
        <h2 class="text-xs uppercase tracking-wide text-fg-dim">
          examples · {report.assets?.example_dirs?.length}
        </h2>
        <div class="flex flex-col gap-2">
          {#each report.assets?.example_dirs ?? [] as d (d)}
            <ExampleDir
              module={page.params.name}
              version={page.params.version}
              dir={d}
            />
          {/each}
        </div>
      </section>
    {/if}

    {#if hasFindings}
      <section class="flex flex-col gap-3">
        <h2 class="text-xs uppercase tracking-wide text-fg-dim">
          hermeticity findings · {report.hermeticity?.findings?.length}
        </h2>
        <div class="overflow-x-auto">
          <table class="w-full text-[12px] font-mono">
            <thead>
              <tr class="text-[10px] uppercase tracking-wide text-fg-dim">
                <th class="text-left font-medium py-1 pr-3">class</th>
                <th class="text-left font-medium py-1 pr-3">symbol</th>
                <th class="text-left font-medium py-1 pr-3">where</th>
                <th class="text-left font-medium py-1">reason</th>
              </tr>
            </thead>
            <tbody>
              {#each (report.hermeticity?.findings ?? []).slice(0, findingsCap) as f, i (i)}
                {@const findingHref = codeNavFileHref(
                  page.params.name ?? "",
                  page.params.version ?? "",
                  f.provenance.file,
                  f.provenance.start_row,
                )}
                <tr class="border-t border-line/40">
                  <td class="py-1 pr-3">
                    <HermeticityBadge class={f.class} compact />
                  </td>
                  <td class="py-1 pr-3">
                    <!--
                      Both symbol and where deep-link to the call site
                      in code-nav. The finding's `symbol` is a bare
                      name (http_archive, execute, …) we can't
                      SCIP-resolve from here; landing on the call line
                      lets the user click the identifier in source
                      view where cross-module dispatch takes over.
                    -->
                    <a
                      href={findingHref}
                      class="text-fg hover:text-accent hover:underline"
                      title="Open call site in code-nav"
                    >{f.symbol}</a>
                  </td>
                  <td class="py-1 pr-3">
                    <a
                      href={findingHref}
                      class="text-fg-mute hover:text-accent hover:underline"
                      title="Open in code-nav"
                    >{f.provenance.file}:{f.provenance.start_row}</a>
                  </td>
                  <td class="py-1 text-fg-mute leading-snug">{f.reason}</td>
                </tr>
              {/each}
            </tbody>
          </table>
          {#if (report.hermeticity?.findings?.length ?? 0) > findingsCap}
            <p class="text-[11px] text-fg-dim mt-2">
              showing first {findingsCap} of {report.hermeticity?.findings?.length}
            </p>
          {/if}
        </div>
      </section>
    {/if}
  </article>
{/if}
