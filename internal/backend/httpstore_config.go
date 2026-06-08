package backend

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	artifactory "github.com/albertocavalcante/go-bcr-artifactory"
	httpstore "github.com/albertocavalcante/go-bcr-httpstore"
)

// HTTPStoreConfig describes how to construct an HTTP-backed bzlhub
// substrate. Threaded into Build to produce an *HTTPStore wired
// against either a plain HTTP store (nginx / Caddy autoindex) or a
// JFrog Artifactory generic repository.
//
// Populate via LoadHTTPStoreConfig (env-driven, the production
// path) or by direct struct literal (test path).
//
// The config layer deliberately owns:
//
//   - validation (Build returns a typed error before any network IO)
//   - auth-secret-from-file resolution (per the corporate-security-
//     first feedback rule — no env-var-only secret paths)
//   - Layout choice per Kind (HTMLAutoindex for plain stores,
//     artifactory.Layout for the Artifactory adapter)
//
// What it does NOT own:
//
//   - HTTP client construction (caller supplies *http.Client so
//     tests can plug in httptest.Server transports + production
//     deployments can layer their own timeouts / OTel hooks)
//   - boot wiring (cmd/bzlhub/serve.go decides when to call
//     LoadHTTPStoreConfig vs the filesystem path)
type HTTPStoreConfig struct {
	// Kind selects the Layout. Case-insensitive.
	//
	//   "httpstore"   — plain HTTP server with nginx-style HTML
	//                   autoindex listing (HTMLAutoindex). Works
	//                   against R2 with autoindex Worker, nginx,
	//                   Caddy, GitHub raw, Forgejo raw.
	//   "artifactory" — JFrog Artifactory generic repo
	//                   (artifactory.Layout). Requires
	//                   ArtifactoryRepo.
	Kind string

	// BaseURL is the HTTP root. For Artifactory deployments this
	// is typically `https://<host>/artifactory` (without the repo
	// name — that's threaded via ArtifactoryRepo into the storage
	// API path). Required.
	BaseURL string

	// ArtifactoryRepo is the Artifactory repository name. Required
	// when Kind="artifactory"; ignored otherwise.
	ArtifactoryRepo string

	// AuthKind selects the credential scheme. Case-insensitive.
	// Empty / "anonymous" produces httpstore.Anonymous (the
	// no-auth path, useful for public read-only mirrors).
	//
	//   "anonymous"           — no auth
	//   "bearer"              — Bearer <AuthFile-contents>
	//   "basic"               — Basic <AuthUser>:<AuthFile-contents>
	//   "artifactory-api-key" — X-JFrog-Art-Api: <AuthFile-contents>
	//                           (Artifactory's vendor-standard header)
	AuthKind string

	// AuthFile is the path to a 0600 file holding the credential
	// material (bearer token / basic password / artifactory API
	// key). Required when AuthKind is not "anonymous".
	//
	// File-path-only secret loading is intentional: the corporate-
	// security-first feedback rule rejects env-var-only secrets
	// (visible in `ps`, in docker inspect, in container env
	// dumps). Operators mount the file via docker secrets / K8s
	// Secret / similar.
	AuthFile string

	// AuthUser is the basic-auth username. Required when AuthKind
	// is "basic"; ignored otherwise.
	AuthUser string
}

// LoadHTTPStoreConfig reads BZLHUB_* env vars into an HTTPStoreConfig.
// Returns the empty config (zero Kind) when BZLHUB_BACKEND_KIND is
// unset — callers should treat that as "fall through to the
// filesystem path" rather than as an error.
//
// Env vars consumed:
//
//	BZLHUB_BACKEND_KIND       → Kind
//	BZLHUB_HTTPSTORE_URL      → BaseURL
//	BZLHUB_ARTIFACTORY_REPO   → ArtifactoryRepo
//	BZLHUB_HTTPSTORE_AUTH_KIND → AuthKind
//	BZLHUB_HTTPSTORE_AUTH_FILE → AuthFile
//	BZLHUB_HTTPSTORE_AUTH_USER → AuthUser
func LoadHTTPStoreConfig() HTTPStoreConfig {
	return HTTPStoreConfig{
		Kind:            strings.TrimSpace(os.Getenv("BZLHUB_BACKEND_KIND")),
		BaseURL:         strings.TrimSpace(os.Getenv("BZLHUB_HTTPSTORE_URL")),
		ArtifactoryRepo: strings.TrimSpace(os.Getenv("BZLHUB_ARTIFACTORY_REPO")),
		AuthKind:        strings.TrimSpace(os.Getenv("BZLHUB_HTTPSTORE_AUTH_KIND")),
		AuthFile:        strings.TrimSpace(os.Getenv("BZLHUB_HTTPSTORE_AUTH_FILE")),
		AuthUser:        strings.TrimSpace(os.Getenv("BZLHUB_HTTPSTORE_AUTH_USER")),
	}
}

// Set reports whether the config is non-empty (Kind set). False on
// the zero value, which serve.go uses to decide "fall through to
// the filesystem backend".
func (c HTTPStoreConfig) Set() bool { return c.Kind != "" }

// Build constructs an *HTTPStore from the config and the caller-
// supplied *http.Client. Validation fires before any network call —
// configuration errors surface at boot, not at first request.
//
// Returns an error describing the failed field/kind in operator-
// readable form (env-var names + expected values).
func (c HTTPStoreConfig) Build(httpClient *http.Client) (*HTTPStore, error) {
	if c.Kind == "" {
		return nil, errors.New("backend: BZLHUB_BACKEND_KIND is required (httpstore | artifactory)")
	}
	if c.BaseURL == "" {
		return nil, errors.New("backend: BZLHUB_HTTPSTORE_URL is required")
	}

	layout, err := c.layout()
	if err != nil {
		return nil, err
	}

	auth, err := c.auth()
	if err != nil {
		return nil, err
	}

	store, err := httpstore.New(httpstore.NewOptions{
		BaseURL: c.BaseURL,
		Auth:    auth,
		HTTP:    httpClient,
		Layout:  layout,
	})
	if err != nil {
		return nil, fmt.Errorf("backend: httpstore.New: %w", err)
	}
	return NewHTTPStore(store), nil
}

// layout picks the httpstore.Layout for the configured Kind. Returns
// an operator-readable error for unknown Kind values.
func (c HTTPStoreConfig) layout() (httpstore.Layout, error) {
	switch strings.ToLower(c.Kind) {
	case "httpstore":
		// nginx / Caddy / GitHub-raw / Forgejo-raw — autoindex
		// HTML listing.
		return httpstore.HTMLAutoindex{}, nil
	case "artifactory":
		if c.ArtifactoryRepo == "" {
			return nil, errors.New("backend: BZLHUB_ARTIFACTORY_REPO is required when BZLHUB_BACKEND_KIND=artifactory")
		}
		return artifactory.New(c.ArtifactoryRepo)
	default:
		return nil, fmt.Errorf("backend: unknown BZLHUB_BACKEND_KIND=%q (want httpstore | artifactory)", c.Kind)
	}
}

// auth resolves the AuthKind selector into a concrete httpstore.Auth.
// Reads AuthFile from disk at boot time (one read per process,
// callers wanting rotation wrap their own shim).
func (c HTTPStoreConfig) auth() (httpstore.Auth, error) {
	switch strings.ToLower(c.AuthKind) {
	case "", "anonymous":
		return httpstore.Anonymous{}, nil

	case "bearer":
		tok, err := readAuthSecret("bearer", c.AuthFile)
		if err != nil {
			return nil, err
		}
		return httpstore.BearerAuth{Token: tok}, nil

	case "basic":
		if c.AuthUser == "" {
			return nil, errors.New("backend: BZLHUB_HTTPSTORE_AUTH_USER is required for basic auth")
		}
		pw, err := readAuthSecret("basic", c.AuthFile)
		if err != nil {
			return nil, err
		}
		return httpstore.BasicAuth{User: c.AuthUser, Pass: pw}, nil

	case "artifactory-api-key":
		key, err := readAuthSecret("artifactory-api-key", c.AuthFile)
		if err != nil {
			return nil, err
		}
		return httpstore.CustomHeaderAuth{
			HeaderName: "X-JFrog-Art-Api",
			Value:      key,
		}, nil

	default:
		return nil, fmt.Errorf("backend: unknown BZLHUB_HTTPSTORE_AUTH_KIND=%q (want anonymous | bearer | basic | artifactory-api-key)", c.AuthKind)
	}
}

// readAuthSecret reads a one-line secret from path. Trims whitespace
// + trailing newline — operators frequently `echo "token" >`-write
// these files and a trailing \n would otherwise break Bearer / Basic
// signing in confusing ways.
//
// kind is just for the error message so operators see which
// credential failed without having to grep the env-var name.
func readAuthSecret(kind, path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("backend: BZLHUB_HTTPSTORE_AUTH_FILE is required for %s auth", kind)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("backend: read %s secret %q: %w", kind, path, err)
	}
	tok := strings.TrimSpace(string(raw))
	if tok == "" {
		return "", fmt.Errorf("backend: %s secret file %q is empty", kind, path)
	}
	return tok, nil
}
