<script lang="ts">
  // Procurement submit form. Modal triggered from /requests'
  // "new request" button when policy.actions.submit_request is
  // true for the caller. Posts to /api/v1/requests via the
  // existing submitRequest client (which attaches the bearer
  // header automatically via authed()).

  import { submitRequest, type SubmitRequestResult } from '$api/client';

  let { onClose, onSubmitted }: {
    onClose: () => void;
    // Called with the server's response on success so the queue
    // page can refresh + scroll the new row into view. dedup=true
    // means the server collapsed onto an existing open request.
    onSubmitted: (r: SubmitRequestResult) => void;
  } = $props();

  let module = $state('');
  let version = $state('');
  let sourceURL = $state('');
  let notes = $state('');
  let busy = $state(false);
  let error = $state<string | null>(null);

  const canSubmit = $derived(
    !busy && module.trim() !== '' && version.trim() !== '',
  );

  async function submit() {
    if (!canSubmit) return;
    busy = true;
    error = null;
    try {
      const res = await submitRequest({
        module: module.trim(),
        version: version.trim(),
        source_url: sourceURL.trim() || undefined,
        notes: notes.trim() || undefined,
      });
      onSubmitted(res);
      onClose();
    } catch (e: unknown) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }

  function handleKey(e: KeyboardEvent) {
    if (e.key === 'Escape') onClose();
  }
</script>

<div
  class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
  role="dialog"
  aria-modal="true"
  aria-labelledby="newreq-title"
  tabindex="-1"
  onclick={(e) => { if (e.target === e.currentTarget) onClose(); }}
  onkeydown={handleKey}
>
  <div class="w-full max-w-lg rounded-md border border-line bg-bg p-4 flex flex-col gap-3">
    <h2 id="newreq-title" class="font-mono text-fg">New procurement request</h2>
    <p class="text-[12px] text-fg-mute">
      Submit a Bazel module for admission. Preflight will route it
      automatically (or to human review) within a few seconds. The
      identity attached to your sign-in becomes the submitter on
      the request row.
    </p>

    <label class="flex flex-col gap-1 text-[11px] text-fg-dim font-mono">
      <span>module <span class="text-err">*</span></span>
      <input
        type="text"
        bind:value={module}
        placeholder="rules_python"
        autocomplete="off"
        class="rounded-md border border-line bg-bg-elev px-2 py-1 text-[12px] font-mono text-fg outline-none focus:border-accent"
      />
    </label>

    <label class="flex flex-col gap-1 text-[11px] text-fg-dim font-mono">
      <span>version <span class="text-err">*</span></span>
      <input
        type="text"
        bind:value={version}
        placeholder="1.5.0"
        autocomplete="off"
        class="rounded-md border border-line bg-bg-elev px-2 py-1 text-[12px] font-mono text-fg outline-none focus:border-accent"
      />
    </label>

    <label class="flex flex-col gap-1 text-[11px] text-fg-dim font-mono">
      <span>source url <span class="text-fg-dim normal-case">(optional — preflight falls back to BCR when absent)</span></span>
      <input
        type="url"
        bind:value={sourceURL}
        placeholder="https://github.com/bazelbuild/rules_python/archive/1.5.0.tar.gz"
        autocomplete="off"
        class="rounded-md border border-line bg-bg-elev px-2 py-1 text-[12px] font-mono text-fg outline-none focus:border-accent"
      />
    </label>

    <label class="flex flex-col gap-1 text-[11px] text-fg-dim font-mono">
      <span>notes <span class="text-fg-dim normal-case">(optional — context for the reviewer)</span></span>
      <textarea
        bind:value={notes}
        rows="2"
        placeholder="why this version, what depends on it, etc."
        class="rounded-md border border-line bg-bg-elev px-2 py-1 text-[12px] font-mono text-fg outline-none focus:border-accent"
      ></textarea>
    </label>

    {#if error}
      <p class="text-[12px] text-err font-mono" role="alert">{error}</p>
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
        disabled={!canSubmit}
        onclick={submit}
      >{busy ? 'submitting…' : 'submit'}</button>
    </div>
  </div>
</div>
