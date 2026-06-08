import { beforeEach, describe, expect, it } from 'vitest';

import {
  applyAuthHeader,
  memoryStorage,
  parseStoredToken,
  STORAGE_KEY,
  writeStoredToken,
  type TokenStorage,
} from './token';

describe('parseStoredToken', () => {
  let storage: TokenStorage;
  beforeEach(() => {
    storage = memoryStorage();
  });

  it('returns null when no token is stored', () => {
    expect(parseStoredToken(storage)).toBeNull();
  });

  it('returns the stored entry shape', () => {
    storage.setItem(
      STORAGE_KEY,
      JSON.stringify({ token: 'abc', email: 'alice@example.com' }),
    );
    expect(parseStoredToken(storage)).toEqual({
      token: 'abc',
      email: 'alice@example.com',
    });
  });

  it('returns null on malformed JSON (and self-heals by clearing)', () => {
    storage.setItem(STORAGE_KEY, 'not-json');
    expect(parseStoredToken(storage)).toBeNull();
    // Self-heals so the next page load isn't stuck on the bad value.
    expect(storage.getItem(STORAGE_KEY)).toBeNull();
  });

  it('returns null when the entry is missing the token field', () => {
    storage.setItem(STORAGE_KEY, JSON.stringify({ email: 'alice@example.com' }));
    expect(parseStoredToken(storage)).toBeNull();
  });

  it('returns null when token is an empty string', () => {
    storage.setItem(STORAGE_KEY, JSON.stringify({ token: '', email: 'x' }));
    expect(parseStoredToken(storage)).toBeNull();
  });

  it('tolerates a missing email field (treats as empty)', () => {
    storage.setItem(STORAGE_KEY, JSON.stringify({ token: 'abc' }));
    expect(parseStoredToken(storage)).toEqual({ token: 'abc', email: '' });
  });
});

describe('writeStoredToken', () => {
  let storage: TokenStorage;
  beforeEach(() => {
    storage = memoryStorage();
  });

  it('persists the entry as JSON', () => {
    writeStoredToken(storage, { token: 'abc', email: 'alice@example.com' });
    const raw = storage.getItem(STORAGE_KEY);
    expect(raw).toBeTruthy();
    const parsed = JSON.parse(raw as string);
    expect(parsed.token).toBe('abc');
    expect(parsed.email).toBe('alice@example.com');
  });

  it('clears the slot when entry is null', () => {
    storage.setItem(STORAGE_KEY, JSON.stringify({ token: 'x', email: 'y' }));
    writeStoredToken(storage, null);
    expect(storage.getItem(STORAGE_KEY)).toBeNull();
  });
});

describe('applyAuthHeader', () => {
  it('returns the init unchanged when token is null', () => {
    const init: RequestInit = { method: 'GET' };
    expect(applyAuthHeader(init, null)).toEqual(init);
  });

  it('attaches Authorization to a bare init', () => {
    const out = applyAuthHeader({}, 'abc');
    const headers = new Headers(out.headers);
    expect(headers.get('Authorization')).toBe('Bearer abc');
  });

  it('preserves existing headers on the init', () => {
    const out = applyAuthHeader(
      { headers: { 'Content-Type': 'application/json' } },
      'tok',
    );
    const headers = new Headers(out.headers);
    expect(headers.get('Authorization')).toBe('Bearer tok');
    expect(headers.get('Content-Type')).toBe('application/json');
  });

  it('does not mutate the input init', () => {
    const input: RequestInit = { method: 'POST', headers: { foo: 'bar' } };
    const before = JSON.stringify(input);
    applyAuthHeader(input, 'tok');
    expect(JSON.stringify(input)).toBe(before);
  });

  it('overrides an existing Authorization header (explicit replace)', () => {
    const out = applyAuthHeader(
      { headers: { Authorization: 'Bearer stale' } },
      'fresh',
    );
    const headers = new Headers(out.headers);
    expect(headers.get('Authorization')).toBe('Bearer fresh');
  });

  it('omits Authorization when token is empty string', () => {
    const out = applyAuthHeader({}, '');
    expect(out.headers).toBeUndefined();
  });
});
