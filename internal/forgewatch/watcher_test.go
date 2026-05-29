package forgewatch

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/albertocavalcante/bigorna"
	"github.com/albertocavalcante/bigorna/bigornatest"
)

// fakeForge satisfies bigorna.Forge with scripted ListNewCommits
// responses. Each call dequeues the next scripted response, exposing
// the actual (sinceSHA, etag) the watcher passed so tests can assert
// the watcher correctly threaded state.
type fakeForge struct {
	mu      sync.Mutex
	scripts []forgeScript
	calls   []forgeCall
}

type forgeScript struct {
	commits     []bigorna.Commit
	etag        string
	notModified bool
	err         error
}

type forgeCall struct {
	sinceSHA string
	etag     string
}

func (f *fakeForge) ListNewCommits(
	_ context.Context, _ bigorna.Repo, _, sinceSHA, etag string,
) ([]bigorna.Commit, string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, forgeCall{sinceSHA: sinceSHA, etag: etag})
	if len(f.scripts) == 0 {
		// Default behavior after script is exhausted: nothing new.
		// Prevents the watcher from spinning into errors after we've
		// asserted everything we care about.
		return nil, etag, true, nil
	}
	s := f.scripts[0]
	f.scripts = f.scripts[1:]
	return s.commits, s.etag, s.notModified, s.err
}

// Stub the rest of bigorna.Forge — unused by Watcher tests.
func (f *fakeForge) OpenPR(context.Context, bigorna.OpenPROpts) (bigorna.PR, error) {
	return bigorna.PR{}, errors.New("not implemented")
}
func (f *fakeForge) GetPR(context.Context, bigorna.Repo, int) (bigorna.PR, error) {
	return bigorna.PR{}, errors.New("not implemented")
}
func (f *fakeForge) ListOpenPRs(context.Context, bigorna.Repo, string) ([]bigorna.PR, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeForge) Comment(context.Context, bigorna.Repo, int, string) error {
	return errors.New("not implemented")
}
func (f *fakeForge) Health(context.Context) error {
	return errors.New("not implemented")
}

// quietLogger discards log output so tests don't pollute the run log.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// runWatcherFor starts the watcher's Run loop in a goroutine, drives
// the clock through `cycles` poll cycles, then cancels and returns.
// Each cycle = one ManualClock.Advance(interval) plus a brief real-
// time yield so the watcher's goroutine can progress.
func runWatcherFor(t *testing.T, w *Watcher, clk *bigornatest.ManualClock, cycles int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	// First poll happens immediately (no initial sleep). Give the
	// goroutine real-time to reach its first Sleep call.
	time.Sleep(20 * time.Millisecond)

	for range cycles {
		// Advance enough to cover any adaptive backoff up to MaxInterval.
		clk.Advance(w.cfg.MaxInterval + time.Second)
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Run did not return after ctx cancel")
	}
}

// ----- constructor validation -----

func TestNew_ValidatesRequiredFields(t *testing.T) {
	base := Config{
		Forge:    &fakeForge{},
		Repo:     bigorna.Repo{Owner: "o", Name: "r"},
		Store:    NewMemoryStore(),
		OnCommit: func(context.Context, []bigorna.Commit) error { return nil },
	}
	cases := []struct {
		name string
		mut  func(*Config)
	}{
		{"missing Forge", func(c *Config) { c.Forge = nil }},
		{"missing Repo", func(c *Config) { c.Repo = bigorna.Repo{} }},
		{"missing Store", func(c *Config) { c.Store = nil }},
		{"missing OnCommit", func(c *Config) { c.OnCommit = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.mut(&cfg)
			if _, err := New(cfg); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestNew_AppliesDefaults(t *testing.T) {
	w, err := New(Config{
		Forge:    &fakeForge{},
		Repo:     bigorna.Repo{Owner: "o", Name: "r"},
		Store:    NewMemoryStore(),
		OnCommit: func(context.Context, []bigorna.Commit) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if w.cfg.Branch != "main" {
		t.Errorf("Branch default: %q", w.cfg.Branch)
	}
	if w.cfg.Interval != 60*time.Second {
		t.Errorf("Interval default: %v", w.cfg.Interval)
	}
	if w.cfg.MaxInterval != 5*60*time.Second {
		t.Errorf("MaxInterval default: %v", w.cfg.MaxInterval)
	}
	if w.cfg.Clock == nil || w.cfg.Logger == nil {
		t.Errorf("Clock/Logger not defaulted")
	}
}

func TestNew_ClampsMaxIntervalBelowInterval(t *testing.T) {
	w, err := New(Config{
		Forge:       &fakeForge{},
		Repo:        bigorna.Repo{Owner: "o", Name: "r"},
		Store:       NewMemoryStore(),
		OnCommit:    func(context.Context, []bigorna.Commit) error { return nil },
		Interval:    30 * time.Second,
		MaxInterval: 5 * time.Second, // invalid
	})
	if err != nil {
		t.Fatal(err)
	}
	if w.cfg.MaxInterval != w.cfg.Interval {
		t.Errorf("MaxInterval should clamp to Interval, got %v", w.cfg.MaxInterval)
	}
}

// ----- single-cycle behavior -----

func TestPollOnce_ColdStart(t *testing.T) {
	ff := &fakeForge{
		scripts: []forgeScript{{
			commits: []bigorna.Commit{{SHA: "abc", Message: "first"}},
			etag:    `W/"v1"`,
		}},
	}
	var received [][]bigorna.Commit
	store := NewMemoryStore()
	w, _ := New(Config{
		Forge: ff, Repo: bigorna.Repo{Owner: "o", Name: "r"},
		Store: store,
		OnCommit: func(_ context.Context, cs []bigorna.Commit) error {
			received = append(received, cs)
			return nil
		},
		Logger: quietLogger(),
	})

	if err := w.pollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	if len(ff.calls) != 1 {
		t.Fatalf("calls: %d", len(ff.calls))
	}
	if ff.calls[0].sinceSHA != "" || ff.calls[0].etag != "" {
		t.Errorf("first call should be cold-start: %+v", ff.calls[0])
	}
	if len(received) != 1 || received[0][0].SHA != "abc" {
		t.Fatalf("OnCommit received: %+v", received)
	}
	st, _ := store.Load(context.Background(), w.cfg.Repo, "main")
	if st.LastSHA != "abc" || st.LastETag != `W/"v1"` {
		t.Errorf("state after cold-start: %+v", st)
	}
}

func TestPollOnce_NotModified_StateETagUpdatedSHAUntouched(t *testing.T) {
	store := NewMemoryStore()
	_ = store.Save(context.Background(),
		bigorna.Repo{Owner: "o", Name: "r"}, "main",
		State{LastSHA: "abc", LastETag: `W/"v1"`})

	ff := &fakeForge{
		scripts: []forgeScript{{etag: `W/"v2"`, notModified: true}},
	}
	calls := 0
	w, _ := New(Config{
		Forge: ff, Repo: bigorna.Repo{Owner: "o", Name: "r"},
		Store: store,
		OnCommit: func(context.Context, []bigorna.Commit) error {
			calls++
			return nil
		},
		Logger: quietLogger(),
	})

	if err := w.pollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Errorf("OnCommit should not fire on notModified, got %d calls", calls)
	}
	st, _ := store.Load(context.Background(), w.cfg.Repo, "main")
	if st.LastSHA != "abc" {
		t.Errorf("LastSHA should be untouched on notModified, got %q", st.LastSHA)
	}
	if st.LastETag != `W/"v2"` {
		t.Errorf("LastETag should track latest, got %q", st.LastETag)
	}
}

func TestPollOnce_OnCommitErrorPinsState(t *testing.T) {
	ff := &fakeForge{
		scripts: []forgeScript{{
			commits: []bigorna.Commit{{SHA: "new"}},
			etag:    `W/"e"`,
		}},
	}
	store := NewMemoryStore()
	calls := atomic.Int32{}
	w, _ := New(Config{
		Forge: ff, Repo: bigorna.Repo{Owner: "o", Name: "r"},
		Store: store,
		OnCommit: func(context.Context, []bigorna.Commit) error {
			calls.Add(1)
			return errors.New("simulated ingest failure")
		},
		Logger: quietLogger(),
	})

	// First poll: commit arrives, callback fails, state should NOT
	// advance past the original (empty) LastSHA.
	if err := w.pollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected 1 OnCommit call, got %d", calls.Load())
	}
	st, _ := store.Load(context.Background(), w.cfg.Repo, "main")
	if st.LastSHA != "" {
		t.Errorf("LastSHA must NOT advance when callback fails, got %q", st.LastSHA)
	}
	if st.LastPolledAt.IsZero() {
		t.Errorf("LastPolledAt should still be touched so observers see the loop is alive")
	}
}

func TestPollOnce_OnCommitSuccessAdvancesState(t *testing.T) {
	ff := &fakeForge{
		scripts: []forgeScript{{
			// Forge returns newest-first; commits[0] should become LastSHA.
			commits: []bigorna.Commit{
				{SHA: "newest"},
				{SHA: "middle"},
				{SHA: "oldest"},
			},
			etag: `W/"e"`,
		}},
	}
	store := NewMemoryStore()
	w, _ := New(Config{
		Forge: ff, Repo: bigorna.Repo{Owner: "o", Name: "r"},
		Store: store,
		OnCommit: func(context.Context, []bigorna.Commit) error { return nil },
		Logger:   quietLogger(),
	})

	if err := w.pollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, _ := store.Load(context.Background(), w.cfg.Repo, "main")
	if st.LastSHA != "newest" {
		t.Errorf("LastSHA should be commits[0] (newest), got %q", st.LastSHA)
	}
}

func TestPollOnce_ThreadsSinceSHAAndETag(t *testing.T) {
	store := NewMemoryStore()
	_ = store.Save(context.Background(),
		bigorna.Repo{Owner: "o", Name: "r"}, "main",
		State{LastSHA: "from-state", LastETag: "etag-from-state"})

	ff := &fakeForge{
		scripts: []forgeScript{{notModified: true, etag: "etag-from-state"}},
	}
	w, _ := New(Config{
		Forge: ff, Repo: bigorna.Repo{Owner: "o", Name: "r"},
		Store: store,
		OnCommit: func(context.Context, []bigorna.Commit) error { return nil },
		Logger:   quietLogger(),
	})

	_ = w.pollOnce(context.Background())
	if len(ff.calls) != 1 {
		t.Fatalf("calls: %d", len(ff.calls))
	}
	c := ff.calls[0]
	if c.sinceSHA != "from-state" || c.etag != "etag-from-state" {
		t.Errorf("watcher did not thread state: %+v", c)
	}
}

// ----- adaptive backoff -----

func TestApplyBackoff_GrowsAndCapsAtMax(t *testing.T) {
	w := &Watcher{
		cfg: Config{Interval: time.Second, MaxInterval: 8 * time.Second},
		interval: time.Second,
	}
	want := []time.Duration{
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		8 * time.Second, // capped
		8 * time.Second, // still capped
	}
	for i, w_want := range want {
		w.applyBackoff()
		if w.interval != w_want {
			t.Errorf("step %d: got %v, want %v", i, w.interval, w_want)
		}
	}
}

func TestPollOnce_NewCommitsResetInterval(t *testing.T) {
	ff := &fakeForge{
		scripts: []forgeScript{{
			commits: []bigorna.Commit{{SHA: "x"}},
			etag:    "e",
		}},
	}
	w, _ := New(Config{
		Forge: ff, Repo: bigorna.Repo{Owner: "o", Name: "r"},
		Store: NewMemoryStore(),
		OnCommit: func(context.Context, []bigorna.Commit) error { return nil },
		Interval: 10 * time.Second, MaxInterval: 60 * time.Second,
		Logger: quietLogger(),
	})
	// Inflate the interval to simulate having backed off.
	w.interval = 60 * time.Second

	_ = w.pollOnce(context.Background())
	if w.interval != 10*time.Second {
		t.Errorf("interval should reset to base on activity, got %v", w.interval)
	}
}

// ----- Run loop integration -----

func TestRun_PollsImmediatelyThenSleeps(t *testing.T) {
	ff := &fakeForge{
		scripts: []forgeScript{
			{commits: []bigorna.Commit{{SHA: "first"}}, etag: "e1"},
			{notModified: true, etag: "e2"},
			{notModified: true, etag: "e3"},
		},
	}
	clk := bigornatest.NewManualClock(time.Unix(0, 0))
	w, _ := New(Config{
		Forge: ff, Repo: bigorna.Repo{Owner: "o", Name: "r"},
		Store: NewMemoryStore(),
		OnCommit: func(context.Context, []bigorna.Commit) error { return nil },
		Clock:    clk,
		Interval: time.Second, MaxInterval: 5 * time.Second,
		Logger: quietLogger(),
	})

	runWatcherFor(t, w, clk, 3)

	// Expect at least 3 poll cycles. Allow some flex on the upper bound
	// in case the goroutine got an extra cycle during cancel.
	if len(ff.calls) < 3 {
		t.Errorf("expected >= 3 polls, got %d", len(ff.calls))
	}
}

func TestRun_StopsOnContextCancel(t *testing.T) {
	ff := &fakeForge{}
	w, _ := New(Config{
		Forge: ff, Repo: bigorna.Repo{Owner: "o", Name: "r"},
		Store: NewMemoryStore(),
		OnCommit: func(context.Context, []bigorna.Commit) error { return nil },
		Clock: bigornatest.NewManualClock(time.Unix(0, 0)),
		Logger: quietLogger(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Run should return context.Canceled, got %v", err)
	}
}

// ----- Store impls -----

func TestMemoryStore_RoundTrip(t *testing.T) {
	s := NewMemoryStore()
	repo := bigorna.Repo{Owner: "o", Name: "r"}
	in := State{LastSHA: "abc", LastETag: "e", LastPolledAt: time.Unix(123, 0)}

	if err := s.Save(context.Background(), repo, "main", in); err != nil {
		t.Fatal(err)
	}
	out, err := s.Load(context.Background(), repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Errorf("round-trip mismatch:\n  in:  %+v\n  out: %+v", in, out)
	}
}

func TestMemoryStore_MissingReturnsZero(t *testing.T) {
	s := NewMemoryStore()
	out, err := s.Load(context.Background(), bigorna.Repo{Owner: "o", Name: "r"}, "main")
	if err != nil {
		t.Fatal(err)
	}
	if out != (State{}) {
		t.Errorf("missing entry should be zero, got %+v", out)
	}
}

func TestFileStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewFileStore(filepath.Join(dir, "state.json"))

	repo := bigorna.Repo{Owner: "o", Name: "r"}
	in := State{LastSHA: "abc", LastETag: "e", LastPolledAt: time.Unix(123, 0).UTC()}

	if err := s.Save(context.Background(), repo, "main", in); err != nil {
		t.Fatal(err)
	}
	out, err := s.Load(context.Background(), repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !out.LastPolledAt.Equal(in.LastPolledAt) || out.LastSHA != in.LastSHA || out.LastETag != in.LastETag {
		t.Errorf("round-trip mismatch:\n  in:  %+v\n  out: %+v", in, out)
	}

	// File should exist on disk.
	if _, err := os.Stat(filepath.Join(dir, "state.json")); err != nil {
		t.Errorf("state file missing: %v", err)
	}
}

func TestFileStore_MissingFileReturnsZero(t *testing.T) {
	s := NewFileStore(filepath.Join(t.TempDir(), "nonexistent.json"))
	out, err := s.Load(context.Background(), bigorna.Repo{Owner: "o", Name: "r"}, "main")
	if err != nil {
		t.Fatalf("missing file should not error, got %v", err)
	}
	if out != (State{}) {
		t.Errorf("missing file should yield zero State, got %+v", out)
	}
}

func TestFileStore_SiblingKeysPreserved(t *testing.T) {
	// Saving (repo-a, main) must not lose (repo-b, main) state in the
	// same file.
	dir := t.TempDir()
	s := NewFileStore(filepath.Join(dir, "state.json"))
	repoA := bigorna.Repo{Owner: "oa", Name: "ra"}
	repoB := bigorna.Repo{Owner: "ob", Name: "rb"}

	_ = s.Save(context.Background(), repoA, "main", State{LastSHA: "shaA"})
	_ = s.Save(context.Background(), repoB, "main", State{LastSHA: "shaB"})

	outA, _ := s.Load(context.Background(), repoA, "main")
	outB, _ := s.Load(context.Background(), repoB, "main")
	if outA.LastSHA != "shaA" || outB.LastSHA != "shaB" {
		t.Errorf("sibling keys lost: A=%+v B=%+v", outA, outB)
	}
}

// TestJitteredInterval_DistributionWithinBounds — when Jitter is
// positive, every sample must land in [interval*(1-J), interval*(1+J)]
// and the population must show variance (otherwise the jitter is
// a no-op masquerading as defense).
func TestJitteredInterval_DistributionWithinBounds(t *testing.T) {
	w := &Watcher{
		cfg:      Config{Interval: 60 * time.Second, Jitter: 0.1},
		interval: 60 * time.Second,
	}
	const samples = 200
	var minSeen, maxSeen time.Duration
	seen := map[time.Duration]int{}
	for i := 0; i < samples; i++ {
		d := w.jitteredInterval()
		if i == 0 || d < minSeen {
			minSeen = d
		}
		if d > maxSeen {
			maxSeen = d
		}
		seen[d]++
	}
	low := time.Duration(float64(w.interval) * 0.9)
	high := time.Duration(float64(w.interval) * 1.1)
	if minSeen < low || maxSeen > high {
		t.Errorf("samples escaped bounds: min=%v max=%v [%v..%v]", minSeen, maxSeen, low, high)
	}
	// Variance check — with 200 samples and ±10% jitter (a continuous
	// distribution), we'd expect >10 distinct values. If we see fewer,
	// the jitter source isn't doing anything random.
	if len(seen) < 10 {
		t.Errorf("jitter looks degenerate: only %d distinct durations across %d samples", len(seen), samples)
	}
}

// TestJitteredInterval_ZeroDisabled — Jitter=0 (the production-test
// default) MUST be a no-op pass-through so ManualClock tests stay
// deterministic.
func TestJitteredInterval_ZeroDisabled(t *testing.T) {
	w := &Watcher{
		cfg:      Config{Interval: 60 * time.Second, Jitter: 0},
		interval: 60 * time.Second,
	}
	for i := 0; i < 50; i++ {
		if d := w.jitteredInterval(); d != w.interval {
			t.Fatalf("zero-jitter must pass through; got %v want %v", d, w.interval)
		}
	}
}

// TestJitteredInterval_GuardsZeroDurationUnderlay — if a pathological
// computed duration came out non-positive (e.g., a future negative
// Jitter setting), the guard must fall back to the base interval
// instead of returning a sleeper that never sleeps.
func TestJitteredInterval_GuardsZeroDurationUnderlay(t *testing.T) {
	// Force a non-positive jittered value by using interval=1 +
	// Jitter > 1.0 — many samples will roll negative deltas larger
	// than the interval.
	w := &Watcher{
		cfg:      Config{Interval: 1, Jitter: 2.0},
		interval: 1,
	}
	for i := 0; i < 100; i++ {
		if d := w.jitteredInterval(); d <= 0 {
			t.Fatalf("guard breached on iteration %d: got %v", i, d)
		}
	}
}
