// Pure token-handling primitives. Keeps the runes-backed store
// in token.svelte.ts thin — it owns reactive state; this module
// owns serialization + header attachment so each piece is
// testable in isolation.

// STORAGE_KEY is the localStorage slot all auth state lives under.
// One key (not separate token + email keys) so the cross-tab
// `storage` event fires exactly once per sign-in / sign-out.
export const STORAGE_KEY = 'bzlhub.auth';

// StoredEntry is the JSON shape persisted in localStorage. Versioned
// implicitly via the schema — adding fields is fine, removing or
// renaming requires a STORAGE_KEY bump.
export interface StoredEntry {
  token: string;
  email: string;
}

// TokenStorage is the minimal slice of the DOM Storage interface
// the auth primitives need. Production binds it to localStorage;
// tests inject an in-memory Map-backed fake because vitest 4 +
// jsdom 29 expose Storage globals inconsistently across files.
export interface TokenStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem(key: string): void;
}

// memoryStorage returns a fresh in-memory TokenStorage. Useful for
// tests AND as a degraded-mode fallback when the browser denies
// localStorage (e.g., third-party-cookie-blocked iframes).
export function memoryStorage(): TokenStorage {
  const map = new Map<string, string>();
  return {
    getItem: (k) => map.get(k) ?? null,
    setItem: (k, v) => {
      map.set(k, v);
    },
    removeItem: (k) => {
      map.delete(k);
    },
  };
}

// parseStoredToken reads STORAGE_KEY from storage and returns the
// parsed entry, or null when nothing is stored OR the value is
// malformed.
//
// Self-heals on malformed input by clearing the bad value so the
// next page load isn't stuck re-parsing it.
export function parseStoredToken(storage: TokenStorage): StoredEntry | null {
  const raw = storage.getItem(STORAGE_KEY);
  if (!raw) return null;
  try {
    const parsed = JSON.parse(raw) as Partial<StoredEntry>;
    if (typeof parsed.token !== 'string' || parsed.token === '') {
      return null;
    }
    return {
      token: parsed.token,
      email: typeof parsed.email === 'string' ? parsed.email : '',
    };
  } catch {
    storage.removeItem(STORAGE_KEY);
    return null;
  }
}

// writeStoredToken serializes + persists an entry, OR clears the
// slot when entry is null. Triggers cross-tab `storage` events
// when storage is the real localStorage.
export function writeStoredToken(storage: TokenStorage, entry: StoredEntry | null): void {
  if (entry === null) {
    storage.removeItem(STORAGE_KEY);
    return;
  }
  storage.setItem(STORAGE_KEY, JSON.stringify(entry));
}

// applyAuthHeader returns a NEW RequestInit with Authorization
// attached when token is a non-empty string. Doesn't mutate the
// input. Empty / null token returns the init unchanged (no
// Authorization header attached).
export function applyAuthHeader(init: RequestInit, token: string | null): RequestInit {
  if (!token) return init;
  const headers = new Headers(init.headers);
  headers.set('Authorization', `Bearer ${token}`);
  return { ...init, headers };
}
