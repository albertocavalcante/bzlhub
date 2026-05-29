/**
 * Convert FTS5 [match] markers into HTML spans for safe rendering. Bracket
 * characters in user-supplied module/rule/provider/macro names are rare but
 * we still escape the surrounding text to prevent injection from any
 * pathological doc string content.
 */
export function renderSnippet(snippet: string | undefined): string {
  if (!snippet) return '';
  const escaped = escapeHtml(snippet);
  // The unescaped brackets [...] survive escapeHtml because they're plain
  // ASCII; replace them with span.match in a controlled pass.
  return escaped.replace(/\[([^\]]+)\]/g, '<span class="match">$1</span>');
}

function escapeHtml(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

