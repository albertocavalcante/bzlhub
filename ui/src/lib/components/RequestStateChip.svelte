<script lang="ts">
  import type { RequestState } from '$api/types';

  let { state }: { state: RequestState } = $props();

  // Color tier matches the existing semantic-token vocabulary
  // used by DriftChip + history-page kindStyle: text-ok for
  // success-y, text-err for failure-y, text-fg-mute for
  // in-flight / undecided.
  const style: Record<RequestState, { label: string; cls: string }> = {
    pending:      { label: 'pending',      cls: 'text-fg-mute' },
    preflighting: { label: 'preflighting', cls: 'text-fg-mute' },
    needs_review: { label: 'needs review', cls: 'text-fg' },
    auto_pass:    { label: 'auto-pass',    cls: 'text-ok' },
    approved:     { label: 'approved',     cls: 'text-ok' },
    fetching:     { label: 'fetching',     cls: 'text-fg-mute' },
    indexed:      { label: 'indexed',      cls: 'text-ok' },
    denied:       { label: 'denied',       cls: 'text-err' },
  };

  const s = $derived(style[state] ?? { label: state, cls: 'text-fg-mute' });
</script>

<span class="font-mono text-[11px] uppercase tracking-wide {s.cls}">{s.label}</span>
