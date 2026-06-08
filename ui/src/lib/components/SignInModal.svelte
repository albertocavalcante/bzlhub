<script lang="ts">
  // Token-paste sign-in modal. The whole auth surface for v0:
  // operator hands out tokens out-of-band; user pastes one here.
  // No password, no redirect, no OIDC dance. Validation = a probe
  // GET against /api/v1/policy/effective with the token attached.

  import { auth } from '$lib/auth/auth.svelte';

  let { onClose }: { onClose: () => void } = $props();

  let input = $state('');

  async function submit() {
    if (!input.trim()) return;
    await auth.signIn(input);
    // signIn writes auth.error on failure; the modal stays open
    // for the user to retry. On success it clears the token state
    // and we dismiss.
    if (auth.token) {
      input = '';
      onClose();
    }
  }

  function handleKey(e: KeyboardEvent) {
    if (e.key === 'Escape') onClose();
    if (e.key === 'Enter') void submit();
  }
</script>

<div
  class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
  role="dialog"
  aria-modal="true"
  aria-labelledby="signin-title"
  tabindex="-1"
  onclick={(e) => { if (e.target === e.currentTarget) onClose(); }}
  onkeydown={handleKey}
>
  <div class="w-full max-w-md rounded-md border border-line bg-bg p-4 flex flex-col gap-3">
    <h2 id="signin-title" class="font-mono text-fg">Sign in</h2>
    <p class="text-[12px] text-fg-mute">
      Paste your bearer token. Operator-issued; ask your bzlhub admin
      if you don't have one. The token stays in this browser
      (<code class="text-fg">localStorage</code>) until you sign out.
    </p>
    <input
      type="password"
      bind:value={input}
      placeholder="bearer token"
      autocomplete="off"
      class="rounded-md border border-line bg-bg-elev px-2 py-1 text-[12px] font-mono text-fg outline-none focus:border-accent"
    />
    {#if auth.error}
      <p class="text-[12px] text-err font-mono" role="alert">{auth.error}</p>
    {/if}
    <div class="flex justify-end gap-2">
      <button
        type="button"
        class="rounded-md border border-line bg-bg-elev px-3 py-1 text-[12px] font-mono text-fg-mute hover:text-fg cursor-pointer"
        onclick={onClose}
      >cancel</button>
      <button
        type="button"
        class="rounded-md border border-accent bg-accent/10 px-3 py-1 text-[12px] font-mono text-accent hover:bg-accent/20 cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
        disabled={auth.busy || !input.trim()}
        onclick={submit}
      >{auth.busy ? 'signing in…' : 'sign in'}</button>
    </div>
  </div>
</div>
