/**
 * Format bytes as a compact human string: "2 MB", "512 KB", "1.4 GB".
 * Used by the tarball-size chip on the per-version header. Returns
 * "" for 0 or undefined so callers can omit the chip.
 */
export function formatBytes(n: number | undefined): string {
  if (!n || n <= 0) return '';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let v = n;
  let u = 0;
  while (v >= 1024 && u < units.length - 1) {
    v /= 1024;
    u++;
  }
  // One decimal for MB+, none for B/KB.
  const formatted = u <= 1 ? Math.round(v).toString() : v.toFixed(1);
  return `${formatted} ${units[u]}`;
}

/**
 * Pure relative-time formatter — "2h ago", "3d ago", etc. Used in
 * places where the backend ships an RFC3339 timestamp and the UI
 * just needs a compact human-readable badge.
 *
 * Display-only: no business logic, no edge-case decisions beyond
 * "what string corresponds to this duration." Returns "" when the
 * input is empty or unparseable so callers can blindly drop the
 * badge.
 */
export function relativeTime(iso: string | undefined, now: number = Date.now()): string {
  if (!iso) return '';
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return '';
  const sec = Math.max(0, Math.floor((now - t) / 1000));
  if (sec < 60) return 'just now';
  const min = Math.floor(sec / 60);
  if (min < 60) return `${min}m ago`;
  const hr = Math.floor(min / 60);
  if (hr < 24) return `${hr}h ago`;
  const day = Math.floor(hr / 24);
  if (day < 30) return `${day}d ago`;
  const mo = Math.floor(day / 30);
  if (mo < 12) return `${mo}mo ago`;
  return `${Math.floor(mo / 12)}y ago`;
}
