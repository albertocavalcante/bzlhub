package server

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/albertocavalcante/bzlhub/internal/auth"
)

func bearerTestSHA(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func newTestBearerRegistry(t *testing.T, body string) *auth.IdentityRegistry {
	t.Helper()
	path := filepath.Join(t.TempDir(), "identity.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := auth.LoadIdentityFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// identityRecordingHandler is the test downstream — captures the
// identity FromContext so assertions can verify what arrived.
func identityRecordingHandler(captured *auth.Identity, mu *sync.Mutex) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		mu.Lock()
		*captured = id
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
}

func TestExtractBearerToken(t *testing.T) {
	cases := map[string]string{
		"":                  "",
		"Bearer ":           "",
		"Bearer abc":        "abc",
		"bearer abc":        "abc",
		"BEARER abc":        "abc",
		"Bearer\tabc":       "abc",
		"Bearer  abc":       "abc",
		"Basic abc":         "",
		"AnotherScheme abc": "",
		"Bearerabc":         "",        // missing separator between scheme and token
		"Bearer abc def":    "abc def", // remainder verbatim post-leading-trim
	}
	for input, want := range cases {
		got := extractBearerToken(input)
		if got != want {
			t.Errorf("extractBearerToken(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestBearerAuth_NilRegistry_PassThrough(t *testing.T) {
	var captured auth.Identity
	var mu sync.Mutex
	mw := bearerAuth(nil, nil)
	h := mw(identityRecordingHandler(&captured, &mu))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer some-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d", w.Code)
	}
	if captured.IsAuthenticated() {
		t.Errorf("nil registry must not authenticate; got identity %+v", captured)
	}
}

func TestBearerAuth_ValidToken_SetsIdentity(t *testing.T) {
	const token = "alice-secret-token-XXXXXXXXXX"
	body := `{"version": 1, "tokens": [{
		"token_sha256": "` + bearerTestSHA(token) + `",
		"identity": {"user": "alice@example.com", "email": "alice@example.com", "groups": ["eval-submitter"]}
	}]}`
	reg := newTestBearerRegistry(t, body)

	var captured auth.Identity
	var mu sync.Mutex
	mw := bearerAuth(reg, nil)
	h := mw(identityRecordingHandler(&captured, &mu))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d", w.Code)
	}
	if !captured.IsAuthenticated() {
		t.Fatal("identity not set on context")
	}
	if captured.Email != "alice@example.com" {
		t.Errorf("email = %q", captured.Email)
	}
	if captured.Source != auth.SourceBearer {
		t.Errorf("source = %q, want bearer", captured.Source)
	}
}

func TestBearerAuth_InvalidToken_StaysAnonymous(t *testing.T) {
	body := `{"version": 1, "tokens": [{
		"token_sha256": "` + bearerTestSHA("real-token") + `",
		"identity": {"user": "real-user"}
	}]}`
	reg := newTestBearerRegistry(t, body)

	var captured auth.Identity
	var mu sync.Mutex
	mw := bearerAuth(reg, nil)
	h := mw(identityRecordingHandler(&captured, &mu))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d (should pass through, not reject)", w.Code)
	}
	if captured.IsAuthenticated() {
		t.Errorf("invalid token must NOT authenticate; got %+v", captured)
	}
}

func TestBearerAuth_NoAuthHeader_StaysAnonymous(t *testing.T) {
	body := `{"version": 1, "tokens": [{
		"token_sha256": "` + bearerTestSHA("real-token") + `",
		"identity": {"user": "real-user"}
	}]}`
	reg := newTestBearerRegistry(t, body)

	var captured auth.Identity
	var mu sync.Mutex
	mw := bearerAuth(reg, nil)
	h := mw(identityRecordingHandler(&captured, &mu))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if captured.IsAuthenticated() {
		t.Error("no Authorization header should leave identity anonymous")
	}
}

func TestBearerAuth_NonBearerScheme_StaysAnonymous(t *testing.T) {
	body := `{"version": 1, "tokens": [{
		"token_sha256": "` + bearerTestSHA("real-token") + `",
		"identity": {"user": "real-user"}
	}]}`
	reg := newTestBearerRegistry(t, body)

	var captured auth.Identity
	var mu sync.Mutex
	mw := bearerAuth(reg, nil)
	h := mw(identityRecordingHandler(&captured, &mu))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if captured.IsAuthenticated() {
		t.Error("Basic auth header must not be parsed as bearer")
	}
}

// TestBearerAuth_BeatsHeaderAuth pins Plan 72 §CC3 precedence:
// when BOTH bearer + X-Forwarded-* arrive, bearer wins.
//
// This is an integration-shaped test that wires both middlewares
// to confirm the runtime order matches the documented contract.
func TestBearerAuth_BeatsHeaderAuth(t *testing.T) {
	const token = "bearer-wins-token-XXXXXXXX"
	body := `{"version": 1, "tokens": [{
		"token_sha256": "` + bearerTestSHA(token) + `",
		"identity": {"user": "bearer-user", "email": "bearer@example.com", "groups": ["bearer-group"]}
	}]}`
	reg := newTestBearerRegistry(t, body)

	cidrs, err := ParseTrustedProxyCIDRs("127.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}

	var captured auth.Identity
	var mu sync.Mutex
	// Compose both middlewares in the documented order.
	finalHandler := bearerAuth(reg, nil)(
		headerAuth(cidrs)(
			identityRecordingHandler(&captured, &mu),
		),
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Forwarded-User", "header-user")
	req.Header.Set("X-Forwarded-Email", "header@example.com")

	w := httptest.NewRecorder()
	finalHandler.ServeHTTP(w, req)

	if !captured.IsAuthenticated() {
		t.Fatal("identity not set")
	}
	if captured.Email != "bearer@example.com" {
		t.Errorf("bearer should win; got email %q", captured.Email)
	}
	if captured.Source != auth.SourceBearer {
		t.Errorf("source = %q, want bearer", captured.Source)
	}
}

// TestHeaderAuth_StillWorks_WhenBearerAbsent confirms the existing
// header-based flow is unchanged when no bearer is presented.
func TestHeaderAuth_StillWorks_WhenBearerAbsent(t *testing.T) {
	body := `{"version": 1, "tokens": [{
		"token_sha256": "` + bearerTestSHA("unused-token") + `",
		"identity": {"user": "unused"}
	}]}`
	reg := newTestBearerRegistry(t, body)

	cidrs, err := ParseTrustedProxyCIDRs("127.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}

	var captured auth.Identity
	var mu sync.Mutex
	finalHandler := bearerAuth(reg, nil)(
		headerAuth(cidrs)(
			identityRecordingHandler(&captured, &mu),
		),
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Forwarded-User", "header-user")
	req.Header.Set("X-Forwarded-Email", "header@example.com")
	req.Header.Set("X-Forwarded-Groups", "g1,g2")
	// No Authorization header.

	w := httptest.NewRecorder()
	finalHandler.ServeHTTP(w, req)

	if !captured.IsAuthenticated() {
		t.Fatal("header identity not set")
	}
	if captured.Email != "header@example.com" {
		t.Errorf("got email %q", captured.Email)
	}
	if captured.Source != auth.SourceHeader {
		t.Errorf("source = %q, want header", captured.Source)
	}
	if !strings.Contains(strings.Join(captured.Groups, ","), "g1") {
		t.Errorf("groups missing: %v", captured.Groups)
	}
}

func TestBearerAuth_Concurrent_NoRace(t *testing.T) {
	const token = "concurrent-token-XXXXXXXXX"
	body := `{"version": 1, "tokens": [{
		"token_sha256": "` + bearerTestSHA(token) + `",
		"identity": {"user": "concurrent"}
	}]}`
	reg := newTestBearerRegistry(t, body)

	mw := bearerAuth(reg, nil)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, _ := auth.FromContext(r.Context()); !id.IsAuthenticated() {
			t.Error("identity not set in concurrent goroutine")
		}
		w.WriteHeader(http.StatusOK)
	}))

	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
		})
	}
	wg.Wait()
}
