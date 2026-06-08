package preflight

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/albertocavalcante/bzlhub/internal/policy"
	"github.com/albertocavalcante/bzlhub/internal/store"
)

// stubCascade lets tests force the probe verdict.
type stubCascade struct {
	hit *CascadeHit
	err error
}

func (s *stubCascade) Has(_ context.Context, _, _ string) (*CascadeHit, error) {
	return s.hit, s.err
}

func TestDefaultChecker_Cascade_AutoPassWhenInUpstream(t *testing.T) {
	p := &policy.Policy{
		Admission: policy.Admission{
			Review: policy.Review{AutoPassOnAlreadyInUpstream: true},
		},
	}
	c := NewDefaultChecker(policy.Static(p))
	c.Cascade = &stubCascade{hit: &CascadeHit{URL: "https://example.com/a.tar.gz", Integrity: "sha256-xyz"}}

	v := c.Check(context.Background(), store.Request{
		Module: "rules_python", Version: "1.5.0",
		SourceURL: "https://github.com/x/y/archive/v1.5.0.tar.gz",
	})
	if v.NextState != store.RequestStateAutoPass {
		t.Errorf("NextState = %q, want auto_pass", v.NextState)
	}
	if !strings.Contains(v.Notes, "upstream") {
		t.Errorf("Notes = %q, expected to mention upstream", v.Notes)
	}
}

func TestDefaultChecker_Cascade_NeedsReviewWhenNotInUpstream(t *testing.T) {
	p := &policy.Policy{
		Admission: policy.Admission{
			Review: policy.Review{AutoPassOnAlreadyInUpstream: true},
		},
	}
	c := NewDefaultChecker(policy.Static(p))
	c.Cascade = &stubCascade{hit: nil}

	v := c.Check(context.Background(), store.Request{
		Module: "private_mod", Version: "1.0",
		SourceURL: "https://github.com/x/y/archive/v1.tar.gz",
	})
	if v.NextState != store.RequestStateNeedsReview {
		t.Errorf("NextState = %q, want needs_review", v.NextState)
	}
}

func TestDefaultChecker_Cascade_RespectsPolicyFlag(t *testing.T) {
	// auto_pass_on_already_in_upstream=false → don't short-circuit
	// even when the module IS in upstream.
	p := &policy.Policy{
		Admission: policy.Admission{
			Review: policy.Review{AutoPassOnAlreadyInUpstream: false},
		},
	}
	c := NewDefaultChecker(policy.Static(p))
	c.Cascade = &stubCascade{hit: &CascadeHit{URL: "https://example.com/a.tar.gz", Integrity: "sha256-xyz"}}

	v := c.Check(context.Background(), store.Request{
		Module: "rules_python", Version: "1.5.0",
		SourceURL: "https://github.com/x/y.tar.gz",
	})
	if v.NextState != store.RequestStateNeedsReview {
		t.Errorf("auto_pass flag off should defeat cascade; got %q", v.NextState)
	}
}

func TestDefaultChecker_Cascade_NetworkErrorDegradesToNeedsReview(t *testing.T) {
	// Cascade probe failures (DNS down, upstream 500) should NOT
	// deny the request. Degrade to needs_review (human looks).
	p := &policy.Policy{
		Admission: policy.Admission{
			Review: policy.Review{AutoPassOnAlreadyInUpstream: true},
		},
	}
	c := NewDefaultChecker(policy.Static(p))
	c.Cascade = &stubCascade{err: errors.New("upstream timeout")}

	v := c.Check(context.Background(), store.Request{
		Module: "x", Version: "1.0",
		SourceURL: "https://example.com/x.tar.gz",
	})
	if v.NextState != store.RequestStateNeedsReview {
		t.Errorf("cascade error should degrade to needs_review; got %q", v.NextState)
	}
}

func TestDefaultChecker_AllowedHosts_DeniesNotInList(t *testing.T) {
	p := &policy.Policy{
		Admission: policy.Admission{
			Source: policy.Source{
				AllowedHosts: []string{"github.com", "gitlab.com"},
			},
		},
	}
	c := NewDefaultChecker(policy.Static(p))

	v := c.Check(context.Background(), store.Request{
		Module: "x", Version: "1.0",
		SourceURL: "https://untrusted.example.com/x.tar.gz",
	})
	if v.NextState != store.RequestStateDenied {
		t.Errorf("NextState = %q, want denied", v.NextState)
	}
	if !strings.Contains(v.Reason, "untrusted.example.com") {
		t.Errorf("Reason should mention the offending host: %q", v.Reason)
	}
}

func TestDefaultChecker_AllowedHosts_AllowsInList(t *testing.T) {
	p := &policy.Policy{
		Admission: policy.Admission{
			Source: policy.Source{
				AllowedHosts: []string{"github.com", "gitlab.com"},
			},
		},
	}
	c := NewDefaultChecker(policy.Static(p))

	v := c.Check(context.Background(), store.Request{
		Module: "x", Version: "1.0",
		SourceURL: "https://github.com/x/y/archive/v1.tar.gz",
	})
	if v.NextState == store.RequestStateDenied {
		t.Errorf("github.com URL should not be denied by allowed_hosts gate")
	}
}

func TestDefaultChecker_AllowedHosts_EmptyAllowsAll(t *testing.T) {
	p := &policy.Policy{} // empty allowed_hosts → no enforcement
	c := NewDefaultChecker(policy.Static(p))

	v := c.Check(context.Background(), store.Request{
		Module: "x", Version: "1.0",
		SourceURL: "https://random.example.com/x.tar.gz",
	})
	if v.NextState == store.RequestStateDenied {
		t.Errorf("empty allowed_hosts must NOT enforce; got denied")
	}
}

func TestDefaultChecker_DenylistedHosts_Denies(t *testing.T) {
	p := &policy.Policy{
		Admission: policy.Admission{
			Source: policy.Source{
				DenylistedHosts: []string{"evil.example.com"},
			},
		},
	}
	c := NewDefaultChecker(policy.Static(p))

	v := c.Check(context.Background(), store.Request{
		Module: "x", Version: "1.0",
		SourceURL: "https://evil.example.com/x.tar.gz",
	})
	if v.NextState != store.RequestStateDenied {
		t.Errorf("NextState = %q, want denied", v.NextState)
	}
}

// -- BCRProbe (real GET source.json) ---------------------------------

const sampleSourceJSON = `{
  "url": "https://github.com/bazelbuild/rules_python/archive/refs/tags/1.5.0.tar.gz",
  "integrity": "sha256-abc123==",
  "strip_prefix": "rules_python-1.5.0"
}`

func TestBCRProbe_HappyPath_ReturnsParsedHit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/modules/rules_python/1.5.0/source.json" {
			_, _ = w.Write([]byte(sampleSourceJSON))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	probe := NewBCRProbe(srv.URL, http.DefaultClient)
	hit, err := probe.Has(context.Background(), "rules_python", "1.5.0")
	if err != nil {
		t.Fatal(err)
	}
	if hit == nil {
		t.Fatal("want a hit for a known module")
	}
	if hit.URL == "" || hit.Integrity == "" {
		t.Errorf("hit fields not populated: %+v", hit)
	}
	if hit.StripPrefix != "rules_python-1.5.0" {
		t.Errorf("strip_prefix = %q", hit.StripPrefix)
	}
}

func TestBCRProbe_404_ReturnsNilHit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	probe := NewBCRProbe(srv.URL, http.DefaultClient)
	hit, err := probe.Has(context.Background(), "private_mod", "1.0")
	if err != nil {
		t.Fatal(err)
	}
	if hit != nil {
		t.Errorf("want nil hit for unknown module, got %+v", hit)
	}
}

func TestBCRProbe_5xx_ReturnsError(t *testing.T) {
	// 5xx is ambiguous (upstream might have the module but be flaky);
	// surface as error so the checker can degrade to needs_review.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	probe := NewBCRProbe(srv.URL, http.DefaultClient)
	_, err := probe.Has(context.Background(), "x", "1.0")
	if err == nil {
		t.Error("5xx should surface as error")
	}
}

func TestBCRProbe_MalformedJSON_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	probe := NewBCRProbe(srv.URL, http.DefaultClient)
	_, err := probe.Has(context.Background(), "x", "1.0")
	if err == nil {
		t.Error("malformed source.json should surface as error")
	}
}

func TestBCRProbe_MissingFields_ReturnsError(t *testing.T) {
	// source.json missing url + integrity is unusable for cascade
	// (admit needs both). Surface as error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"strip_prefix": "x"}`))
	}))
	defer srv.Close()

	probe := NewBCRProbe(srv.URL, http.DefaultClient)
	_, err := probe.Has(context.Background(), "x", "1.0")
	if err == nil {
		t.Error("source.json without url/integrity should surface as error")
	}
}
