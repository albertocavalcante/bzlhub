package bigornatest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/albertocavalcante/bigorna"
)

// Factory is the per-provider hook into the conformance suite. Each
// provider implements once; RunConformance drives the same behavioral
// checks across every Factory it's given.
//
// Naming: provider adapter types are conventionally called Factory in
// their own test file and instantiated via NewFactory(). The
// conformance suite doesn't care about the type's name, only that it
// satisfies this interface.
type Factory interface {
	// Name identifies the provider in test output. Becomes the
	// sub-test prefix under RunConformance.
	Name() string

	// Repo returns the repo identifier the conformance tests use. The
	// returned bigorna.Repo must match whatever the factory's
	// NewClient is configured for.
	Repo() bigorna.Repo

	// NewClient constructs a bigorna.Forge wired to baseURL.
	//
	// CRITICAL: the returned client MUST have retries enabled but
	// instant (e.g., ManualClock with SkipSleeps engaged). The
	// retry test in RunConformance will time out if real-time sleeps
	// happen between attempts.
	NewClient(t *testing.T, baseURL string) bigorna.Forge

	// HealthOK returns a handler that responds successfully to a
	// Forge.Health() call.
	HealthOK() http.HandlerFunc

	// HealthStatus returns a handler that responds with the given
	// HTTP status code (e.g. 401, 403, 404). The body should match
	// the provider's error envelope when applicable.
	HealthStatus(status int) http.HandlerFunc

	// OpenPRIdempotencyHit returns a handler that, when called via
	// OpenPR, finds an existing PR matching opts.HeadBranch and
	// returns it. The create endpoint, if invoked, should fail
	// loudly (HTTP 500) so a regression in idempotency surfaces.
	OpenPRIdempotencyHit(existing bigorna.PR) http.HandlerFunc

	// OpenPRCreate returns a handler where the idempotency pre-check
	// finds no existing PR and the create endpoint returns the given
	// PR. The pre-check (if applicable) responds with no matching
	// open PRs.
	OpenPRCreate(newPR bigorna.PR) http.HandlerFunc

	// RetryThenSuccess returns a handler that 503s the first
	// numFailures calls, then 200s. Used to exercise the retry loop.
	// The handler must be safe for concurrent calls and track its
	// own state.
	RetryThenSuccess(numFailures int) http.HandlerFunc

	// ListNewCommitsChanged returns a handler responding with the
	// given commits via ListNewCommits.
	ListNewCommitsChanged(commits []bigorna.Commit) http.HandlerFunc

	// ListNewCommitsUnchanged returns a handler that signals "no new
	// commits" via the provider's native mechanism (304 for ETag-
	// style; empty values for since/until-style).
	ListNewCommitsUnchanged() http.HandlerFunc
}

// RunConformance executes the conformance suite against the given
// Factory. All tests run as sub-tests under t — use go test -v to
// see them individually, go test -run '<Factory>/<TestName>' to
// target one.
func RunConformance(t *testing.T, f Factory) {
	t.Helper()
	t.Run(f.Name(), func(t *testing.T) {
		t.Run("Health/OK", func(t *testing.T) { testHealthOK(t, f) })
		t.Run("Health/Unauthorized", func(t *testing.T) { testHealthStatus(t, f, http.StatusUnauthorized, bigorna.ErrUnauthorized) })
		t.Run("Health/Forbidden", func(t *testing.T) { testHealthStatus(t, f, http.StatusForbidden, bigorna.ErrUnauthorized) })
		t.Run("Health/NotFound", func(t *testing.T) { testHealthStatus(t, f, http.StatusNotFound, bigorna.ErrNotFound) })
		t.Run("Errors/HTTPErrorIntrospection", func(t *testing.T) { testHTTPErrorIntrospection(t, f) })
		t.Run("OpenPR/IdempotencyHit", func(t *testing.T) { testOpenPRIdempotencyHit(t, f) })
		t.Run("OpenPR/Create", func(t *testing.T) { testOpenPRCreate(t, f) })
		t.Run("OpenPR/RepoMismatchRejected", func(t *testing.T) { testOpenPRRepoMismatchRejected(t, f) })
		t.Run("ListNewCommits/Changed", func(t *testing.T) { testListNewCommitsChanged(t, f) })
		t.Run("ListNewCommits/Unchanged", func(t *testing.T) { testListNewCommitsUnchanged(t, f) })
		t.Run("Retry/Transient503", func(t *testing.T) { testRetryTransient503(t, f) })
	})
}

// ----- individual conformance tests -----

func testHealthOK(t *testing.T, f Factory) {
	srv := httptest.NewServer(f.HealthOK())
	t.Cleanup(srv.Close)
	c := f.NewClient(t, srv.URL)
	if err := c.Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
}

// testHealthStatus verifies that a non-2xx response is mapped to the
// expected sentinel AND wrapped in an HTTPError for introspection.
func testHealthStatus(t *testing.T, f Factory, status int, wantSentinel error) {
	srv := httptest.NewServer(f.HealthStatus(status))
	t.Cleanup(srv.Close)
	c := f.NewClient(t, srv.URL)
	err := c.Health(context.Background())
	if err == nil {
		t.Fatalf("expected error for HTTP %d", status)
	}
	if !errors.Is(err, wantSentinel) {
		t.Fatalf("HTTP %d: errors.Is failed; got %v, want %v", status, err, wantSentinel)
	}
	var hErr *bigorna.HTTPError
	if !errors.As(err, &hErr) {
		t.Fatalf("HTTP %d: errors.As to *HTTPError failed; got %v", status, err)
	}
	if hErr.Status != status {
		t.Errorf("HTTPError.Status: got %d, want %d", hErr.Status, status)
	}
}

// testHTTPErrorIntrospection: a 4xx response with a body must surface
// the body via HTTPError.Body so operators can debug from logs.
func testHTTPErrorIntrospection(t *testing.T, f Factory) {
	srv := httptest.NewServer(f.HealthStatus(http.StatusForbidden))
	t.Cleanup(srv.Close)
	c := f.NewClient(t, srv.URL)
	err := c.Health(context.Background())
	var hErr *bigorna.HTTPError
	if !errors.As(err, &hErr) {
		t.Fatalf("not HTTPError: %v", err)
	}
	if hErr.Method == "" {
		t.Errorf("HTTPError.Method should be non-empty")
	}
	if hErr.Path == "" {
		t.Errorf("HTTPError.Path should be non-empty")
	}
	if hErr.Status != http.StatusForbidden {
		t.Errorf("HTTPError.Status: got %d, want 403", hErr.Status)
	}
}

func testOpenPRIdempotencyHit(t *testing.T, f Factory) {
	existing := bigorna.PR{
		Number:     42,
		HeadBranch: "release/add-foo-1.0.0",
		BaseBranch: "main",
		State:      bigorna.PRStateOpen,
		URL:        "https://example.test/pulls/42",
	}
	srv := httptest.NewServer(f.OpenPRIdempotencyHit(existing))
	t.Cleanup(srv.Close)
	c := f.NewClient(t, srv.URL)

	pr, err := c.OpenPR(context.Background(), bigorna.OpenPROpts{
		Repo:       f.Repo(),
		Title:      "Add foo@1.0.0",
		Body:       "test",
		HeadBranch: existing.HeadBranch,
		BaseBranch: existing.BaseBranch,
	})
	if err != nil {
		t.Fatalf("OpenPR: %v", err)
	}
	if pr.Number != existing.Number {
		t.Errorf("expected existing PR #%d returned by idempotency check, got %d", existing.Number, pr.Number)
	}
}

func testOpenPRCreate(t *testing.T, f Factory) {
	newPR := bigorna.PR{
		Number:     99,
		HeadBranch: "release/add-foo-1.0.0",
		BaseBranch: "main",
		State:      bigorna.PRStateOpen,
		HeadSHA:    "abc123",
		URL:        "https://example.test/pulls/99",
	}
	srv := httptest.NewServer(f.OpenPRCreate(newPR))
	t.Cleanup(srv.Close)
	c := f.NewClient(t, srv.URL)

	pr, err := c.OpenPR(context.Background(), bigorna.OpenPROpts{
		Repo:       f.Repo(),
		Title:      "Add foo@1.0.0",
		Body:       "test",
		HeadBranch: newPR.HeadBranch,
		BaseBranch: newPR.BaseBranch,
	})
	if err != nil {
		t.Fatalf("OpenPR: %v", err)
	}
	if pr.Number != newPR.Number {
		t.Errorf("expected created PR #%d, got %d", newPR.Number, pr.Number)
	}
}

func testOpenPRRepoMismatchRejected(t *testing.T, f Factory) {
	// Calling OpenPR with a different repo than the Client was
	// configured for must error out before any HTTP call. We point
	// the server at a handler that fails the test if reached, so a
	// regression where the mismatch check is removed gets caught.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("repo-mismatch OpenPR should not reach server, but got %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	c := f.NewClient(t, srv.URL)

	wrong := bigorna.Repo{Owner: "different", Name: "wrong"}
	_, err := c.OpenPR(context.Background(), bigorna.OpenPROpts{
		Repo:       wrong,
		HeadBranch: "head", BaseBranch: "main",
	})
	if err == nil {
		t.Fatal("expected mismatch error")
	}
}

func testListNewCommitsChanged(t *testing.T, f Factory) {
	want := []bigorna.Commit{
		{SHA: "newer", Message: "newer commit", Author: "alice", AuthoredAt: time.Now().UTC().Truncate(time.Second)},
		{SHA: "older", Message: "older commit", Author: "alice", AuthoredAt: time.Now().UTC().Truncate(time.Second).Add(-time.Hour)},
	}
	srv := httptest.NewServer(f.ListNewCommitsChanged(want))
	t.Cleanup(srv.Close)
	c := f.NewClient(t, srv.URL)

	got, etag, notMod, err := c.ListNewCommits(context.Background(), f.Repo(), "main", "previous-sha", "previous-etag")
	if err != nil {
		t.Fatalf("ListNewCommits: %v", err)
	}
	if notMod {
		t.Errorf("notModified should be false when commits returned")
	}
	if len(got) != len(want) {
		t.Fatalf("got %d commits, want %d", len(got), len(want))
	}
	// Order semantics: forges return newest-first. The conformance
	// contract pins this — both bigorna providers do this today.
	if got[0].SHA != want[0].SHA {
		t.Errorf("first commit SHA: got %q, want %q (newest-first contract)", got[0].SHA, want[0].SHA)
	}
	_ = etag // forge-specific; per-provider tests pin exact semantics
}

func testListNewCommitsUnchanged(t *testing.T, f Factory) {
	srv := httptest.NewServer(f.ListNewCommitsUnchanged())
	t.Cleanup(srv.Close)
	c := f.NewClient(t, srv.URL)

	const inputETag = "previous-etag"
	got, etag, notMod, err := c.ListNewCommits(context.Background(), f.Repo(), "main", "previous-sha", inputETag)
	if err != nil {
		t.Fatalf("ListNewCommits: %v", err)
	}
	if !notMod {
		t.Errorf("notModified should be true on unchanged response")
	}
	if len(got) != 0 {
		t.Errorf("got %d commits, expected 0 on notModified", len(got))
	}
	// ETag preservation: forges without conditional-GET support echo
	// back the input etag so callers can round-trip it. Forges with
	// support return their own. Either is acceptable; what's NOT is
	// dropping it on the floor (which would force a cold-poll cycle
	// on the next call).
	if etag == "" {
		t.Errorf("etag dropped to empty on notModified; should round-trip (input %q)", inputETag)
	}
}

func testRetryTransient503(t *testing.T, f Factory) {
	// Two 503s then 200 — the retry loop should swallow the first two
	// and surface the third as success. ManualClock + SkipSleeps in
	// the factory's NewClient keeps this instant.
	srv := httptest.NewServer(f.RetryThenSuccess(2))
	t.Cleanup(srv.Close)
	c := f.NewClient(t, srv.URL)

	start := time.Now()
	if err := c.Health(context.Background()); err != nil {
		t.Fatalf("Health should succeed after retries: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("retry took %v — NewClient must inject a fast clock (ManualClock+SkipSleeps)", elapsed)
	}
}
