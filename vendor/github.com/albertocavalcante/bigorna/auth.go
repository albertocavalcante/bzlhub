package bigorna

import (
	"context"
	"encoding/base64"
)

// Authorizer produces the Authorization header value for an HTTP
// request. Called once per HTTP attempt so rotating providers (Vault,
// KMS, OIDC, GitHub App installation tokens) can hand over fresh
// credentials without restarting the host process.
//
// Implementations return the full header value including the scheme
// prefix:
//
//   - Bearer: "Bearer <token>" — PATs, App installation tokens, OAuth
//     access tokens.
//   - Basic:  "Basic <base64(user:password)>" — Bitbucket Cloud app
//     passwords, legacy DC HTTP-Basic.
//
// Returning an empty string means "no Authorization header" — some
// forges accept that for public-repo reads.
//
// This shape (full header value, not just the token) is what lets
// the library extend cleanly to non-Bearer schemes without changing
// every provider impl.
type Authorizer interface {
	Authorize(ctx context.Context) (header string, err error)
}

// AuthorizerFunc adapts a function to the Authorizer interface, the
// same way http.HandlerFunc adapts to http.Handler. Useful for
// closure-based or KMS-signed credentials.
type AuthorizerFunc func(ctx context.Context) (string, error)

// Authorize calls f(ctx).
func (f AuthorizerFunc) Authorize(ctx context.Context) (string, error) {
	return f(ctx)
}

// BearerAuth returns an Authorizer that yields "Bearer <token>" on
// every call. Use for PATs, OAuth access tokens, and any other
// bearer-shaped credential whose value doesn't rotate during the
// process lifetime.
//
// For rotating tokens (KMS, GitHub App installation tokens, refresh-
// flow OAuth), implement Authorizer directly or wrap a closure with
// AuthorizerFunc.
func BearerAuth(token string) Authorizer {
	return AuthorizerFunc(func(_ context.Context) (string, error) {
		return "Bearer " + token, nil
	})
}

// BasicAuth returns an Authorizer that yields the HTTP Basic Auth
// header. Use for Bitbucket Cloud app passwords or legacy DC setups
// without HTTP PAT support.
//
// The credentials are encoded once at construction; the returned
// Authorizer is allocation-free per call.
func BasicAuth(user, password string) Authorizer {
	creds := base64.StdEncoding.EncodeToString([]byte(user + ":" + password))
	header := "Basic " + creds
	return AuthorizerFunc(func(_ context.Context) (string, error) {
		return header, nil
	})
}
