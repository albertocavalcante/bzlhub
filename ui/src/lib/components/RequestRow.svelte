<script lang="ts">
  // One <tr> in the procurement queue tables (/requests and
  // /admin/requests). When admin callbacks are wired AND the
  // request is in a reviewable state, an action column with
  // Approve / Deny buttons is rendered. Otherwise the same
  // column shows the read-only detail (denial reason on denied,
  // short commit on indexed, em-dash otherwise).

  import type { Request } from '$api/types';
  import RequestStateChip from './RequestStateChip.svelte';

  let {
    request,
    onApprove,
    onDeny,
    busy = false,
  }: {
    request: Request;
    onApprove?: (r: Request) => void;
    onDeny?: (r: Request) => void;
    busy?: boolean;
  } = $props();

  const admin = $derived(typeof onApprove === 'function' && typeof onDeny === 'function');

  // Approve is only legal from needs_review. Deny is legal from
  // pending / preflighting / needs_review. Mirrors the server's
  // policy-gate + state-machine checks so non-actionable states
  // don't render dead buttons.
  const canApprove = $derived(admin && request.state === 'needs_review');
  const canDeny = $derived(
    admin && (request.state === 'pending' || request.state === 'preflighting' || request.state === 'needs_review'),
  );

  function rel(ts: string): string {
    const then = new Date(ts).getTime();
    const now = Date.now();
    const dt = Math.max(0, Math.floor((now - then) / 1000));
    if (dt < 5) return 'just now';
    if (dt < 60) return `${dt}s ago`;
    if (dt < 3600) return `${Math.floor(dt / 60)}m ago`;
    if (dt < 86400) return `${Math.floor(dt / 3600)}h ago`;
    return `${Math.floor(dt / 86400)}d ago`;
  }
</script>

<tr class="border-b border-line/30 hover:bg-bg-elev/50">
  <td class="py-1.5 pr-3 text-fg-mute" title={request.state_changed_at}>{rel(request.state_changed_at)}</td>
  <td class="py-1.5 pr-3 text-fg">
    <a href="/requests/{request.id}" class="hover:text-accent hover:underline">{request.module}</a>
  </td>
  <td class="py-1.5 pr-3 text-fg-mute">{request.version}</td>
  <td class="py-1.5 pr-3 text-fg-mute">{request.submitter_email || request.submitter_sub}</td>
  <td class="py-1.5 pr-3"><RequestStateChip state={request.state} /></td>
  <td class="py-1.5 pr-3 text-fg-mute">
    {#if request.state === 'denied' && request.denial_reason}
      <span class="text-err">{request.denial_reason}</span>
    {:else if request.state === 'indexed' && request.committed_sha}
      committed {request.committed_sha.slice(0, 8)}
    {:else}—{/if}
  </td>
  {#if admin}
    <td class="py-1.5 text-right whitespace-nowrap">
      {#if canApprove}
        <button
          type="button"
          class="rounded-md border border-ok/40 bg-ok/10 px-2 py-0.5 text-[11px] font-mono text-ok hover:border-ok hover:bg-ok/20 cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
          disabled={busy}
          onclick={() => onApprove?.(request)}
        >approve</button>
      {/if}
      {#if canDeny}
        <button
          type="button"
          class="ml-1 rounded-md border border-err/40 bg-err/10 px-2 py-0.5 text-[11px] font-mono text-err hover:border-err hover:bg-err/20 cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
          disabled={busy}
          onclick={() => onDeny?.(request)}
        >deny</button>
      {/if}
      {#if !canApprove && !canDeny}
        <span class="text-fg-dim text-[10px]">—</span>
      {/if}
    </td>
  {/if}
</tr>
