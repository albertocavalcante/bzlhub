import DOMPurify from 'dompurify';
import { marked, Renderer, type MarkedOptions, type Tokens } from 'marked';

const renderer = new Renderer();

// Registry docs are Markdown, not trusted HTML. Strip raw HTML tokens before
// sanitizing the generated output so scriptable tags never reach {@html}.
renderer.html = (_token: Tokens.HTML) => '';

const markedOpts = {
  gfm: true,
  breaks: false,
  pedantic: false,
  renderer,
} satisfies MarkedOptions;

export function renderMarkdownSafe(source: string | undefined, opts: { dedent?: boolean } = {}): string {
  if (!source) return '';
  const text = opts.dedent === false ? source : stripCommonIndent(source);
  const html = marked.parse(text, markedOpts) as string;
  return DOMPurify.sanitize(html, {
    USE_PROFILES: { html: true },
  });
}

export function stripCommonIndent(text: string): string {
  const lines = text.split('\n');
  let min = Infinity;
  for (const line of lines) {
    if (line.trim().length === 0) continue;
    let i = 0;
    while (i < line.length && (line[i] === ' ' || line[i] === '\t')) i++;
    if (i < min) min = i;
  }
  if (!isFinite(min) || min === 0) return text;
  return lines.map((l) => (l.length >= min ? l.slice(min) : l)).join('\n');
}
