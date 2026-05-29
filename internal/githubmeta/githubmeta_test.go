package githubmeta

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseRepoLabel(t *testing.T) {
	cases := []struct {
		in              string
		ok              bool
		owner, repo     string
	}{
		{"bazelbuild/rules_go", true, "bazelbuild", "rules_go"},
		{" bazelbuild/rules_go ", true, "bazelbuild", "rules_go"},
		{"", false, "", ""},
		{"justname", false, "", ""},
		{"owner/", false, "", ""},
		{"/repo", false, "", ""},
		{"a/b/c", false, "", ""},
		{"owner repo", false, "", ""},
	}
	for _, tc := range cases {
		owner, repo, ok := ParseRepoLabel(tc.in)
		if ok != tc.ok || owner != tc.owner || repo != tc.repo {
			t.Errorf("ParseRepoLabel(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.in, owner, repo, ok, tc.owner, tc.repo, tc.ok)
		}
	}
}

func TestFetch_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/bazelbuild/rules_go":
			w.Header().Set("ETag", `W/"abc123"`)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
                "description": "Go rules for Bazel",
                "default_branch": "master",
                "language": "Go",
                "stargazers_count": 1500,
                "forks_count": 220,
                "subscribers_count": 60,
                "open_issues_count": 33
            }`))
		case "/repos/bazelbuild/rules_go/languages":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Go": 100000, "Starlark": 50000}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewClient(nil)
	c.BaseURL = srv.URL

	m, err := c.Fetch(context.Background(), "bazelbuild", "rules_go", "")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if m.Stars != 1500 || m.Forks != 220 || m.Watchers != 60 {
		t.Errorf("counts: stars=%d forks=%d watchers=%d", m.Stars, m.Forks, m.Watchers)
	}
	if m.PrimaryLanguage != "Go" {
		t.Errorf("primary lang = %q, want Go", m.PrimaryLanguage)
	}
	if m.Languages["Starlark"] != 50000 {
		t.Errorf("languages map missing Starlark: %v", m.Languages)
	}
	if m.ETag != `W/"abc123"` {
		t.Errorf("etag = %q", m.ETag)
	}
	if m.FetchedAt.IsZero() {
		t.Error("FetchedAt not set")
	}
}

func TestFetch_NotModified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != `W/"abc"` {
			t.Errorf("expected If-None-Match header, got %q", r.Header.Get("If-None-Match"))
		}
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()

	c := NewClient(nil)
	c.BaseURL = srv.URL
	_, err := c.Fetch(context.Background(), "o", "r", `W/"abc"`)
	if !errors.Is(err, ErrNotModified) {
		t.Fatalf("err = %v, want ErrNotModified", err)
	}
}

func TestFetch_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := NewClient(nil)
	c.BaseURL = srv.URL
	_, err := c.Fetch(context.Background(), "o", "r", "")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestFetch_RateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	c := NewClient(nil)
	c.BaseURL = srv.URL
	_, err := c.Fetch(context.Background(), "o", "r", "")
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
}

func TestFetch_LanguagesFailureNonFatal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/o/r" {
			_, _ = w.Write([]byte(`{"stargazers_count":1}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := NewClient(nil)
	c.BaseURL = srv.URL
	m, err := c.Fetch(context.Background(), "o", "r", "")
	if err != nil {
		t.Fatalf("expected non-fatal language failure; got %v", err)
	}
	if m.Stars != 1 || len(m.Languages) != 0 {
		t.Errorf("partial result wrong: stars=%d langs=%v", m.Stars, m.Languages)
	}
}

// TestFetch_RejectsOverCapBody — a compromised or misbehaving GitHub
// proxy serving a multi-GB JSON response must be rejected via
// ErrResponseTooLarge instead of being streamed into memory by the
// JSON decoder. Real GitHub repo responses are <2KB; the cap is
// orders of magnitude above legitimate use.
func TestFetch_RejectsOverCapBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Open the JSON, then stream a huge string field. JSON
		// decoder would otherwise allocate this whole field.
		_, _ = w.Write([]byte(`{"description":"`))
		// One byte over the cap of filler.
		buf := make([]byte, 4096)
		for i := range buf {
			buf[i] = 'x'
		}
		for written := int64(0); written <= MaxJSONResponseBytes; written += int64(len(buf)) {
			_, _ = w.Write(buf)
		}
		_, _ = w.Write([]byte(`"}`))
	}))
	defer srv.Close()

	c := NewClient(nil)
	c.BaseURL = srv.URL
	_, err := c.Fetch(context.Background(), "huge", "repo", "")
	if err == nil {
		t.Fatal("expected ErrResponseTooLarge, got nil")
	}
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Errorf("want ErrResponseTooLarge, got %v", err)
	}
}
