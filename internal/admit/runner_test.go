package admit

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/albertocavalcante/bzlhub/internal/preflight"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/albertocavalcante/bzlhub/internal/publish"
	"github.com/albertocavalcante/bzlhub/internal/purge"
	"github.com/albertocavalcante/bzlhub/internal/store"
)

// stubFetcher writes a canned payload to sink and ignores url.
type stubFetcher struct {
	payload []byte
	err     error
	calls   atomic.Int64
}

func (f *stubFetcher) Fetch(_ context.Context, _ string, sink io.Writer) (int64, error) {
	f.calls.Add(1)
	if f.err != nil {
		return 0, f.err
	}
	n, err := sink.Write(f.payload)
	return int64(n), err
}

// makeTarGz returns a gzipped tar with the given (path, content)
// pairs. Used to fake a real source archive in tests.
func makeTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(body)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func newAdmitTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "bzlhub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func driveToApproved(t *testing.T, s *store.Store, module, version, sourceURL string) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := s.CreateRequest(ctx, store.Request{
		SubmitterSub: "alice@example.com",
		AuthMethod:   "bearer",
		Module:       module,
		Version:      version,
		SourceURL:    sourceURL,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ from, to store.RequestState }{
		{store.RequestStatePending, store.RequestStatePreflighting},
		{store.RequestStatePreflighting, store.RequestStateNeedsReview},
		{store.RequestStateNeedsReview, store.RequestStateApproved},
	} {
		if err := s.TransitionRequest(ctx, id, step.from, step.to, nil); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func newAdmitRunnerForTest(t *testing.T, s *store.Store, f Fetcher) (*Runner, string) {
	t.Helper()
	root := t.TempDir()
	pub, err := publish.NewFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	return New(Options{
		Store:     s,
		Publisher: pub,
		Fetcher:   f,
		BotIdent:  publish.Identity{Name: "bzlhub-bot", Email: "bot@example.com"},
		Workers:   1,
		PollEvery: 5 * time.Millisecond,
		Log:       slog.Default(),
	}), root
}

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
	t.Fatalf("id=%d state=%q after %s; want %q", id, got.State, timeout, want)
}

func TestRunner_HappyPath_IndexesApproved(t *testing.T) {
	s := newAdmitTestStore(t)
	tarball := makeTarGz(t, map[string]string{
		"rules_python-1.5.0/MODULE.bazel": "module(name = \"rules_python\", version = \"1.5.0\")\n",
		"rules_python-1.5.0/BUILD":        "",
	})
	f := &stubFetcher{payload: tarball}
	id := driveToApproved(t, s, "rules_python", "1.5.0", "https://example.com/rp.tar.gz")

	r, _ := newAdmitRunnerForTest(t, s, f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)

	waitForState(t, s, id, store.RequestStateIndexed, 500*time.Millisecond)
	if f.calls.Load() != 1 {
		t.Errorf("fetcher calls = %d, want 1", f.calls.Load())
	}

	// Audit event was written.
	events, _ := s.ListAudit(context.Background(), store.AuditQuery{Kinds: []string{"admit_success"}})
	if len(events) != 1 {
		t.Errorf("audit rows = %d, want 1", len(events))
	}
}

func TestRunner_AlsoPicksUpAutoPass(t *testing.T) {
	s := newAdmitTestStore(t)
	tarball := makeTarGz(t, map[string]string{
		"foo-1.0/MODULE.bazel": "module(name=\"foo\")",
	})
	f := &stubFetcher{payload: tarball}
	ctx := context.Background()
	id, _ := s.CreateRequest(ctx, store.Request{
		SubmitterSub: "x", AuthMethod: "bearer",
		Module: "foo", Version: "1.0", SourceURL: "https://example.com/foo.tar.gz",
	})
	// pending → preflighting → auto_pass (skipping needs_review/approve)
	_ = s.TransitionRequest(ctx, id, store.RequestStatePending, store.RequestStatePreflighting, nil)
	_ = s.TransitionRequest(ctx, id, store.RequestStatePreflighting, store.RequestStateAutoPass, nil)

	r, _ := newAdmitRunnerForTest(t, s, f)
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(runCtx)

	waitForState(t, s, id, store.RequestStateIndexed, 500*time.Millisecond)
}

func TestRunner_FetchFailure_DeniesWithReason(t *testing.T) {
	s := newAdmitTestStore(t)
	id := driveToApproved(t, s, "broken", "1.0", "https://example.com/dead.tar.gz")
	f := &stubFetcher{err: io.ErrUnexpectedEOF}

	r, _ := newAdmitRunnerForTest(t, s, f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)

	waitForState(t, s, id, store.RequestStateDenied, 500*time.Millisecond)
	got, _ := s.GetRequest(context.Background(), id)
	if got.DenialReason == "" {
		t.Error("denial_reason not persisted")
	}
	if got.RetryCount != 0 {
		t.Errorf("terminal failure should not retry: retry_count=%d, want 0", got.RetryCount)
	}

	events, _ := s.ListAudit(context.Background(), store.AuditQuery{Kinds: []string{"admit_failure"}})
	if len(events) != 1 {
		t.Errorf("admit_failure audit rows = %d, want 1", len(events))
	}
}

func TestRunner_MaterializesOnDiskAndReceiptCarriesPath(t *testing.T) {
	s := newAdmitTestStore(t)
	tarball := makeTarGz(t, map[string]string{
		"rules_x-2.0/MODULE.bazel": "module(name=\"rules_x\", version=\"2.0\")",
	})
	f := &stubFetcher{payload: tarball}
	id := driveToApproved(t, s, "rules_x", "2.0", "https://example.com/rx.tar.gz")

	r, root := newAdmitRunnerForTest(t, s, f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)

	waitForState(t, s, id, store.RequestStateIndexed, 500*time.Millisecond)

	// FilesystemPublisher writes modules/<m>/<v>/{source.json, MODULE.bazel}.
	got, _ := s.GetRequest(context.Background(), id)
	if got.CommittedSHA == "" && got.FetchedSHA == "" {
		t.Error("neither FetchedSHA nor CommittedSHA persisted")
	}
	// Sanity: the on-disk BCR shape exists.
	srcJSON := filepath.Join(root, "modules", "rules_x", "2.0", "source.json")
	if _, err := os.ReadFile(srcJSON); err != nil {
		t.Errorf("source.json not materialized at %s: %v", srcJSON, err)
	}
}

// TestRunner_FallsBackToCascadeSource pins slice θ: when the
// request had no source_url but the preflight verdict carries a
// CascadeSource (BCR hit), admit fetches from the cascade URL.
func TestRunner_FallsBackToCascadeSource(t *testing.T) {
	s := newAdmitTestStore(t)
	tarball := makeTarGz(t, map[string]string{
		"cascade-source-1.0/MODULE.bazel": "module(name=\"cascade_source\")",
	})
	f := &stubFetcher{payload: tarball}

	// Make a request with NO source_url; populate preflight_json
	// with a CascadeSource carrying a stand-in URL the stub
	// fetcher will ignore (it returns canned payload regardless).
	ctx := context.Background()
	id, err := s.CreateRequest(ctx, store.Request{
		SubmitterSub: "alice@example.com",
		AuthMethod:   "bearer",
		Module:       "cascade_source",
		Version:      "1.0",
		// SourceURL deliberately empty
	})
	if err != nil {
		t.Fatal(err)
	}
	cascadeJSON, _ := json.Marshal(preflight.Verdict{
		NextState: store.RequestStateAutoPass,
		CascadeSource: &preflight.CascadeHit{
			URL:       "https://bcr.bazel.build/modules/cascade_source/1.0/source.tar.gz",
			Integrity: "sha256-fromcascade",
		},
	})
	for _, step := range []struct{ from, to store.RequestState }{
		{store.RequestStatePending, store.RequestStatePreflighting},
		{store.RequestStatePreflighting, store.RequestStateAutoPass},
	} {
		if err := s.TransitionRequest(ctx, id, step.from, step.to, &store.RequestFields{
			PreflightJSON: cascadeJSON,
		}); err != nil {
			t.Fatal(err)
		}
	}

	r, _ := newAdmitRunnerForTest(t, s, f)
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(runCtx)

	waitForState(t, s, id, store.RequestStateIndexed, 500*time.Millisecond)
	if f.calls.Load() != 1 {
		t.Errorf("fetcher calls = %d, want 1 (cascade URL was used)", f.calls.Load())
	}
}

// TestRunner_NoURLAndNoCascade_Denies pins the "no source at all"
// case: admit should transition to denied with a useful reason
// rather than crashing.
func TestRunner_NoURLAndNoCascade_Denies(t *testing.T) {
	s := newAdmitTestStore(t)
	f := &stubFetcher{payload: []byte("never read")}

	ctx := context.Background()
	id, err := s.CreateRequest(ctx, store.Request{
		SubmitterSub: "x", AuthMethod: "bearer",
		Module: "lonely_mod", Version: "1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ from, to store.RequestState }{
		{store.RequestStatePending, store.RequestStatePreflighting},
		{store.RequestStatePreflighting, store.RequestStateApproved}, // skipping needs_review for test brevity — illegal in the real graph
	} {
		// the real graph forbids preflighting → approved, so go through
		// the legal path:
		if step.to == store.RequestStateApproved {
			_ = s.TransitionRequest(ctx, id, store.RequestStatePreflighting, store.RequestStateNeedsReview, nil)
			step.from = store.RequestStateNeedsReview
		}
		if err := s.TransitionRequest(ctx, id, step.from, step.to, nil); err != nil {
			t.Fatal(err)
		}
	}

	r, _ := newAdmitRunnerForTest(t, s, f)
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(runCtx)

	waitForState(t, s, id, store.RequestStateDenied, 500*time.Millisecond)
	got, _ := s.GetRequest(context.Background(), id)
	if got.DenialReason == "" {
		t.Error("denial_reason not populated")
	}
}

func TestRunner_NilStore_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on nil store")
		}
	}()
	_ = New(Options{Store: nil, Publisher: nil, Fetcher: nil})
}

// flakyFetcher returns ErrTransient for its first `transientFailures`
// calls, then succeeds by writing payload to sink. Used to drive the
// runner's retry-with-backoff path.
type flakyFetcher struct {
	transientFailures int
	payload           []byte
	calls             atomic.Int64
}

func (f *flakyFetcher) Fetch(_ context.Context, _ string, sink io.Writer) (int64, error) {
	n := f.calls.Add(1)
	if int(n) <= f.transientFailures {
		return 0, fmt.Errorf("%w: simulated upstream 503", ErrTransient)
	}
	written, err := sink.Write(f.payload)
	return int64(written), err
}

// newAdmitRunnerForTestWithRetry mirrors newAdmitRunnerForTest but
// lets the test cap retries + inject a zero backoff. Used by the η
// retry-on-transient-failure tests.
func newAdmitRunnerForTestWithRetry(t *testing.T, s *store.Store, f Fetcher, maxRetries int) (*Runner, string) {
	t.Helper()
	root := t.TempDir()
	pub, err := publish.NewFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	return New(Options{
		Store:        s,
		Publisher:    pub,
		Fetcher:      f,
		BotIdent:     publish.Identity{Name: "bzlhub-bot", Email: "bot@example.com"},
		Workers:      1,
		PollEvery:    5 * time.Millisecond,
		Log:          slog.Default(),
		MaxRetries:   maxRetries,
		RetryBackoff: func(int) time.Duration { return 0 },
	}), root
}

func TestRunner_TransientFailure_RetriesThenSucceeds(t *testing.T) {
	s := newAdmitTestStore(t)
	tarball := makeTarGz(t, map[string]string{
		"rules_retry-1.0/MODULE.bazel": "module(name=\"rules_retry\")",
	})
	f := &flakyFetcher{transientFailures: 2, payload: tarball}
	id := driveToApproved(t, s, "rules_retry", "1.0", "https://example.com/rr.tar.gz")

	r, _ := newAdmitRunnerForTestWithRetry(t, s, f, 3)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)

	waitForState(t, s, id, store.RequestStateIndexed, 2*time.Second)

	got, _ := s.GetRequest(context.Background(), id)
	if got.RetryCount != 2 {
		t.Errorf("retry_count=%d after 2 transient failures then success, want 2", got.RetryCount)
	}
	if calls := f.calls.Load(); calls != 3 {
		t.Errorf("fetcher calls=%d, want 3 (initial + 2 retries)", calls)
	}

	events, _ := s.ListAudit(context.Background(), store.AuditQuery{Kinds: []string{"admit_success"}})
	if len(events) != 1 {
		t.Errorf("admit_success audit rows=%d, want 1", len(events))
	}
}

func TestRunner_TransientFailure_ExhaustsRetriesThenDenies(t *testing.T) {
	s := newAdmitTestStore(t)
	// transientFailures bigger than any plausible retry budget.
	f := &flakyFetcher{transientFailures: 99, payload: nil}
	id := driveToApproved(t, s, "rules_dead", "1.0", "https://example.com/d.tar.gz")

	r, _ := newAdmitRunnerForTestWithRetry(t, s, f, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)

	waitForState(t, s, id, store.RequestStateDenied, 2*time.Second)

	got, _ := s.GetRequest(context.Background(), id)
	if got.RetryCount != 2 {
		t.Errorf("retry_count=%d after exhausting 2 retries, want 2", got.RetryCount)
	}
	if got.DenialReason == "" {
		t.Error("denial_reason empty after transient exhaustion")
	}
	if calls := f.calls.Load(); calls != 3 {
		t.Errorf("fetcher calls=%d, want 3 (initial + 2 retries)", calls)
	}
}

func TestRunner_IsTransient_RecognisesSentinel(t *testing.T) {
	wrapped := fmt.Errorf("publish: %w", fmt.Errorf("network blip: %w", ErrTransient))
	if !IsTransient(wrapped) {
		t.Error("IsTransient should recognise sentinel through fmt.Errorf %w wraps")
	}
	terminal := errors.New("integrity mismatch")
	if IsTransient(terminal) {
		t.Error("IsTransient should not recognise a non-wrapped error")
	}
}

// ---------- CDN purger wiring (Plan 24 slice θ3) ----------

// recordingPurger captures every Purge call for assertions.
type recordingPurger struct {
	calls atomic.Int64
	mu    sync.Mutex
	urls  [][]string
	err   error
}

func (p *recordingPurger) Purge(_ context.Context, urls []string) error {
	p.calls.Add(1)
	p.mu.Lock()
	p.urls = append(p.urls, append([]string(nil), urls...))
	p.mu.Unlock()
	return p.err
}

func (p *recordingPurger) Name() string { return "recording" }

func (p *recordingPurger) snapshot() [][]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([][]string, len(p.urls))
	for i, u := range p.urls {
		out[i] = append([]string(nil), u...)
	}
	return out
}

func TestRunner_PurgerCalledOnIndex(t *testing.T) {
	s := newAdmitTestStore(t)
	tarball := makeTarGz(t, map[string]string{
		"rules_cdn-1.0/MODULE.bazel": "module(name=\"rules_cdn\")",
	})
	f := &stubFetcher{payload: tarball}
	id := driveToApproved(t, s, "rules_cdn", "1.0", "https://example.com/rc.tar.gz")

	p := &recordingPurger{}
	root := t.TempDir()
	pub, _ := publish.NewFilesystem(root)
	r := New(Options{
		Store:      s,
		Publisher:  pub,
		Fetcher:    f,
		BotIdent:   publish.Identity{Name: "bzlhub-bot", Email: "bot@example.com"},
		Workers:    1,
		PollEvery:  5 * time.Millisecond,
		Log:        slog.Default(),
		Purger:     p,
		CDNBaseURL: "https://bcr.bzlhub.com",
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)

	waitForState(t, s, id, store.RequestStateIndexed, 2*time.Second)
	// Give the purger a tick to be invoked after the transition.
	deadline := time.Now().Add(500 * time.Millisecond)
	for p.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if p.calls.Load() != 1 {
		t.Errorf("purger calls=%d, want 1", p.calls.Load())
	}
	urls := p.snapshot()
	if len(urls) != 1 || len(urls[0]) == 0 {
		t.Fatalf("unexpected URL payload: %v", urls)
	}
	// Expect the metadata.json URL among the purged set.
	found := false
	for _, u := range urls[0] {
		if u == "https://bcr.bzlhub.com/modules/rules_cdn/metadata.json" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("metadata.json not in purge set: %v", urls[0])
	}

	// Audit row recorded as cdn_purge.
	events, _ := s.ListAudit(context.Background(), store.AuditQuery{Kinds: []string{"cdn_purge"}})
	if len(events) != 1 {
		t.Errorf("cdn_purge audit rows=%d, want 1", len(events))
	}
}

func TestRunner_NoOpPurger_NotCalled(t *testing.T) {
	s := newAdmitTestStore(t)
	tarball := makeTarGz(t, map[string]string{
		"rules_noop-1.0/MODULE.bazel": "module(name=\"rules_noop\")",
	})
	f := &stubFetcher{payload: tarball}
	id := driveToApproved(t, s, "rules_noop", "1.0", "https://example.com/rn.tar.gz")

	// Explicit NoOp; CDNBaseURL irrelevant.
	r, _ := newAdmitRunnerForTest(t, s, f) // omits Purger → NoOp default
	_ = r
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)
	waitForState(t, s, id, store.RequestStateIndexed, 2*time.Second)
	// No cdn_purge audit row.
	events, _ := s.ListAudit(context.Background(), store.AuditQuery{Kinds: []string{"cdn_purge"}})
	if len(events) != 0 {
		t.Errorf("cdn_purge audit rows=%d, want 0 (NoOp purger should not audit)", len(events))
	}
}

func TestRunner_PurgerError_DoesNotFailAdmit(t *testing.T) {
	s := newAdmitTestStore(t)
	tarball := makeTarGz(t, map[string]string{
		"rules_pfail-1.0/MODULE.bazel": "module(name=\"rules_pfail\")",
	})
	f := &stubFetcher{payload: tarball}
	id := driveToApproved(t, s, "rules_pfail", "1.0", "https://example.com/rp.tar.gz")

	p := &recordingPurger{err: errors.New("cdn 503")}
	pub, _ := publish.NewFilesystem(t.TempDir())
	r := New(Options{
		Store:      s,
		Publisher:  pub,
		Fetcher:    f,
		BotIdent:   publish.Identity{Name: "bot", Email: "bot@example.com"},
		Workers:    1,
		PollEvery:  5 * time.Millisecond,
		Log:        slog.Default(),
		Purger:     p,
		CDNBaseURL: "https://bcr.bzlhub.com",
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)
	// Admit should still index — purge failures are non-fatal.
	waitForState(t, s, id, store.RequestStateIndexed, 2*time.Second)

	events, _ := s.ListAudit(context.Background(), store.AuditQuery{Kinds: []string{"cdn_purge"}})
	deadline := time.Now().Add(500 * time.Millisecond)
	for len(events) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		events, _ = s.ListAudit(context.Background(), store.AuditQuery{Kinds: []string{"cdn_purge"}})
	}
	if len(events) != 1 {
		t.Errorf("cdn_purge audit rows=%d, want 1 (failed purge still audited)", len(events))
	}
	if len(events) > 0 && events[0].OK {
		t.Error("failed-purge audit row OK=true; want false")
	}
}

// compile-time guard that the recording purger satisfies purge.Provider.
var _ purge.Provider = (*recordingPurger)(nil)

// TestRunner_BootSweepsStuckFetching exercises the crash-mid-retry
// recovery path end-to-end at the Runner level: a row in `fetching`
// past the staleness threshold should be reclaimed by Run() before
// any worker would otherwise leave it stranded.
func TestRunner_BootSweepsStuckFetching(t *testing.T) {
	s := newAdmitTestStore(t)
	ctx := context.Background()

	// Set up a request stuck in `fetching` as if a prior process
	// died mid-attempt.
	id := driveToApproved(t, s, "rules_python", "1.0.0",
		"https://example.com/rules_python-1.0.0.tar.gz")
	if err := s.TransitionRequest(ctx, id,
		store.RequestStateApproved, store.RequestStateFetching, nil); err != nil {
		t.Fatal(err)
	}

	// Build a runner with a tiny sweep threshold so the row qualifies
	// as "stuck" immediately.
	f := &stubFetcher{payload: []byte("never reached")}
	root := t.TempDir()
	pub, err := publish.NewFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	r := New(Options{
		Store:          s,
		Publisher:      pub,
		Fetcher:        f,
		BotIdent:       publish.Identity{Name: "bot", Email: "bot@example.com"},
		PollEvery:      1 * time.Hour, // workers never tick in this test
		SweepStaleness: 1 * time.Nanosecond,
		Log:            slog.New(slog.DiscardHandler),
	})

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { r.Run(runCtx); close(done) }()
	// Give the boot sweep a beat to fire; workers don't tick because
	// PollEvery is an hour.
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	got, err := s.GetRequest(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.RequestStateApproved {
		t.Errorf("state=%s after boot sweep, want approved", got.State)
	}
}

func TestRunner_GracefulShutdown(t *testing.T) {
	s := newAdmitTestStore(t)
	f := &stubFetcher{payload: []byte("noop")}
	r, _ := newAdmitRunnerForTest(t, s, f)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
		// ok
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Runner did not return after ctx cancel")
	}
}

