package httpstore

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
)

// Auth signs an outgoing request. Implementations are typically
// stateless; for token rotation, wrap a load-from-file shim that
// re-resolves the token on each Apply call.
//
// Name() identifies the scheme for the audit log + diagnostics —
// the library logs which auth was used (without the secret), so
// operators can verify "we hit upstream X with bearer auth" after
// the fact.
type Auth interface {
	// Apply mutates req to carry whatever credential the scheme
	// requires. Implementations MUST be safe to call concurrently
	// from multiple goroutines against the same request only when
	// the request itself is also goroutine-safe (it usually isn't;
	// the Backend calls Apply once per request, sequentially).
	Apply(req *http.Request) error

	// Name returns a short stable identifier ("anonymous",
	// "bearer", "basic", "custom-header:X-Foo"). Used by the
	// Backend's audit hooks; must not reveal secret material.
	Name() string
}

// Anonymous is the explicit "no auth" choice. Operators must opt
// in by passing Anonymous{} as NewOptions.Auth — there is no
// implicit default — so the audit log records "anonymous" rather
// than "configured-but-unset". A nil Auth in NewOptions is an
// error.
type Anonymous struct{}

// Apply is a no-op. The request goes out as-is.
func (Anonymous) Apply(*http.Request) error { return nil }

// Name returns "anonymous".
func (Anonymous) Name() string { return "anonymous" }

// BearerAuth is the canopy-standard auth for self-hosted
// deployments. Sets `Authorization: Bearer <Token>` on every
// request.
//
// Token is the already-resolved bearer value. For load-from-file
// or rotation, wrap BearerAuth in a small shim:
//
//	type rotatingBearer struct{ path string }
//	func (r rotatingBearer) Apply(req *http.Request) error {
//	    tok, err := os.ReadFile(r.path)
//	    if err != nil { return err }
//	    return BearerAuth{Token: strings.TrimSpace(string(tok))}.Apply(req)
//	}
//	func (r rotatingBearer) Name() string { return "bearer-rotating:" + filepath.Base(r.path) }
//
// An empty Token is rejected at Apply time (not at New) so a
// config-file-shaped consumer that reads "" from a missing
// secret file gets a clear error instead of a silent anonymous
// request.
type BearerAuth struct {
	Token string
}

// ErrEmptyToken signals an Auth implementation refused to sign a
// request because its credential is unset. Treated as a
// recoverable condition by callers that load credentials from a
// rotating file (re-read, retry once).
var ErrEmptyToken = errors.New("httpstore: auth token is empty")

// Apply sets Authorization: Bearer <token>. Returns ErrEmptyToken
// when Token is empty — operators want a hard failure, not a
// silent anonymous request, when a credential file went missing.
func (b BearerAuth) Apply(req *http.Request) error {
	if b.Token == "" {
		return fmt.Errorf("%w (bearer)", ErrEmptyToken)
	}
	req.Header.Set("Authorization", "Bearer "+b.Token)
	return nil
}

// Name returns "bearer". Token material never appears here —
// safe to log.
func (BearerAuth) Name() string { return "bearer" }

// BasicAuth covers legacy hosts that key on HTTP Basic
// (Artifactory in some configs, Forgejo with basic-auth-over-
// HTTPS, older nginx fronts). Avoid in new deployments — bearer
// + a load-from-file rotation is the portfolio standard.
//
// Both User and Pass are required at Apply time; empty in either
// returns ErrEmptyToken so a misconfigured secret fails loudly.
type BasicAuth struct {
	User string
	Pass string
}

// Apply sets Authorization: Basic base64(user:pass).
func (b BasicAuth) Apply(req *http.Request) error {
	if b.User == "" || b.Pass == "" {
		return fmt.Errorf("%w (basic: user or pass empty)", ErrEmptyToken)
	}
	creds := b.User + ":" + b.Pass
	req.Header.Set("Authorization",
		"Basic "+base64.StdEncoding.EncodeToString([]byte(creds)))
	return nil
}

// Name returns "basic". Credential material never appears here.
func (BasicAuth) Name() string { return "basic" }

// CustomHeaderAuth signs a request by setting a vendor-specific
// header (Artifactory's X-JFrog-Art-Api, GitLab's PRIVATE-TOKEN,
// JFrog Xray's X-Xray-Token, etc.). HeaderName MUST be a valid
// HTTP header; the library performs no further validation.
//
// Empty Value at Apply time returns ErrEmptyToken — same loud-
// failure contract as Bearer/Basic.
//
// Name() returns "custom-header:<HeaderName>" so audit logs show
// which header was used without revealing the credential value.
type CustomHeaderAuth struct {
	HeaderName string
	Value      string
}

// Apply sets req.Header[HeaderName] = Value.
func (c CustomHeaderAuth) Apply(req *http.Request) error {
	if c.HeaderName == "" {
		return fmt.Errorf("%w (custom-header: HeaderName is empty)", ErrEmptyToken)
	}
	if c.Value == "" {
		return fmt.Errorf("%w (custom-header %q: value is empty)", ErrEmptyToken, c.HeaderName)
	}
	req.Header.Set(c.HeaderName, c.Value)
	return nil
}

// Name returns "custom-header:<HeaderName>" so audit logs
// identify which header carried the credential without leaking
// the value.
func (c CustomHeaderAuth) Name() string {
	if c.HeaderName == "" {
		return "custom-header:<unset>"
	}
	return "custom-header:" + c.HeaderName
}
