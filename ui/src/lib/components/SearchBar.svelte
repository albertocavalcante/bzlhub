<script lang="ts">
  import { onMount } from 'svelte';

  let {
    value = $bindable(''),
    placeholder = 'search modules, rules, providers…',
    onSubmit,
  }: {
    value?: string;
    placeholder?: string;
    onSubmit?: (q: string) => void;
  } = $props();

  let input: HTMLInputElement | undefined = $state();

  // Auto-focus on mount and on the global focus-search event. Per UX
  // principle A1: the first interaction is typing into the search bar.
  onMount(() => {
    input?.focus();
    const onGlobal = () => input?.focus();
    window.addEventListener('bzlhub:focus-search', onGlobal);
    return () => window.removeEventListener('bzlhub:focus-search', onGlobal);
  });

  function onKeydown(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      value = '';
    } else if (e.key === 'Enter') {
      onSubmit?.(value);
    }
  }
</script>

<div
  class="relative flex items-center bg-bg-elev border border-line focus-within:border-accent rounded-lg transition-colors"
>
  <span class="absolute left-3 text-fg-dim pointer-events-none" aria-hidden="true">
    <!-- Inline magnifier glyph; avoids extra deps. -->
    <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
      <circle cx="7" cy="7" r="5" stroke="currentColor" stroke-width="1.4" />
      <path d="m11 11 3 3" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" />
    </svg>
  </span>
  <input
    bind:this={input}
    bind:value
    onkeydown={onKeydown}
    type="search"
    enterkeyhint="search"
    autocomplete="off"
    autocorrect="off"
    spellcheck="false"
    {placeholder}
    class="w-full bg-transparent pl-9 pr-3 py-2.5 text-[15px] placeholder:text-fg-dim focus:outline-none"
  />
</div>
