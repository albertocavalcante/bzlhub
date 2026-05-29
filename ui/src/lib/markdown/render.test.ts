import { describe, expect, it } from 'vitest';

import { renderMarkdownSafe, stripCommonIndent } from './render';

describe('renderMarkdownSafe', () => {
  it('renders common Markdown', () => {
    const html = renderMarkdownSafe('A **bold** move with `code`.\n\n- one\n- two');

    expect(html).toContain('<strong>bold</strong>');
    expect(html).toContain('<code>code</code>');
    expect(html).toContain('<li>one</li>');
  });

  it('strips raw HTML tokens from module docs', () => {
    const html = renderMarkdownSafe('hello <img src=x onerror=alert(1)> <b>world</b>\n\n<script>alert(1)</script>');

    expect(html).toContain('hello');
    expect(html).toContain('world');
    expect(html).not.toContain('<img');
    expect(html).not.toContain('<b>');
    expect(html).not.toContain('script');
    expect(html).not.toContain('onerror');
  });

  it('sanitizes unsafe generated link attributes', () => {
    const html = renderMarkdownSafe('[click](javascript:alert(1))');

    expect(html).toContain('<a');
    expect(html).toContain('click');
    expect(html).not.toContain('javascript:');
  });
});

describe('stripCommonIndent', () => {
  it('removes shared leading indentation from non-blank lines', () => {
    expect(stripCommonIndent('    # Heading\n      body\n')).toBe('# Heading\n  body\n');
  });
});
