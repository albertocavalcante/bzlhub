import { describe, expect, test, vi, beforeEach } from 'vitest';

// Mock the API client before importing the helpers. We use the
// `$lib/...` alias here (matching the SUT's import spec) so a future
// move of api/client.ts doesn't silently desync the mock — vitest
// resolves the alias via the `$lib` entry in vitest.config.ts.
const fakeGet = vi.fn();
class FakeNotFound extends Error {
  constructor(public module: string) {
    super(`module ${module} not indexed`);
    this.name = 'ModuleSummaryNotFoundError';
  }
}
vi.mock('$lib/api/client', () => ({
  getModuleSummary: (name: string, signal?: AbortSignal) => fakeGet(name, signal),
  ModuleSummaryNotFoundError: FakeNotFound,
}));

const { previewCache, moduleNameFromHref, _resetPreviewCacheForTesting } = await import(
  './hover-card'
);

describe('moduleNameFromHref', () => {
  test('extracts plain module name', () => {
    expect(moduleNameFromHref('/modules/rules_python')).toBe('rules_python');
  });

  test('extracts module name from versioned path', () => {
    expect(moduleNameFromHref('/modules/rules_go/0.50.1')).toBe('rules_go');
  });

  test('extracts module name from deep code-nav path', () => {
    expect(moduleNameFromHref('/modules/platforms/0.0.4/code-nav/platforms.bzl')).toBe(
      'platforms',
    );
  });

  test('decodes percent-escaped names', () => {
    expect(moduleNameFromHref('/modules/rules%5Fjs')).toBe('rules_js');
  });

  test('returns null for non-module hrefs', () => {
    expect(moduleNameFromHref('/drift')).toBeNull();
    expect(moduleNameFromHref('https://example.com')).toBeNull();
    expect(moduleNameFromHref('')).toBeNull();
    expect(moduleNameFromHref(null)).toBeNull();
    expect(moduleNameFromHref(undefined)).toBeNull();
  });

  test('survives malformed percent-encoding', () => {
    // Bad percent-encoding shouldn't blow up — fall back to the raw
    // segment so the hover at least shows something readable.
    expect(moduleNameFromHref('/modules/rules%')).toBe('rules%');
  });
});

describe('previewCache', () => {
  beforeEach(() => {
    fakeGet.mockReset();
    _resetPreviewCacheForTesting();
  });

  test('dedupes concurrent fetches for the same target', async () => {
    fakeGet.mockResolvedValue({
      name: 'rules_python',
      latest_version: '0.40.0',
      version_count: 5,
      has_source_index: true,
    });
    const [a, b, c] = await Promise.all([
      previewCache('rules_python'),
      previewCache('rules_python'),
      previewCache('rules_python'),
    ]);
    expect(fakeGet).toHaveBeenCalledTimes(1);
    expect(a).toEqual(b);
    expect(b).toEqual(c);
  });

  test('caches resolved values for subsequent calls', async () => {
    fakeGet.mockResolvedValue({
      name: 'platforms',
      latest_version: '0.0.4',
      version_count: 3,
      has_source_index: false,
    });
    await previewCache('platforms');
    await previewCache('platforms');
    await previewCache('platforms');
    expect(fakeGet).toHaveBeenCalledTimes(1);
  });

  test('caches NotFound so repeated hovers do not re-fetch', async () => {
    fakeGet.mockRejectedValueOnce(new FakeNotFound('absent'));
    await expect(previewCache('absent')).rejects.toBeInstanceOf(FakeNotFound);
    await expect(previewCache('absent')).rejects.toBeInstanceOf(FakeNotFound);
    expect(fakeGet).toHaveBeenCalledTimes(1);
  });

  test('does NOT cache generic errors so transient failures retry', async () => {
    fakeGet.mockRejectedValueOnce(new Error('network'));
    fakeGet.mockResolvedValueOnce({
      name: 'rules_x',
      latest_version: '1.0.0',
      version_count: 1,
      has_source_index: true,
    });
    await expect(previewCache('rules_x')).rejects.toThrow('network');
    const ok = await previewCache('rules_x');
    expect(ok.name).toBe('rules_x');
    expect(fakeGet).toHaveBeenCalledTimes(2);
  });

  // Explicit cache-miss test: after _resetPreviewCacheForTesting,
  // the next call must re-fetch — guards against accidental
  // "in-memory forever" caching that would survive page navigation
  // and serve stale data on a long-running session.
  test('explicit cache reset triggers re-fetch', async () => {
    const payload = {
      name: 'rules_x',
      latest_version: '1.0.0',
      version_count: 1,
      has_source_index: true,
    };
    fakeGet.mockResolvedValue(payload);
    await previewCache('rules_x');
    _resetPreviewCacheForTesting();
    await previewCache('rules_x');
    expect(fakeGet).toHaveBeenCalledTimes(2);
  });
});
