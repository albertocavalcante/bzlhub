<script lang="ts">
  // /admin/requests — procurement queue with Approve / Deny controls.
  //
  // Renders the same queue as /requests but with action buttons per
  // row. Buttons render only when the caller's policy gate (read
  // from /api/v1/policy/effective) allows approve_request +
  // deny_request AND the row's state is actionable.

  import { listRequests, approveRequest, denyRequest, getPolicyEffective } from '$api/client';
  import type { Request, PolicyEffective } from '$api/types';
  import RequestRow from '$components/RequestRow.svelte';

  let rows = $state<Request[]>([]);
  let loading = $state(true);
  let error = $state<string | null>(null);
  let policy = $state<PolicyEffective | null>(null);
  let busyID = $state<number | null>(null);
  let actionError = $state<string | null>(null);

  // Pending deny: the active request waiting for a reason; null
  // when no deny modal is open.
  let pendingDeny = $state<Request | null>(null);
  let denyReason = $state<string>('');

  async function loadPolicy() {
    try {
      policy = await getPolicyEffective();
    } catch (e: unknown) {
      // Policy fetch failure → treat as fully-denied (safer).
      policy = {
        profile: 'unknown',
        actions: {},
        identity: { email: '', user: '', groups: null, source: 'anonymous' },
      };
      error = e instanceof Error ? e.message : String(e);
    }
  }

  async function loadRows() {
    loading = rows.length === 0;
    error = null;
    try {
      rows = await listRequests({ limit: 100 });
    } catch (e: unknown) {
      error = e instanceof Error ? e.message : String(e);
      rows = [];
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    void loadPolicy();
    void loadRows();
  });

  $effect(() => {
    const t = setInterval(() => void loadRows(), 15000);
    return () => clearInterval(t);
  });

  // Approve flow — single click, optimistic update, rollback on
  // error. The Q2 default from Plan 74.
  async function approve(r: Request) {
    if (!confirm(`Approve ${r.module}@${r.version}?`)) return;
    busyID = r.id;
    actionError = null;
    const prev = r.state;
    rows = rows.map((x) => (x.id === r.id ? { ...x, state: 'approved' } : x));
    try {
      await approveRequest(r.id);
    } catch (e: unknown) {
      // Rollback.
      rows = rows.map((x) => (x.id === r.id ? { ...x, state: prev } : x));
      actionError = e instanceof Error ? e.message : String(e);
    } finally {
      busyID = null;
    }
  }

  function openDeny(r: Request) {
    pendingDeny = r;
    denyReason = '';
  }

  function cancelDeny() {
    pendingDeny = null;
    denyReason = '';
  }

  async function confirmDeny() {
    if (!pendingDeny) return;
    const reason = denyReason.trim();
    if (!reason) {
      actionError = 'denial reason required';
      return;
    }
    const r = pendingDeny;
    busyID = r.id;
    actionError = null;
    const prev = r.state;
    pendingDeny = null;
    denyReason = '';
    rows = rows.map((x) =>
      x.id === r.id ? { ...x, state: 'denied', denial_reason: reason } : x,
    );
    try {
      await denyRequest(r.id, reason);
    } catch (e: unknown) {
      rows = rows.map((x) =>
        x.id === r.id ? { ...x, state: prev, denial_reason: undefined } : x,
      );
      actionError = e instanceof Error ? e.message : String(e);
    } finally {
      busyID = null;
    }
  }

  const canApproveAny = $derived(policy?.actions?.approve_request === true);
  const canDenyAny = $derived(policy?.actions?.deny_request === true);
  const adminGated = $derived(!canApproveAny && !canDenyAny);
</script>

<svelte:head>
  <title>admin · requests — bzlhub</title>
</svelte:head>

<div class="flex flex-col gap-4">
  <header class="flex flex-col gap-2 pb-3 border-b border-line">
    <div class="flex items-baseline gap-3 flex-wrap">
      <a href="/requests" class="text-[11px] text-fg-mute hover:text-accent font-mono">← public queue</a>
    </div>
    <div class="flex items-baseline gap-3 flex-wrap">
      <h1 class="font-mono text-2xl text-fg tracking-tight">admin · requests</h1>
      <p class="text-[12px] text-fg-mute">approve or deny procurement submissions</p>
    </div>
  </header>

  {#if adminGated}
    <div class="rounded-md border border-line bg-bg-elev px-3 py-2 text-sm text-fg-mute" role="status">
      Your identity does not have the <code class="text-fg">approve_request</code> or
      <code class="text-fg">deny_request</code> gate. Reviewer actions are hidden.
      Switch to the
      <a href="/requests" class="text-accent hover:underline">public queue</a>
      for read-only access.
    </div>
  {/if}

  {#if error}
    <div class="rounded-md border border-err/30 bg-err/10 px-3 py-2 text-sm text-err" role="alert">
      {error}
    </div>
  {/if}

  {#if actionError}
    <div class="rounded-md border border-err/30 bg-err/10 px-3 py-2 text-sm text-err" role="alert">
      action failed: {actionError}
    </div>
  {/if}

  {#if loading}
    <div class="flex flex-col gap-1">
      {#each Array.from({ length: 6 }) as _, i (i)}
        <div class="skeleton h-9 w-full"></div>
      {/each}
    </div>
  {:else if rows.length === 0}
    <p class="text-center py-16 text-fg-dim font-mono text-sm">no procurement requests</p>
  {:else}
    <p class="text-[11px] text-fg-mute font-mono">{rows.length} request{rows.length === 1 ? '' : 's'}</p>
    <table class="w-full text-[12px] font-mono">
      <thead>
        <tr class="text-[10px] uppercase tracking-wide text-fg-dim border-b border-line/60">
          <th class="text-left font-medium py-1.5 pr-3">when</th>
          <th class="text-left font-medium py-1.5 pr-3">module</th>
          <th class="text-left font-medium py-1.5 pr-3">version</th>
          <th class="text-left font-medium py-1.5 pr-3">submitter</th>
          <th class="text-left font-medium py-1.5 pr-3">state</th>
          <th class="text-left font-medium py-1.5 pr-3">detail</th>
          <th class="text-right font-medium py-1.5">action</th>
        </tr>
      </thead>
      <tbody>
        {#each rows as r (r.id)}
          <RequestRow
            request={r}
            onApprove={canApproveAny ? approve : undefined}
            onDeny={canDenyAny ? openDeny : undefined}
            busy={busyID === r.id}
          />
        {/each}
      </tbody>
    </table>
  {/if}

  {#if pendingDeny}
    <div
      class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
      role="dialog"
      aria-modal="true"
      aria-labelledby="deny-title"
      tabindex="-1"
      onclick={(e) => { if (e.target === e.currentTarget) cancelDeny(); }}
      onkeydown={(e) => { if (e.key === 'Escape') cancelDeny(); }}
    >
      <div class="w-full max-w-md rounded-md border border-line bg-bg p-4 flex flex-col gap-3">
        <h2 id="deny-title" class="font-mono text-fg">
          deny {pendingDeny.module}<span class="text-fg-dim">@</span>{pendingDeny.version}?
        </h2>
        <p class="text-[12px] text-fg-mute">
          The reason is recorded on the request row and surfaced to the submitter.
        </p>
        <textarea
          bind:value={denyReason}
          rows="3"
          placeholder="why is this request being denied?"
          class="rounded-md border border-line bg-bg-elev px-2 py-1 text-[12px] font-mono text-fg outline-none focus:border-accent"
        ></textarea>
        <div class="flex justify-end gap-2">
          <button
            type="button"
            class="rounded-md border border-line bg-bg-elev px-3 py-1 text-[12px] font-mono text-fg-mute hover:text-fg cursor-pointer"
            onclick={cancelDeny}
          >cancel</button>
          <button
            type="button"
            class="rounded-md border border-err/40 bg-err/10 px-3 py-1 text-[12px] font-mono text-err hover:border-err hover:bg-err/20 cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
            disabled={denyReason.trim() === ''}
            onclick={confirmDeny}
          >deny</button>
        </div>
      </div>
    </div>
  {/if}
</div>
