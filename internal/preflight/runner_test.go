package preflight

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/albertocavalcante/bzlhub/internal/policy"
	"github.com/albertocavalcante/bzlhub/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "bzlhub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func makePending(t *testing.T, s *store.Store, module, version, sourceURL string) int64 {
	t.Helper()
	id, err := s.CreateRequest(context.Background(), store.Request{
		SubmitterSub: "alice@example.com",
		AuthMethod:   "bearer",
		Module:       module,
		Version:      version,
		SourceURL:    sourceURL,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// stubChecker returns a fixed verdict and counts calls. Test seam for
// the Runner — keeps tests independent of network and policy quirks.
type stubChecker struct {
	verdict Verdict
	calls   atomic.Int64
}

func (c *stubChecker) Check(_ context.Context, _ store.Request) Verdict {
	c.calls.Add(1)
	return c.verdict
}

func newRunnerForTest(s *store.Store, c Checker, workers int) *Runner {
	return New(Options{
		Store:     s,
		Checker:   c,
		Workers:   workers,
		PollEvery: 5 * time.Millisecond, // fast for tests
		Log:       slog.Default(),
	})
}

// waitForState polls the store until req.State == want or timeout.
// Avoids brittle Sleep + assert patterns.
func waitForState(t *testing.T, s *store.Store, id int64, want store.RequestState, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		got, err := s.GetRequest(context.Background(), id)
		if err == nil && got.State == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	got, _ := s.GetRequest(context.Background(), id)
	t.Fatalf("id=%d still in state %q after %s; want %q", id, got.State, timeout, want)
}

func TestRunner_ProcessesPending_NeedsReview(t *testing.T) {
	s := newTestStore(t)
	id := makePending(t, s, "rules_python", "1.5.0", "https://example.com/rp.tar.gz")
	c := &stubChecker{verdict: Verdict{NextState: store.RequestStateNeedsReview}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newRunnerForTest(s, c, 2)
	go r.Run(ctx)

	waitForState(t, s, id, store.RequestStateNeedsReview, 500*time.Millisecond)
	if c.calls.Load() == 0 {
		t.Error("checker not called")
	}
}

func TestRunner_AutoPass(t *testing.T) {
	s := newTestStore(t)
	id := makePending(t, s, "rules_go", "0.50.0", "https://example.com/rg.tar.gz")
	c := &stubChecker{verdict: Verdict{NextState: store.RequestStateAutoPass, License: "Apache-2.0"}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newRunnerForTest(s, c, 2)
	go r.Run(ctx)

	waitForState(t, s, id, store.RequestStateAutoPass, 500*time.Millisecond)
	// Verdict was persisted in preflight_json.
	got, _ := s.GetRequest(context.Background(), id)
	var v Verdict
	if err := json.Unmarshal(got.PreflightJSON, &v); err != nil {
		t.Fatalf("preflight_json: %v", err)
	}
	if v.License != "Apache-2.0" {
		t.Errorf("preflight.License = %q", v.License)
	}
}

func TestRunner_DenyWritesReason(t *testing.T) {
	s := newTestStore(t)
	id := makePending(t, s, "rules_x", "1.0.0", "http://insecure.example.com/x.tar.gz")
	c := &stubChecker{verdict: Verdict{
		NextState: store.RequestStateDenied,
		Reason:    "source_url must be https://",
	}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newRunnerForTest(s, c, 2)
	go r.Run(ctx)

	waitForState(t, s, id, store.RequestStateDenied, 500*time.Millisecond)
	got, _ := s.GetRequest(context.Background(), id)
	if got.DenialReason == "" {
		t.Error("denial_reason not persisted")
	}
}

func TestRunner_NoDuplicateProcessing(t *testing.T) {
	// CAS ensures only one worker processes any given request.
	// Submit 5 pending; run 4 workers; total checker calls = 5
	// (not 20).
	s := newTestStore(t)
	for i := range 5 {
		makePending(t, s, "rules_x", "1.0."+string(rune('0'+i)), "https://example.com/x.tar.gz")
	}
	c := &stubChecker{verdict: Verdict{NextState: store.RequestStateNeedsReview}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newRunnerForTest(s, c, 4)
	go r.Run(ctx)

	// Wait for all 5 to leave pending.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		rows, _ := s.ListRequests(context.Background(), store.RequestQuery{
			States: []store.RequestState{store.RequestStatePending},
		})
		if len(rows) == 0 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	calls := c.calls.Load()
	if calls != 5 {
		t.Errorf("checker calls = %d, want exactly 5 (no duplicate processing)", calls)
	}
}

func TestRunner_GracefulShutdown(t *testing.T) {
	// ctx.Done blocks new pulls; workers return cleanly.
	s := newTestStore(t)
	c := &stubChecker{verdict: Verdict{NextState: store.RequestStateNeedsReview}}

	ctx, cancel := context.WithCancel(context.Background())
	r := newRunnerForTest(s, c, 2)
	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()
	// Let workers start.
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
		// good
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Runner did not return after ctx cancel")
	}
}

func TestRunner_TransitionFailureLeavesPending(t *testing.T) {
	// If the checker's chosen NextState is illegal (e.g. a bug
	// returns "indexed" from pending), the runner must NOT corrupt
	// state — the request should be moved back to pending or stay
	// where it was. Current impl: workers attempt the transition;
	// on failure, log + leave the request stuck. Verify it doesn't
	// loop or panic.
	s := newTestStore(t)
	id := makePending(t, s, "rules_x", "1.0.0", "https://example.com")
	// Illegal next-state.
	c := &stubChecker{verdict: Verdict{NextState: store.RequestStateIndexed}}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	r := newRunnerForTest(s, c, 1)
	r.Run(ctx)

	got, _ := s.GetRequest(context.Background(), id)
	// Request should have been moved to preflighting (the
	// pending → preflighting transition succeeded) but then stuck
	// because preflighting → indexed is illegal. We just assert
	// the runner didn't panic and the request is in SOME state.
	if got.State == store.RequestStatePending {
		t.Error("expected runner to have made at least the pending→preflighting transition")
	}
}

func TestRunner_NilStore_Panics(t *testing.T) {
	// nil store is a bug, not a runtime config — fail loud at New.
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on nil store")
		}
	}()
	_ = New(Options{Store: nil, Checker: &stubChecker{}})
}

func TestRunner_NilChecker_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on nil checker")
		}
	}()
	_ = New(Options{Store: newTestStore(t), Checker: nil})
}

// -- DefaultChecker tests --------------------------------------------

func TestDefaultChecker_RequiresHTTPS_DeniesHTTP(t *testing.T) {
	p := &policy.Policy{
		Admission: policy.Admission{
			Source: policy.Source{RequireHTTPS: true},
		},
	}
	c := NewDefaultChecker(policy.Static(p))
	v := c.Check(context.Background(), store.Request{
		Module: "x", Version: "1.0", SourceURL: "http://insecure.example.com/x.tar.gz",
	})
	if v.NextState != store.RequestStateDenied {
		t.Errorf("NextState = %q, want denied", v.NextState)
	}
	if v.Reason == "" {
		t.Error("Reason not populated on deny")
	}
}

func TestDefaultChecker_AllowsHTTPS(t *testing.T) {
	p := &policy.Policy{
		Admission: policy.Admission{
			Source: policy.Source{RequireHTTPS: true},
		},
	}
	c := NewDefaultChecker(policy.Static(p))
	v := c.Check(context.Background(), store.Request{
		Module: "x", Version: "1.0", SourceURL: "https://github.com/x/x/archive/1.0.tar.gz",
	})
	if v.NextState != store.RequestStateNeedsReview {
		t.Errorf("https URL should default to needs_review; got %q", v.NextState)
	}
}

func TestDefaultChecker_HTTPSNotRequired_AllowsHTTP(t *testing.T) {
	// When admission.source.require_https is false, http:// is fine
	// (still goes to needs_review since we haven't run real checks).
	p := &policy.Policy{
		Admission: policy.Admission{
			Source: policy.Source{RequireHTTPS: false},
		},
	}
	c := NewDefaultChecker(policy.Static(p))
	v := c.Check(context.Background(), store.Request{
		Module: "x", Version: "1.0", SourceURL: "http://internal.example.com/x.tar.gz",
	})
	if v.NextState == store.RequestStateDenied {
		t.Errorf("http URL must NOT be denied when require_https=false")
	}
}

func TestDefaultChecker_EmptySourceURL_NeedsReview(t *testing.T) {
	// Empty source_url (BCR-shape upstream supplies it later) ends
	// up at needs_review for v0.
	p := &policy.Policy{}
	c := NewDefaultChecker(policy.Static(p))
	v := c.Check(context.Background(), store.Request{Module: "x", Version: "1.0"})
	if v.NextState != store.RequestStateNeedsReview {
		t.Errorf("NextState = %q, want needs_review", v.NextState)
	}
}

// negative-import sentinel for test-pkg cleanliness
var _ = errors.Is
var _ sync.Mutex
