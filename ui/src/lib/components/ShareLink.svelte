<script lang="ts">
  import { page } from '$app/state';

  // Tiny "copy this view's URL" affordance for page headers.
  // Plan 14 principle: every URL = state, so the address bar IS the
  // share link. This button is an explicit discoverability hint +
  // a one-click for touch users who can't easily copy from the
  // address bar.
  //
  // Surface intentionally minimal: no menu, no QR code, no "share to
  // Slack" — just clipboard. If the deployment lands on
  // navigator.share later (mobile-friendly), the inner click handler
  // is the only place that changes.

  let copied = $state(false);

  async function copy() {
    if (typeof window === 'undefined') return;
    try {
      await navigator.clipboard.writeText(window.location.href);
      copied = true;
      setTimeout(() => (copied = false), 1500);
    } catch {
      // Clipboard API can fail (insecure context, permissions).
      // Fall back to a select-and-copy on a hidden input.
      const ta = document.createElement('textarea');
      ta.value = window.location.href;
      ta.style.position = 'fixed';
      ta.style.opacity = '0';
      document.body.appendChild(ta);
      ta.select();
      try {
        document.execCommand('copy');
        copied = true;
        setTimeout(() => (copied = false), 1500);
      } catch {
        // Last-resort failure: alert the URL so the user can copy it.
        alert(window.location.href);
      }
      document.body.removeChild(ta);
    }
  }

  // $derived isn't load-bearing here, but using page reactively lets
  // the title hint reflect the live URL (useful if the consumer is
  // open while filters change).
  const href = $derived(page.url.href);
</script>

<button
  type="button"
  class="rounded border border-line text-fg-mute hover:border-accent hover:text-accent transition-colors px-2 py-1 text-[11px] font-mono"
  title={copied ? 'copied!' : `copy link to this view: ${href}`}
  aria-label="copy link to this view"
  onclick={copy}
>
  {copied ? 'copied' : 'share'}
</button>
