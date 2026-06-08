<script lang="ts">
  import { page } from '$app/state';
  import type { RuleSpec, ProviderSpec, MacroSpec } from '$api/types';
  import { moduleVersion } from '$lib/state/moduleVersion.svelte';
  import { codeNavFileHref } from '$lib/links';
  import MarkdownDoc from '$components/MarkdownDoc.svelte';
  import RuleCard from '$components/RuleCard.svelte';
  import StardocBody from '$components/StardocBody.svelte';

  // Read the shared report from the layout-level loader; no fetch
  // here. Loading and error states render in the layout (well —
  // actually the Overview page handles them today; future work could
  // hoist that to layout once Phase B settles).
  const report = $derived(moduleVersion.report);
  const loading = $derived(moduleVersion.loading);
  const error = $derived(moduleVersion.error);

  // Private-symbol toggle is page-local: the user might want
  // different visibility per tab. Default is "hide private".
  let showPrivate = $state(false);

  const publicRules = $derived<RuleSpec[]>(
    report?.rules?.filter((r: RuleSpec) => showPrivate || !r.private) ?? [],
  );
  const publicProviders = $derived<ProviderSpec[]>(
    report?.providers?.filter((p: ProviderSpec) => showPrivate || !p.private) ?? [],
  );
  // P5: providers can legitimately repeat across files (rules_cc
  // toolchains). Flag dupes so the UI prepends a basename to the
  // affordance — keeps two same-name cards visually distinct.
  const duplicateProviderNames = $derived.by(() => {
    const seen = new Map<string, number>();
    for (const p of publicProviders) {
      seen.set(p.name, (seen.get(p.name) ?? 0) + 1);
    }
    const out = new Set<string>();
    for (const [name, n] of seen) {
      if (n > 1) out.add(name);
    }
    return out;
  });
  const publicMacros = $derived<MacroSpec[]>(report?.macros ?? []);

  function fileBasename(path: string): string {
    const i = path.lastIndexOf('/');
    return i < 0 ? path : path.slice(i + 1);
  }

  // Total documented symbol count drives the "empty" message when a
  // module exposes only structural data (deps, metadata) but no
  // Starlark surface — e.g. zlib mirrored from BCR.
  const totalSymbols = $derived(
    (report?.rules?.length ?? 0) +
      (report?.providers?.length ?? 0) +
      (report?.macros?.length ?? 0) +
      (report?.repository_rules?.length ?? 0) +
      (report?.module_extensions?.length ?? 0) +
      (report?.toolchains?.length ?? 0),
  );
</script>

<svelte:head>
  <title>
    {report ? `${report.name}@${report.version}` : 'module'} · docs — bzlhub
  </title>
</svelte:head>

{#if loading}
  <div class="skeleton h-64 w-full"></div>
{:else if error}
  <!--
    Documentation tab doesn't render the friendly 404 — that lives on
    Overview where the ingest UX makes sense. Surfacing a flat error
    here is fine because users can only reach this route after the
    layout has resolved (no direct landing without the layout
    loading).
  -->
  <div
    class="rounded-md border border-err/30 bg-err/10 px-3 py-2 text-sm text-err"
    role="alert"
  >
    {error}
  </div>
{:else if report}
  <article class="flex flex-col gap-8">
    {#if totalSymbols === 0}
      <p class="text-[13px] text-fg-dim italic">
        No Starlark surface in this module. BCR wraps it with
        registry-level metadata only.
      </p>
    {/if}

    <section class="flex flex-col gap-3">
      <div class="flex items-baseline gap-3">
        <h2 class="text-xs uppercase tracking-wide text-fg-dim">
          rules · {publicRules.length}
          {#if (report.rules?.length ?? 0) !== publicRules.length}
            <span class="ml-1 normal-case tracking-normal text-fg-mute/80">
              ({(report.rules?.length ?? 0) - publicRules.length} private hidden)
            </span>
          {/if}
        </h2>
        <label class="text-[11px] text-fg-dim flex items-center gap-1.5 ml-auto cursor-pointer">
          <input type="checkbox" bind:checked={showPrivate} class="accent-accent" />
          show private
        </label>
      </div>
      {#if publicRules.length === 0}
        <p class="text-[12px] text-fg-dim italic">none</p>
      {:else}
        <div class="flex flex-col gap-1.5">
          {#each publicRules as rule (rule.name + rule.provenance.file)}
            <RuleCard
              {rule}
              module={page.params.name ?? ''}
              version={page.params.version ?? ''}
              parsedDoc={report.parsed_docs?.[rule.name]}
            />
          {/each}
        </div>
      {/if}
    </section>

    {#if publicProviders.length > 0}
      <section class="flex flex-col gap-3">
        <h2 class="text-xs uppercase tracking-wide text-fg-dim">
          providers · {publicProviders.length}
        </h2>
        <ul class="grid grid-cols-1 sm:grid-cols-2 gap-1.5">
          {#each publicProviders as p (p.name + p.provenance.file)}
            <li
              id={p.name}
              class="border border-line rounded-md bg-bg-elev/50 px-3 py-2 scroll-mt-20"
            >
              <div class="flex items-baseline gap-2">
                <span class="font-mono text-[13px] text-fg">{p.name}</span>
                {#if duplicateProviderNames.has(p.name)}
                  <span
                    class="text-[10px] uppercase tracking-wide text-fg-dim font-mono"
                    title="another provider in this module shares this name; basename shown for disambiguation"
                  >
                    {fileBasename(p.provenance.file)}
                  </span>
                {/if}
                <a
                  href={codeNavFileHref(page.params.name ?? "", page.params.version ?? "", p.provenance.file, p.provenance.start_row)}
                  class="ml-auto text-[11px] text-fg-dim hover:text-accent font-mono truncate"
                  title="Open in code-nav"
                >
                  {p.provenance.file}:{p.provenance.start_row}
                </a>
                <a
                  href={`/modules/${encodeURIComponent(page.params.name ?? '')}/${encodeURIComponent(page.params.version ?? '')}/consumers/${encodeURIComponent(p.name)}`}
                  class="text-[11px] text-fg-dim hover:text-accent font-mono"
                  title="Find every call site of this provider across bzlhub's indexed corpus"
                >
                  consumers →
                </a>
              </div>
              {#if p.fields && p.fields.length > 0}
                <div class="text-[12px] text-fg-mute font-mono mt-1">
                  fields: {p.fields.join(', ')}
                </div>
              {/if}
              {#if report.parsed_docs?.[p.name] || p.doc}
                <div class="mt-1">
                  <StardocBody parsed={report.parsed_docs?.[p.name]} raw={p.doc} />
                </div>
              {/if}
            </li>
          {/each}
        </ul>
      </section>
    {/if}

    {#if report.repository_rules && report.repository_rules.length > 0}
      <section class="flex flex-col gap-3">
        <h2 class="text-xs uppercase tracking-wide text-fg-dim">
          repository rules · {report.repository_rules.length}
        </h2>
        <ul class="grid grid-cols-1 sm:grid-cols-2 gap-1.5">
          {#each report.repository_rules as r (r.name + r.provenance.file)}
            <!--
              Bare-name anchor target ("#cc_binary") for backward compat
              with deep-links elsewhere in the UI. <span> rather than
              <a> because we never want to navigate FROM here — the
              element exists only as a scroll-into-view target.
              svelte-check correctly flags href-less <a> as a11y issue;
              <span id="..."> is a valid named anchor in HTML5.
            -->
            <span id={r.name} class="scroll-mt-20" aria-hidden="true"></span>
            <li
              id={`repo-rule-${r.name}`}
              class="border border-line rounded-md bg-bg-elev/50 px-3 py-2 flex items-baseline gap-2 scroll-mt-20"
            >
              <span class="font-mono text-[13px] text-fg">{r.name}</span>
              <a
                href={codeNavFileHref(page.params.name ?? "", page.params.version ?? "", r.provenance.file, r.provenance.start_row)}
                class="ml-auto text-[11px] text-fg-dim hover:text-accent font-mono truncate"
                title="Open in code-nav"
              >
                {r.provenance.file}:{r.provenance.start_row}
              </a>
              <a
                href={`/modules/${encodeURIComponent(page.params.name ?? '')}/${encodeURIComponent(page.params.version ?? '')}/consumers/${encodeURIComponent(r.name)}`}
                class="text-[11px] text-fg-dim hover:text-accent font-mono"
                title="Find every call site of this repository rule across bzlhub's indexed corpus"
              >
                consumers →
              </a>
            </li>
          {/each}
        </ul>
      </section>
    {/if}

    {#if report.module_extensions && report.module_extensions.length > 0}
      <section class="flex flex-col gap-3">
        <h2 class="text-xs uppercase tracking-wide text-fg-dim">
          module extensions · {report.module_extensions.length}
        </h2>
        <ul class="grid grid-cols-1 sm:grid-cols-2 gap-1.5">
          {#each report.module_extensions as m (m.name + m.provenance.file)}
            <li
              id={m.name}
              class="border border-line rounded-md bg-bg-elev/50 px-3 py-2 flex items-baseline gap-2 scroll-mt-20"
            >
              <span class="font-mono text-[13px] text-fg">{m.name}</span>
              <a
                href={codeNavFileHref(page.params.name ?? "", page.params.version ?? "", m.provenance.file, m.provenance.start_row)}
                class="ml-auto text-[11px] text-fg-dim hover:text-accent font-mono truncate"
                title="Open in code-nav"
              >
                {m.provenance.file}:{m.provenance.start_row}
              </a>
              <a
                href={`/modules/${encodeURIComponent(page.params.name ?? '')}/${encodeURIComponent(page.params.version ?? '')}/consumers/${encodeURIComponent(m.name)}`}
                class="text-[11px] text-fg-dim hover:text-accent font-mono"
                title="Find every MODULE.bazel that invokes this extension"
              >
                consumers →
              </a>
            </li>
          {/each}
        </ul>
      </section>
    {/if}

    {#if report.toolchains && report.toolchains.length > 0}
      <section class="flex flex-col gap-3">
        <h2 class="text-xs uppercase tracking-wide text-fg-dim">
          toolchain types · {report.toolchains.length}
        </h2>
        <ul class="font-mono text-[13px] flex flex-col gap-1">
          {#each report.toolchains as t (t.name + t.provenance.file)}
            <li id={t.name} class="text-fg-mute scroll-mt-20">
              <span class="text-fg">{t.name}</span>
              <a
                href={codeNavFileHref(page.params.name ?? "", page.params.version ?? "", t.provenance.file, t.provenance.start_row)}
                class="text-fg-dim hover:text-accent ml-2 text-[11px]"
                title="Open in code-nav"
              >{t.provenance.file}:{t.provenance.start_row}</a>
            </li>
          {/each}
        </ul>
      </section>
    {/if}

    {#if publicMacros.length > 0}
      <section class="flex flex-col gap-3">
        <h2 class="text-xs uppercase tracking-wide text-fg-dim">
          macros · {publicMacros.length}
        </h2>
        <!--
          Each macro renders as a collapsible card. Macros with no
          doc still get a card so they're targetable (anchor IDs
          preserved) and consistent in the page hierarchy.
        -->
        <div class="flex flex-col gap-1.5">
          {#each publicMacros.slice(0, 60) as m (m.name + m.provenance.file)}
            {@const pd = report.parsed_docs?.[m.name]}
            <details
              id={m.name}
              class="border border-line rounded-md bg-bg-elev/50 scroll-mt-20"
            >
              <summary
                class="flex items-baseline gap-3 px-3 py-2 cursor-pointer list-none"
              >
                <span class="font-mono text-[13px] text-fg">{m.name}</span>
                <span class="text-[11px] text-fg-dim">
                  ({(m.params ?? []).join(', ')})
                </span>
                {#if page.params.name && page.params.version}
                  <a
                    href={codeNavFileHref(page.params.name ?? "", page.params.version ?? "", m.provenance.file, m.provenance.start_row)}
                    class="ml-auto text-[11px] text-fg-dim hover:text-accent font-mono"
                    title="Open in code-nav"
                    onclick={(e) => e.stopPropagation()}
                  >
                    {m.provenance.file}:{m.provenance.start_row}
                  </a>
                  <a
                    href={`/modules/${encodeURIComponent(page.params.name)}/${encodeURIComponent(page.params.version)}/consumers/${encodeURIComponent(m.name)}`}
                    class="text-[11px] text-fg-dim hover:text-accent font-mono"
                    title="Find every call site of this macro across bzlhub's indexed corpus"
                    onclick={(e) => e.stopPropagation()}
                  >
                    consumers →
                  </a>
                {/if}
              </summary>
              <div class="px-3 pb-3 pt-1 border-t border-line/60">
                {#if pd}
                  <StardocBody parsed={pd} />
                {:else if m.doc}
                  <MarkdownDoc source={m.doc} />
                {:else}
                  <p class="text-[12px] text-fg-dim italic">no doc string</p>
                {/if}
              </div>
            </details>
          {/each}
          {#if publicMacros.length > 60}
            <p class="text-[11px] text-fg-dim">
              showing first 60 of {publicMacros.length}
            </p>
          {/if}
        </div>
      </section>
    {/if}
  </article>
{/if}
