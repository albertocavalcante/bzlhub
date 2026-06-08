<!--
  InstallSnippet — copyable "use this module" card.

  Two rows: the .bazelrc `--registry` line (so this bzlhub instance
  becomes a Bazel-resolvable registry), and the MODULE.bazel
  `bazel_dep(...)` line. Together they answer the conversion-moment
  question on /modules/<m>/<v>: "how do I actually use this module
  via bzlhub?" — Plan 19 Idea C.

  The registry URL is read from window.location.origin so this
  works for any operator-hosted instance (bzlhub.com, an internal
  bzlhub.example.com, Harbor-style behind-VPN deployments).
  MODULE.bazel format only — Bzlmod is the answer in 2026; we don't
  ship a legacy WORKSPACE snippet.
-->
<script lang="ts">
  import { onMount } from 'svelte';

  interface Props {
    name: string;
    version: string;
  }
  let { name, version }: Props = $props();

  const moduleSnippet = $derived(`bazel_dep(name = "${name}", version = "${version}")`);

  // window.location.origin is only available client-side; SSR-safe
  // default is a placeholder users can spot. Adapter-static + SPA
  // means we always reach this in the browser before the user reads
  // the snippet, so the placeholder is never visible in practice.
  let origin = $state('https://your-bzlhub-instance');
  onMount(() => {
    origin = window.location.origin;
  });
  const registrySnippet = $derived(`common --registry=${origin}`);

  // Per-snippet copy state. Two cards, two timers.
  let copiedRegistry = $state(false);
  let copiedModule = $state(false);
  let registryTimer: ReturnType<typeof setTimeout> | null = null;
  let moduleTimer: ReturnType<typeof setTimeout> | null = null;

  async function copyRegistry() {
    try {
      await navigator.clipboard.writeText(registrySnippet);
      copiedRegistry = true;
      if (registryTimer) clearTimeout(registryTimer);
      registryTimer = setTimeout(() => (copiedRegistry = false), 2000);
    } catch {
      // Clipboard permission denied or no API — silently fail.
    }
  }
  async function copyModule() {
    try {
      await navigator.clipboard.writeText(moduleSnippet);
      copiedModule = true;
      if (moduleTimer) clearTimeout(moduleTimer);
      moduleTimer = setTimeout(() => (copiedModule = false), 2000);
    } catch {
      // ditto.
    }
  }
</script>

<div class="flex flex-col gap-1.5" data-testid="install-snippet">
  <div
    class="rounded-md border border-line bg-bg-elev/50 px-3 py-2 flex items-center gap-3"
  >
    <span
      class="text-[10px] uppercase tracking-wide text-fg-dim font-mono shrink-0"
      title="paste into your project's .bazelrc to resolve modules through this instance"
    >
      .bazelrc
    </span>
    <code class="flex-1 font-mono text-[12px] text-fg overflow-x-auto whitespace-nowrap">
      {registrySnippet}
    </code>
    <button
      type="button"
      onclick={copyRegistry}
      class="text-[11px] font-mono px-2 py-1 rounded border border-line hover:border-accent/60 hover:text-accent transition-colors cursor-pointer shrink-0
        {copiedRegistry ? 'border-ok/60 text-ok' : 'text-fg-mute'}"
      aria-live="polite"
      title="copy .bazelrc registry line"
    >
      {copiedRegistry ? '✓ copied' : 'copy'}
    </button>
  </div>
  <div
    class="rounded-md border border-line bg-bg-elev/50 px-3 py-2 flex items-center gap-3"
  >
    <span
      class="text-[10px] uppercase tracking-wide text-fg-dim font-mono shrink-0"
      title="paste into your MODULE.bazel"
    >
      MODULE.bazel
    </span>
    <code class="flex-1 font-mono text-[12px] text-fg overflow-x-auto whitespace-nowrap">
      {moduleSnippet}
    </code>
    <button
      type="button"
      onclick={copyModule}
      class="text-[11px] font-mono px-2 py-1 rounded border border-line hover:border-accent/60 hover:text-accent transition-colors cursor-pointer shrink-0
        {copiedModule ? 'border-ok/60 text-ok' : 'text-fg-mute'}"
      aria-live="polite"
      title="copy bazel_dep line"
    >
      {copiedModule ? '✓ copied' : 'copy'}
    </button>
  </div>
</div>
