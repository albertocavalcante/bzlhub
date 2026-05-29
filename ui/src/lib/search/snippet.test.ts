import { describe, expect, it } from 'vitest';

import { renderSnippet } from './snippet';

describe('renderSnippet', () => {
  it('wraps FTS match markers in controlled span markup', () => {
    expect(renderSnippet('use [go_library] here')).toBe(
      'use <span class="match">go_library</span> here',
    );
  });

  it('escapes HTML outside and inside match markers before injecting spans', () => {
    expect(renderSnippet('<img src=x onerror=alert(1)> [<script>alert(1)</script>]')).toBe(
      '&lt;img src=x onerror=alert(1)&gt; <span class="match">&lt;script&gt;alert(1)&lt;/script&gt;</span>',
    );
  });

  it('returns an empty string for missing snippets', () => {
    expect(renderSnippet(undefined)).toBe('');
  });
});

