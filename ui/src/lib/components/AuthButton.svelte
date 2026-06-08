<script lang="ts">
  // Top-right sign-in/out affordance. Render shape:
  //   signed out → "sign in" button (opens modal)
  //   signed in  → "<email> · sign out" inline
  //
  // Restores from localStorage on mount + reacts to cross-tab
  // sign-in / sign-out via the `storage` event so a token paste
  // in one tab unlocks the admin queue in every open tab.

  import { onMount } from 'svelte';
  import { auth } from '$lib/auth/auth.svelte';
  import { STORAGE_KEY } from '$lib/auth/token';
  import SignInModal from './SignInModal.svelte';

  let showModal = $state(false);

  onMount(() => {
    // Synchronous: hydrate from localStorage so a returning user
    // sees the "signed in" affordance immediately.
    auth.restore();
    // Asynchronous: probe /policy/effective. Detects header-auth
    // identity (no token paste needed) and clears stale bearer
    // tokens (401 from the server). Fire-and-forget; refresh()
    // catches its own errors.
    void auth.refresh();
    const handler = (e: StorageEvent) => {
      if (e.key === STORAGE_KEY || e.key === null) auth.restore();
    };
    window.addEventListener('storage', handler);
    return () => window.removeEventListener('storage', handler);
  });

  function signOut() {
    auth.signOut();
  }

  // Three render modes:
  //   anonymous → "sign in" button (opens modal)
  //   bearer    → "<email> · sign out" (paste flow; sign-out clears)
  //   header / oidc → "<email>" only — the reverse proxy owns the
  //                   auth; sign-out would just re-authenticate on
  //                   the next request, so don't pretend it works.
</script>

{#if auth.source === 'anonymous'}
  <button
    type="button"
    class="text-[11px] font-mono text-fg-mute hover:text-accent cursor-pointer"
    onclick={() => { showModal = true; }}
  >sign in</button>
{:else if auth.source === 'bearer'}
  <span class="text-[11px] text-fg-mute font-mono">
    {auth.email || 'signed in'}
    <button
      type="button"
      class="ml-2 text-fg-mute hover:text-accent cursor-pointer"
      onclick={signOut}
    >sign out</button>
  </span>
{:else}
  <!-- header / oidc: identity injected by the reverse proxy -->
  <span class="text-[11px] text-fg-mute font-mono" title="signed in via reverse proxy">
    {auth.email || 'signed in'}
  </span>
{/if}

{#if showModal}
  <SignInModal onClose={() => { showModal = false; }} />
{/if}
