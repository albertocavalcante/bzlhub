package bundle

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// SignatureAlgorithmEd25519 is the value of Signature.Algorithm
// when the manifest is signed with Ed25519 (the only algorithm
// supported in v0.2.x). Future algorithms (ML-DSA for post-
// quantum) would land as new constants; the Verifier dispatcher
// checks Algorithm to route correctly.
const SignatureAlgorithmEd25519 = "ed25519"

// Ed25519Signer implements Signer using Ed25519. Construct with a
// 64-byte ed25519.PrivateKey and a human-readable KeyID. The
// KeyID lands in Signature.KeyID at sign time so multi-key
// Verifiers can route to the matching public key without trial
// decryption.
//
// KeyID convention: "<name>+<8-hex-byte-public-key-prefix>" —
// same shape as the gosumdb verifier line. Stable identifier
// that survives key-file moves but rotates on every key change.
//
// Recommendation: derive KeyID via Ed25519KeyID(pub) for the
// public key matching this signer's private key.
type Ed25519Signer struct {
	PrivateKey ed25519.PrivateKey
	KeyID      string
}

// Compile-time guard.
var _ Signer = Ed25519Signer{}

// Sign produces an Ed25519 signature over canonicalManifest. Used
// by WriteBundle when WriteOptions.Signer is non-nil. The
// returned Signature is stamped onto manifest.signature before
// the manifest is finalised + written into the tar.gz.
//
// Returns an error if the private key is malformed (wrong size).
func (s Ed25519Signer) Sign(canonicalManifest []byte) (Signature, error) {
	if len(s.PrivateKey) != ed25519.PrivateKeySize {
		return Signature{}, fmt.Errorf(
			"bundle: Ed25519Signer: PrivateKey size = %d; want %d",
			len(s.PrivateKey), ed25519.PrivateKeySize)
	}
	if s.KeyID == "" {
		return Signature{}, fmt.Errorf(
			"bundle: Ed25519Signer: KeyID is required")
	}
	sig := ed25519.Sign(s.PrivateKey, canonicalManifest)
	return Signature{
		Algorithm: SignatureAlgorithmEd25519,
		KeyID:     s.KeyID,
		Value:     base64.StdEncoding.EncodeToString(sig),
	}, nil
}

// Ed25519Verifier verifies Ed25519 manifest signatures.
// Construct via NewEd25519Verifier with a map of KeyID → public
// key — single-key Verifiers are just maps with one entry.
// Multi-key support enables rotation overlap (HQ-prod-2026 +
// HQ-prod-2027 both valid for a transition window) without code
// changes at the airgap side.
//
// Compile-time enforces Verifier interface conformance.
type Ed25519Verifier struct {
	keys map[string]ed25519.PublicKey
}

// Compile-time guard.
var _ Verifier = (*Ed25519Verifier)(nil)

// NewEd25519Verifier constructs an Ed25519Verifier over the given
// KeyID → public-key map. Empty map → ErrInvalidBundle (no point
// constructing a Verifier with no keys). Each public key must be
// 32 bytes; mismatch → ErrInvalidBundle.
//
// The returned Verifier dispatches Signature.KeyID against the
// map; unknown KeyID → ErrSignatureInvalid.
func NewEd25519Verifier(keys map[string]ed25519.PublicKey) (*Ed25519Verifier, error) {
	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: Ed25519Verifier needs at least one key",
			ErrInvalidBundle)
	}
	for keyID, pub := range keys {
		if keyID == "" {
			return nil, fmt.Errorf("%w: Ed25519Verifier: empty KeyID in keys map",
				ErrInvalidBundle)
		}
		if len(pub) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("%w: Ed25519Verifier: key %q has size %d; want %d",
				ErrInvalidBundle, keyID, len(pub), ed25519.PublicKeySize)
		}
	}
	// Defensive copy so callers can't mutate the verifier post-construction.
	cp := make(map[string]ed25519.PublicKey, len(keys))
	for k, v := range keys {
		cp[k] = append(ed25519.PublicKey(nil), v...)
	}
	return &Ed25519Verifier{keys: cp}, nil
}

// Verify implements Verifier. Returns nil on a valid signature,
// ErrSignatureInvalid otherwise (with wrapped context).
//
// Three failure modes, all surface as ErrSignatureInvalid:
//   - sig.Algorithm not "ed25519"
//   - sig.KeyID not in the verifier's keys map
//   - cryptographic verification fails (key/signature/message mismatch)
//
// Wraps with detail so operators can distinguish causes from
// log lines, but errors.Is(err, ErrSignatureInvalid) is the
// stable predicate.
func (v *Ed25519Verifier) Verify(canonicalManifest []byte, sig Signature) error {
	if sig.Algorithm != SignatureAlgorithmEd25519 {
		return fmt.Errorf("%w: algorithm %q not supported (want %q)",
			ErrSignatureInvalid, sig.Algorithm, SignatureAlgorithmEd25519)
	}
	pub, ok := v.keys[sig.KeyID]
	if !ok {
		return fmt.Errorf("%w: unknown KeyID %q (operator must add the public key to the verifier)",
			ErrSignatureInvalid, sig.KeyID)
	}
	raw, err := base64.StdEncoding.DecodeString(sig.Value)
	if err != nil {
		return fmt.Errorf("%w: signature value not valid base64: %v",
			ErrSignatureInvalid, err)
	}
	if !ed25519.Verify(pub, canonicalManifest, raw) {
		return fmt.Errorf("%w: cryptographic verification failed (manifest bytes don't match signature)",
			ErrSignatureInvalid)
	}
	return nil
}

// GenerateKey returns a fresh Ed25519 keypair plus a KeyID derived
// from the public key's prefix. Convenience for canopy's bundle
// keygen CLI; production deployments may prefer hardware-backed
// keys via a custom Signer implementation.
//
// The KeyID format is "<name>+<8-hex-char-public-key-prefix>"
// — same shape as the gosumdb verifier line. The first 4 bytes
// of the public key become 8 hex chars. Operators rotate by
// generating a new key; the KeyID changes automatically.
func GenerateKey(name string) (pub ed25519.PublicKey, priv ed25519.PrivateKey, keyID string, err error) {
	if name == "" {
		return nil, nil, "", fmt.Errorf("bundle: GenerateKey: name is required")
	}
	pub, priv, err = ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, "", fmt.Errorf("bundle: ed25519.GenerateKey: %w", err)
	}
	keyID = Ed25519KeyID(name, pub)
	return pub, priv, keyID, nil
}

// Ed25519KeyID returns the KeyID for the given public key prefix
// + name. Same format as the gosumdb verifier line:
// "<name>+<8-hex-char-public-key-prefix>". Operators can compute
// this themselves when minting keys via external tooling.
func Ed25519KeyID(name string, pub ed25519.PublicKey) string {
	if len(pub) < 4 {
		return name + "+0000"
	}
	return fmt.Sprintf("%s+%02x%02x%02x%02x", name, pub[0], pub[1], pub[2], pub[3])
}
