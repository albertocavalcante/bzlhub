// Runes-backed auth singleton. Owns the reactive "is the user
// signed in / who" state the layout + API client read. Pure
// storage + header logic lives in ./token (testable without
// the runes machinery).
//
// SPA-only (svelte.config.js: ssr=false) so a module-level
// singleton is safe — no cross-request leakage.

import type { PolicyEffective } from '$lib/api/types';
import {
  applyAuthHeader,
  memoryStorage,
  parseStoredToken,
  writeStoredToken,
  type TokenStorage,
} from './token';

// IdentitySource mirrors policy.identity.source from the server's
// /api/v1/policy/effective response. Exported so the AuthButton's
// conditional render (no sign-out for header-auth) can type-check.
export type IdentitySource = 'anonymous' | 'bearer' | 'header' | 'oidc';

// pickStorage uses real localStorage when the runtime exposes it
// and a hidden in-memory fallback otherwise (SSR build step,
// privacy-blocked iframes, jsdom test contexts). The fallback
// means signed-in state is per-tab in degraded environments,
// which is fine.
function pickStorage(): TokenStorage {
  if (typeof window !== 'undefined') {
    try {
      const probe = '__bzlhub_auth_probe__';
      window.localStorage.setItem(probe, '1');
      window.localStorage.removeItem(probe);
      return window.localStorage;
    } catch {
      // Falls through to memoryStorage below.
    }
  }
  return memoryStorage();
}

class AuthStore {
  // The bearer token; null when not using bearer-paste auth.
  // Header-auth users have a non-anonymous source WITHOUT a token.
  token = $state<string | null>(null);
  // The signed-in user's email, populated from /policy/effective's
  // identity field on refresh() or successful signIn().
  email = $state<string>('');
  // How the server resolved this caller's identity. UI conditionals:
  //   anonymous  → render "sign in" button
  //   bearer     → render "email · sign out"
  //   header/oidc → render "email" only (no sign-out — the proxy
  //                 owns the auth; clearing localStorage wouldn't
  //                 sign the user out of the IdP)
  source = $state<IdentitySource>('anonymous');
  // True while a sign-in attempt is in flight.
  busy = $state(false);
  // Most-recent sign-in error, surfaced in the modal.
  error = $state<string | null>(null);

  private storage = pickStorage();

  // Restore from storage on first import. Idempotent — calling
  // restore() repeatedly is safe (layout's onMount may invoke it
  // alongside a cross-tab `storage` event).
  //
  // Doesn't validate against the server — that's refresh()'s job.
  // restore() is the synchronous hydration; refresh() is the
  // asynchronous probe that detects stale-token + header-auth.
  restore(): void {
    const entry = parseStoredToken(this.storage);
    if (entry) {
      this.token = entry.token;
      this.email = entry.email;
    } else {
      this.token = null;
      this.email = '';
    }
  }

  // refresh probes /api/v1/policy/effective with the current token
  // attached (if any). Updates state from the identity response so
  // header-auth deployments show "signed in as alice@example.com"
  // without a token-paste step. Also catches stale bearer tokens —
  // a 401 means the operator rotated identity.json + SIGHUPed and
  // our stored token is no longer valid; clear it.
  //
  // Network errors leave state as-is — the next interaction will
  // probe again. Silent failure here is fine; the eventual API
  // call will surface the real error.
  async refresh(): Promise<void> {
    try {
      const init = applyAuthHeader({}, this.token);
      const res = await fetch('/api/v1/policy/effective', init);
      if (res.status === 401) {
        // Stale bearer token. Clear + reset state.
        if (this.token) this.signOut();
        return;
      }
      if (!res.ok) return;
      const body = (await res.json()) as PolicyEffective;
      const ident = body.identity;
      if (!ident) return;
      this.source = ident.source;
      if (ident.source === 'anonymous') {
        // Server sees us as anonymous. If we have a stored token
        // that's not being honored, drop it.
        if (this.token) this.signOut();
        return;
      }
      this.email = ident.email || ident.user || '(signed in)';
    } catch {
      // Network / parse error — leave state as-is.
    }
  }

  // signIn validates the token by calling /api/v1/policy/effective
  // with it. A non-error response (any HTTP 2xx) means the bzlhub
  // server accepted the credential — we don't strictly check that
  // policy.actions contains anything specific, since "anonymous
  // would have gotten the same answer" is the only failure shape
  // worth distinguishing, and that's a v0.1 polish.
  async signIn(rawToken: string): Promise<void> {
    const token = rawToken.trim();
    if (!token) {
      this.error = 'token cannot be empty';
      return;
    }
    this.busy = true;
    this.error = null;
    try {
      // Probe by calling /api/v1/policy/effective with the token
      // attached. Direct fetch here (not via api/client) to avoid
      // a circular import — api/client depends on this store for
      // every authed call.
      const init = applyAuthHeader({}, token);
      const res = await fetch('/api/v1/policy/effective', init);
      if (!res.ok) {
        throw new Error(`bzlhub rejected token: HTTP ${res.status}`);
      }
      // Token accepted. Parse the identity from the response so
      // the UI shows "signed in as alice@example.com" instead of
      // a generic placeholder.
      const body = (await res.json()) as PolicyEffective;
      const ident = body.identity;
      const email = (ident && (ident.email || ident.user)) || '(signed in)';
      this.token = token;
      this.email = email;
      this.source = (ident && ident.source) || 'bearer';
      writeStoredToken(this.storage, { token, email });
    } catch (e) {
      this.error = e instanceof Error ? e.message : String(e);
    } finally {
      this.busy = false;
    }
  }

  signOut(): void {
    this.token = null;
    this.email = '';
    this.source = 'anonymous';
    writeStoredToken(this.storage, null);
  }

  // get returns the current token for API-client use. Function
  // rather than direct field access so callers don't accidentally
  // capture a stale value in closures.
  get(): string | null {
    return this.token;
  }
}

export const auth = new AuthStore();
