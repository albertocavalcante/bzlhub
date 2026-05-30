<!--
  /mcp — wire any MCP-capable coding agent to this bzlhub instance.

  Three integration tabs (URL-bound per plan-19 Idea E):
    • Streamable HTTP (default) — generic .mcp.json / mcpServers config
      with a `url` field; works for any client that speaks Streamable
      HTTP.
    • Claude Code — the one-liner `claude mcp add --transport http`.
    • stdio — local canopy binary for sandboxed / fully-offline agents
      that can't reach the network.

  Live tool catalogue at the bottom — POSTs `tools/list` against this
  instance's /mcp so the page shows exactly what's registered, not a
  hand-maintained copy. Cheap (one request per page view, well below
  the per-IP rate limit).

  Origin is derived from window.location so this same component works
  on bzlhub.com, canopy.alberto.engineer, internal Harbor-style
  deployments — no per-deploy config.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { page } from '$app/state';
  import { goto } from '$app/navigation';

  type Tab = 'http' | 'claude-code' | 'stdio';
  const VALID_TABS: Tab[] = ['http', 'claude-code', 'stdio'];

  // URL-bound tab selection. Default: http (the path most readers
  // arriving from /about will care about). Drives a small history
  // entry so back/forward across tabs works.
  let activeTab = $state<Tab>('http');

  $effect(() => {
    const q = page.url.searchParams.get('client');
    if (q && (VALID_TABS as string[]).includes(q)) {
      activeTab = q as Tab;
    }
  });

  function selectTab(t: Tab) {
    activeTab = t;
    const url = new URL(page.url);
    url.searchParams.set('client', t);
    void goto(url.pathname + '?' + url.searchParams.toString(), {
      replaceState: false,
      keepFocus: true,
      noScroll: true,
    });
  }

  // Origin defaults to a visible placeholder pre-mount so SSR has
  // SOMETHING (the SvelteKit prerender pass touches this); the real
  // value lands as soon as the client hydrates, before the reader
  // has time to copy.
  let origin = $state('https://your-bzlhub-instance');
  onMount(() => {
    origin = window.location.origin;
  });
  const mcpURL = $derived(`${origin}/mcp`);

  // Snippet bodies — each $derived so they refresh once origin lands.
  const mcpJsonSnippet = $derived(`{
  "mcpServers": {
    "bzlhub": {
      "url": "${mcpURL}",
      "transport": "streamable-http"
    }
  }
}`);
  const claudeCodeSnippet = $derived(
    `claude mcp add --transport http bzlhub ${mcpURL}`,
  );
  const stdioSnippet = `{
  "mcpServers": {
    "canopy": {
      "command": "canopy",
      "args": ["mcp", "--db", "/path/to/canopy.db"]
    }
  }
}`;

  // ---- Copy-to-clipboard ------------------------------------------

  let copied = $state<Record<string, boolean>>({});
  const copyTimers: Record<string, ReturnType<typeof setTimeout>> = {};
  async function copy(key: string, text: string) {
    try {
      await navigator.clipboard.writeText(text);
      copied = { ...copied, [key]: true };
      if (copyTimers[key]) clearTimeout(copyTimers[key]);
      copyTimers[key] = setTimeout(() => {
        copied = { ...copied, [key]: false };
      }, 2000);
    } catch {
      // Clipboard API denied or unavailable — silently keep the code
      // block visible; the reader can select-all manually.
    }
  }

  // ---- Live tool catalogue ----------------------------------------
  //
  // POST a tools/list request to this instance's /mcp endpoint. When
  // the endpoint isn't enabled (CANOPY_MCP_HTTP_ENABLED=false on the
  // serving instance) the SPA fallback returns index.html which fails
  // JSON parse — we surface that as "MCP-over-HTTP not enabled on this
  // instance" rather than a stack trace.

  type ToolInfo = {
    name: string;
    description?: string;
  };
  let tools = $state<ToolInfo[] | null>(null);
  let toolsError = $state<string | null>(null);

  async function loadTools() {
    try {
      const res = await fetch('/mcp', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          Accept: 'application/json, text/event-stream',
        },
        body: JSON.stringify({
          jsonrpc: '2.0',
          method: 'tools/list',
          id: 1,
        }),
      });
      if (!res.ok) {
        if (res.status === 404 || res.status === 405) {
          toolsError = 'MCP-over-HTTP is not enabled on this instance.';
        } else if (res.status === 429) {
          toolsError = 'Rate limit hit while loading the tool list. Refresh in a moment.';
        } else {
          toolsError = `Unexpected status ${res.status} from /mcp.`;
        }
        return;
      }
      const ct = res.headers.get('content-type') ?? '';
      if (!ct.startsWith('application/json')) {
        // SPA fallback (flag off) would respond text/html.
        toolsError = 'MCP-over-HTTP is not enabled on this instance.';
        return;
      }
      const json = (await res.json()) as {
        result?: { tools?: ToolInfo[] };
      };
      tools = json.result?.tools ?? [];
    } catch (e) {
      toolsError = e instanceof Error ? e.message : String(e);
    }
  }

  onMount(() => {
    void loadTools();
  });
</script>

<svelte:head>
  <title>MCP · bzlhub</title>
</svelte:head>

<article class="mx-auto max-w-[860px] py-2">
  <header class="mb-6">
    <h1 class="font-mono text-base text-fg mb-2">bzlhub MCP server</h1>
    <p class="text-sm text-fg-mute leading-relaxed">
      bzlhub exposes a
      <a
        class="text-accent hover:underline"
        href="https://modelcontextprotocol.io"
        target="_blank"
        rel="noopener noreferrer">Model Context Protocol</a
      >
      server. Configure your coding agent to query the module index, hermeticity
      classifications, drift state, and source-nav directly from inside the session.
    </p>
  </header>

  <!-- Tab strip. Three tabs; the active state is also URL-bound so
       /mcp?client=claude-code deep-links into the Claude Code snippet. -->
  <div role="tablist" aria-label="MCP client setup" class="flex gap-1 border-b border-line mb-5">
    {#each [{ id: 'http', label: 'Streamable HTTP' }, { id: 'claude-code', label: 'Claude Code' }, { id: 'stdio', label: 'stdio (local)' }] as t (t.id)}
      <button
        type="button"
        role="tab"
        aria-selected={activeTab === t.id}
        class="px-3 py-2 text-sm font-mono border-b-2 -mb-px transition-colors"
        class:border-accent={activeTab === t.id}
        class:text-fg={activeTab === t.id}
        class:border-transparent={activeTab !== t.id}
        class:text-fg-mute={activeTab !== t.id}
        class:hover:text-fg={activeTab !== t.id}
        onclick={() => selectTab(t.id as Tab)}
      >
        {t.label}
      </button>
    {/each}
  </div>

  <!-- Per-tab content. Three siblings, conditionally rendered. Each
       carries: short framing, copyable snippet, and any per-client
       notes (versions, gotchas). -->

  {#if activeTab === 'http'}
    <section class="mb-8">
      <p class="text-sm text-fg-mute mb-3 leading-relaxed">
        Add this block to your client's MCP configuration file. Works with any
        client that speaks the Streamable HTTP transport (spec 2025-11-25).
      </p>
      <div class="rounded-md border border-line bg-bg-elev/60 overflow-hidden">
        <div class="flex items-center justify-between border-b border-line/60 px-3 py-1.5 text-[10px] uppercase tracking-wide text-fg-dim font-mono">
          <span>mcp config (.mcp.json or equivalent)</span>
          <button
            type="button"
            onclick={() => copy('http', mcpJsonSnippet)}
            class="text-fg-mute hover:text-fg cursor-pointer"
            aria-label="copy snippet"
          >
            {copied.http ? 'copied' : 'copy'}
          </button>
        </div>
        <pre class="font-mono text-[12px] text-fg px-3 py-2 overflow-x-auto leading-relaxed">{mcpJsonSnippet}</pre>
      </div>
      <p class="text-xs text-fg-dim mt-2">
        Endpoint URL: <code class="font-mono">{mcpURL}</code>.
        Stateless, no session header, no streaming — every request is
        an independent JSON-RPC call.
      </p>
    </section>
  {/if}

  {#if activeTab === 'claude-code'}
    <section class="mb-8">
      <p class="text-sm text-fg-mute mb-3 leading-relaxed">
        Run this once in your terminal. Claude Code persists the
        registration; subsequent sessions resolve the tools automatically.
      </p>
      <div class="rounded-md border border-line bg-bg-elev/60 overflow-hidden">
        <div class="flex items-center justify-between border-b border-line/60 px-3 py-1.5 text-[10px] uppercase tracking-wide text-fg-dim font-mono">
          <span>shell</span>
          <button
            type="button"
            onclick={() => copy('claude-code', claudeCodeSnippet)}
            class="text-fg-mute hover:text-fg cursor-pointer"
            aria-label="copy snippet"
          >
            {copied['claude-code'] ? 'copied' : 'copy'}
          </button>
        </div>
        <pre class="font-mono text-[12px] text-fg px-3 py-2 overflow-x-auto leading-relaxed">{claudeCodeSnippet}</pre>
      </div>
      <p class="text-xs text-fg-dim mt-2">
        Verify with <code class="font-mono">claude mcp list</code>; remove with
        <code class="font-mono">claude mcp remove bzlhub</code>.
      </p>
    </section>
  {/if}

  {#if activeTab === 'stdio'}
    <section class="mb-8">
      <p class="text-sm text-fg-mute mb-3 leading-relaxed">
        For local-process agents, sandboxed environments, or fully-offline
        workflows. Requires <code class="font-mono">canopy</code> on PATH and a
        populated SQLite index at the given path. Build from source or pull a
        prebuilt image — see the
        <a
          class="text-accent hover:underline"
          href="https://github.com/albertocavalcante/bzlhub/blob/main/docs/deployment/build-from-source.md"
          target="_blank"
          rel="noopener noreferrer">self-host guide</a
        >.
      </p>
      <div class="rounded-md border border-line bg-bg-elev/60 overflow-hidden">
        <div class="flex items-center justify-between border-b border-line/60 px-3 py-1.5 text-[10px] uppercase tracking-wide text-fg-dim font-mono">
          <span>mcp config — stdio variant</span>
          <button
            type="button"
            onclick={() => copy('stdio', stdioSnippet)}
            class="text-fg-mute hover:text-fg cursor-pointer"
            aria-label="copy snippet"
          >
            {copied.stdio ? 'copied' : 'copy'}
          </button>
        </div>
        <pre class="font-mono text-[12px] text-fg px-3 py-2 overflow-x-auto leading-relaxed">{stdioSnippet}</pre>
      </div>
      <p class="text-xs text-fg-dim mt-2">
        Replace <code class="font-mono">/path/to/canopy.db</code> with your
        local index file. Same tool catalogue as the HTTP transport — the
        registrar is shared.
      </p>
    </section>
  {/if}

  <!-- Live tool catalogue. Sourced from /mcp tools/list so it stays
       honest as tools are added or removed. -->
  <section>
    <h2 class="font-mono text-sm text-fg-mute uppercase tracking-wide mb-3">
      Tools exposed
      {#if tools !== null && !toolsError}<span class="text-fg-dim">({tools.length})</span>{/if}
    </h2>
    {#if toolsError}
      <p class="text-xs text-fg-mute">{toolsError}</p>
    {:else if tools === null}
      <p class="text-xs text-fg-dim">loading…</p>
    {:else if tools.length === 0}
      <p class="text-xs text-fg-mute">No tools registered on this instance.</p>
    {:else}
      <ul class="grid grid-cols-1 md:grid-cols-2 gap-x-6 gap-y-1.5 font-mono text-[12px]">
        {#each tools as t (t.name)}
          <li class="flex items-baseline gap-2">
            <code class="text-fg whitespace-nowrap">{t.name}</code>
            {#if t.description}
              <span
                class="text-fg-mute text-[11px] truncate"
                title={t.description}
              >
                {t.description.split(/[.\n]/)[0]}
              </span>
            {/if}
          </li>
        {/each}
      </ul>
    {/if}
  </section>
</article>
