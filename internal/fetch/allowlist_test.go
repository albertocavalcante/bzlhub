package fetch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHostAllowed_EmptyListAllowsAll(t *testing.T) {
	c := &Client{}
	if !c.hostAllowed("https://bcr.bazel.build/modules/foo/1.0.0/source.json") {
		t.Fatal("empty allowlist should be no-op")
	}
	if !c.hostAllowed("https://attacker.example.com/x") {
		t.Fatal("empty allowlist should be no-op")
	}
}

func TestHostAllowed_ExactMatch(t *testing.T) {
	c := &Client{AllowedHosts: []string{"bcr.bazel.build"}}
	if !c.hostAllowed("https://bcr.bazel.build/x") {
		t.Fatal("exact host should match")
	}
	if c.hostAllowed("https://attacker.example.com/x") {
		t.Fatal("non-matching host must be denied")
	}
}

func TestHostAllowed_WildcardSubdomain(t *testing.T) {
	c := &Client{AllowedHosts: []string{"*.githubusercontent.com"}}
	if !c.hostAllowed("https://raw.githubusercontent.com/foo/bar") {
		t.Fatal("subdomain should match *.githubusercontent.com")
	}
	if !c.hostAllowed("https://avatars.githubusercontent.com/u/1") {
		t.Fatal("another subdomain should match")
	}
	// Apex must NOT match a *. pattern alone.
	if c.hostAllowed("https://githubusercontent.com/x") {
		t.Fatal("*.foo must not match apex foo")
	}
	if c.hostAllowed("https://evil.com/x") {
		t.Fatal("unrelated host must be denied")
	}
}

func TestHostAllowed_CaseInsensitive(t *testing.T) {
	c := &Client{AllowedHosts: []string{"BCR.Bazel.Build"}}
	if !c.hostAllowed("https://bcr.bazel.build/x") {
		t.Fatal("case-insensitive match expected")
	}
}

func TestHostAllowed_UnparseableURLDenied(t *testing.T) {
	c := &Client{AllowedHosts: []string{"bcr.bazel.build"}}
	if c.hostAllowed("not-a-url") {
		t.Fatal("unparseable URL must fail closed")
	}
}

func TestGet_DeniedHostReturnsErrEgressDenied(t *testing.T) {
	// Spin up a real test server we'd normally fetch from. The
	// allowlist excludes it; the request should fail before any
	// network hit.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("server should never be hit when egress is denied")
	}))
	defer srv.Close()

	c := &Client{
		HTTP:         &http.Client{},
		AllowedHosts: []string{"bcr.bazel.build"}, // intentionally not localhost
	}
	_, err := c.get(context.Background(), srv.URL+"/x")
	if !errors.Is(err, ErrEgressDenied) {
		t.Fatalf("err = %v, want ErrEgressDenied", err)
	}
}

func TestGet_AllowedHostPasses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := &Client{HTTP: &http.Client{}, AllowedHosts: []string{"127.0.0.1"}}
	resp, err := c.get(context.Background(), srv.URL+"/x")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	resp.Body.Close()
}

func TestNewClient_InheritsDefaultAllowedHostsSnapshot(t *testing.T) {
	SetDefaultAllowedHosts(ParseAllowedHosts("bcr.bazel.build, *.githubusercontent.com"))
	t.Cleanup(func() { SetDefaultAllowedHosts(nil) })

	c := NewClient()
	if !c.hostAllowed("https://bcr.bazel.build/modules/foo/metadata.json") {
		t.Fatal("NewClient should inherit exact default allowlist entry")
	}
	if !c.hostAllowed("https://raw.githubusercontent.com/owner/repo/archive/v1.tar.gz") {
		t.Fatal("NewClient should inherit wildcard default allowlist entry")
	}

	SetDefaultAllowedHosts([]string{"attacker.example"})
	if !c.hostAllowed("https://bcr.bazel.build/modules/foo/metadata.json") {
		t.Fatal("NewClient should keep a snapshot, not observe later global mutations")
	}
}

func TestNewClient_DeniesRedirectTargetOutsideAllowlist(t *testing.T) {
	SetDefaultAllowedHosts([]string{"127.0.0.1"})
	t.Cleanup(func() { SetDefaultAllowedHosts(nil) })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://denied.invalid/archive.tar.gz", http.StatusFound)
	}))
	defer srv.Close()

	c := NewClient()
	_, err := c.get(context.Background(), srv.URL+"/source.tar.gz")
	if !errors.Is(err, ErrEgressDenied) {
		t.Fatalf("err = %v, want ErrEgressDenied", err)
	}
}
