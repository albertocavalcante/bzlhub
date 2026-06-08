package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// maxIdentityFileBytes caps how much of the identity file canopy
// will read at boot. A real deployment carries one entry (~150 B)
// per authorized human/service; 10 MB holds ~70k tokens which is
// far past any plausible canopy install. Anything past this is
// almost certainly a mis-mounted bind that would OOM canopy.
const maxIdentityFileBytes = 10 * 1024 * 1024

// IdentityRegistry maps bearer-token SHA-256 hashes to the
// authenticated Identity those tokens represent. Loaded from a JSON
// file at boot; reloadable on SIGHUP (operator concern).
//
// The JSON carries hashes, never plaintext tokens. Operators compute
// the hash locally (`printf '%s' "$TOKEN" | shasum -a 256`) and
// commit only the hex digest. Compromising the canopy host yields
// the hash table, not the tokens; an attacker would have to invert
// SHA-256 to recover any usable token. (Canopy doesn't claim this
// is bcrypt/argon2 strength — bearer tokens here are
// operator-generated 32-byte random strings, so SHA-256 against the
// well-randomized input is sufficient. Defense-in-depth, not
// password storage.)
//
// File format is JSON for v0 (stdlib-only, no new dep). When the
// canonical policy.yml lands (Plan 71 / chunk 6), the identity file
// MAY migrate to YAML for consistency — the wire shape is stable so
// the migration is mechanical.
//
// Plan refs: 69 §Identity, 70 §Identity-from-headers, 72 §C3.
type IdentityRegistry struct {
	mu     sync.RWMutex
	tokens map[string]Identity // key: lowercase hex SHA-256 of token
}

// identityFile is the JSON wire shape. Versioned so a future
// migration (argon2 hashes, per-token TTL, multi-format) can detect.
//
//	{
//	  "version": 1,
//	  "tokens": [
//	    {
//	      "token_sha256": "abc…64hex…",
//	      "identity": {
//	        "user":   "alice@example.com",
//	        "email":  "alice@example.com",
//	        "groups": ["eval-submitter"]
//	      }
//	    }
//	  ]
//	}
type identityFile struct {
	Version int          `json:"version"`
	Tokens  []tokenEntry `json:"tokens"`
}

type tokenEntry struct {
	TokenSHA256 string          `json:"token_sha256"`
	Identity    tokenEntryIdent `json:"identity"`
}

type tokenEntryIdent struct {
	User   string   `json:"user"`
	Email  string   `json:"email"`
	Groups []string `json:"groups"`
}

// LoadIdentityFile parses path and returns a populated registry.
// Returns ErrIdentityFileMissing when the file doesn't exist (so
// callers can branch on missing-vs-malformed). Other errors mean
// the file is present but unusable.
//
// Empty `tokens` list is OK — registry has no entries; all lookups
// miss. Useful for "bearer auth wired but no tokens yet" boot.
func LoadIdentityFile(path string) (*IdentityRegistry, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrIdentityFileMissing
		}
		return nil, fmt.Errorf("auth: open identity file %s: %w", path, err)
	}
	defer f.Close()
	// LimitReader caps the read regardless of stat-vs-actual-size
	// divergence (sparse files, /proc oddities). +1 byte so we can
	// detect "exactly cap or larger" with a single Read.
	data, err := io.ReadAll(io.LimitReader(f, maxIdentityFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("auth: read identity file %s: %w", path, err)
	}
	if len(data) > maxIdentityFileBytes {
		return nil, fmt.Errorf("auth: identity file %s too large (>%d bytes); refusing to parse (likely a mis-mounted bind — point BZLHUB_IDENTITY_FILE at the right file)", path, maxIdentityFileBytes)
	}
	var file identityFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("auth: parse identity file %s: %w", path, err)
	}
	if file.Version != 1 {
		return nil, fmt.Errorf("auth: identity file %s: unsupported version %d (this build supports version 1)", path, file.Version)
	}

	reg := &IdentityRegistry{tokens: make(map[string]Identity, len(file.Tokens))}
	for i, t := range file.Tokens {
		hash := strings.ToLower(strings.TrimSpace(t.TokenSHA256))
		if len(hash) != 64 {
			return nil, fmt.Errorf("auth: identity file %s entry %d: token_sha256 must be a 64-char hex string (got %d chars)", path, i, len(hash))
		}
		if _, err := hex.DecodeString(hash); err != nil {
			return nil, fmt.Errorf("auth: identity file %s entry %d: token_sha256 is not valid hex: %w", path, i, err)
		}
		ident := t.Identity
		if ident.User == "" && ident.Email == "" {
			return nil, fmt.Errorf("auth: identity file %s entry %d: identity must carry at least user or email", path, i)
		}
		if _, exists := reg.tokens[hash]; exists {
			return nil, fmt.Errorf("auth: identity file %s entry %d: duplicate token_sha256 %s…", path, i, hash[:8])
		}
		reg.tokens[hash] = Identity{
			User:   strings.TrimSpace(ident.User),
			Email:  strings.TrimSpace(ident.Email),
			Groups: ident.Groups,
			Source: SourceBearer,
		}
	}
	return reg, nil
}

// Lookup returns the Identity for the given plaintext bearer token,
// or (Identity{}, false) if the token isn't registered. The lookup
// hashes the token in constant work and probes the precomputed
// hash map.
//
// Security model: the wire token is operator-generated 32-byte
// random material; SHA-256 over well-randomized input is sufficient
// to defeat preimage attacks (the only path an attacker has into the
// hash table). The Go map probe is hash-based, not byte-compare —
// no realistic timing channel discriminates one stored token from
// another. No subtle.ConstantTimeCompare is needed; an earlier
// "belt-and-suspenders" comparison was self-vs-self and dead.
//
// Returns false on the empty token or a nil registry — callers can
// invoke unconditionally without nil-guarding.
func (r *IdentityRegistry) Lookup(token string) (Identity, bool) {
	if r == nil || token == "" {
		return Identity{}, false
	}
	sum := sha256.Sum256([]byte(token))
	hashHex := hex.EncodeToString(sum[:])

	r.mu.RLock()
	ident, ok := r.tokens[hashHex]
	r.mu.RUnlock()
	return ident, ok
}

// Replace atomically swaps the registry's token table for the one
// in other. Used by SIGHUP reload — load the new file outside the
// lock, then swap. If parsing the new file fails the caller keeps
// the previous registry (don't call Replace with a nil/empty result).
func (r *IdentityRegistry) Replace(other *IdentityRegistry) {
	if r == nil || other == nil {
		return
	}
	other.mu.RLock()
	newTokens := other.tokens
	other.mu.RUnlock()

	r.mu.Lock()
	r.tokens = newTokens
	r.mu.Unlock()
}

// Size reports the count of registered tokens. Used for boot-log
// observability ("identity registry loaded with N tokens") so the
// operator can confirm canopy sees what they wrote.
func (r *IdentityRegistry) Size() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tokens)
}

// ErrIdentityFileMissing is returned by LoadIdentityFile when the
// path doesn't exist. Distinct error so callers can branch on
// "missing vs malformed" — typical boot pattern: missing →
// continue without bearer auth; malformed → fail fast.
var ErrIdentityFileMissing = errors.New("auth: identity file missing")
