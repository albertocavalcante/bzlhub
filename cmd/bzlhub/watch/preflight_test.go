package watch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/albertocavalcante/bigorna"
)

// startGithubHealthServer spins up an httptest.Server that responds to
// GitHub's repo-health endpoint (GET /repos/<owner>/<name>) with the
// given status + body. Returns the base URL.
func startGithubHealthServer(t *testing.T, status int, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/repos/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestPreflightWatchForge_HappyPath(t *testing.T) {
	base := startGithubHealthServer(t, http.StatusOK, `{"full_name":"o/r"}`)
	cfg := watchConfig{
		forge:   "github",
		repo:    bigorna.Repo{Owner: "o", Name: "r"},
		baseURL: base,
		token:   "ghp_abc",
	}
	c, err := preflightWatchForge(context.Background(), cfg)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if c == nil {
		t.Error("nil forge on success")
	}
}

func TestPreflightWatchForge_HealthFailureWrapped(t *testing.T) {
	base := startGithubHealthServer(t, http.StatusForbidden, `{"message":"no go"}`)
	cfg := watchConfig{
		forge:   "github",
		repo:    bigorna.Repo{Owner: "o", Name: "r"},
		baseURL: base,
		token:   "ghp_abc",
	}
	_, err := preflightWatchForge(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "forge health check failed") {
		t.Fatalf("want wrapped health error, got %v", err)
	}
}
