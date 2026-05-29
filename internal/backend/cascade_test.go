package backend

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubBackend is a Backend that returns canned responses by exact path.
// Anything not in `files` returns ErrNotFound; anything in `errors`
// returns the given error (used to exercise the "primary failed for a
// reason that isn't NotFound" path).
type stubBackend struct {
	files  map[string][]byte
	errors map[string]error
}

func newStub() *stubBackend {
	return &stubBackend{files: map[string][]byte{}, errors: map[string]error{}}
}

func (s *stubBackend) get(key string) (io.ReadCloser, error) {
	if err, ok := s.errors[key]; ok {
		return nil, err
	}
	if b, ok := s.files[key]; ok {
		return io.NopCloser(bytes.NewReader(b)), nil
	}
	return nil, ErrNotFound
}

func (s *stubBackend) GetBazelRegistryJSON(context.Context) (io.ReadCloser, error) {
	return s.get("bazel_registry.json")
}
func (s *stubBackend) GetMetadata(_ context.Context, m string) (io.ReadCloser, error) {
	return s.get("modules/" + m + "/metadata.json")
}
func (s *stubBackend) GetModuleBazel(_ context.Context, m, v string) (io.ReadCloser, error) {
	return s.get("modules/" + m + "/" + v + "/MODULE.bazel")
}
func (s *stubBackend) GetSourceJSON(_ context.Context, m, v string) (io.ReadCloser, error) {
	return s.get("modules/" + m + "/" + v + "/source.json")
}
func (s *stubBackend) GetPatch(_ context.Context, m, v, f string) (io.ReadCloser, error) {
	return s.get("modules/" + m + "/" + v + "/patches/" + f)
}
func (s *stubBackend) GetOverlay(_ context.Context, m, v, p string) (io.ReadCloser, error) {
	return s.get("modules/" + m + "/" + v + "/overlay/" + p)
}
func (s *stubBackend) GetBlob(_ context.Context, k string) (io.ReadCloser, error) {
	return s.get("blobs/" + k)
}

// ---- helpers ----

// upstreamServer spins up an httptest server with a handler that
// responds with the given (statusCode, body) per path. Unknown paths
// return 404. delay applies to every response.
func upstreamServer(t *testing.T, responses map[string]upstreamResp, delay time.Duration) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		key := strings.TrimPrefix(r.URL.Path, "/")
		resp, ok := responses[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(resp.status)
		if resp.body != "" {
			_, _ = w.Write([]byte(resp.body))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

type upstreamResp struct {
	status int
	body   string
}

func readAllAndClose(t *testing.T, rc io.ReadCloser) string {
	t.Helper()
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

// ---- TestCascade_PrimaryHit ----

func TestCascade_PrimaryHit(t *testing.T) {
	primary := newStub()
	primary.files["modules/foo/1.0.0/source.json"] = []byte(`{"primary":"yes"}`)

	srv := upstreamServer(t, map[string]upstreamResp{
		"modules/foo/1.0.0/source.json": {status: 200, body: `{"upstream":"never-asked"}`},
	}, 0)

	c, err := NewCascade(CascadeConfig{
		Primary:   primary,
		Upstreams: []*Upstream{{URL: srv.URL}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rc, err := c.GetSourceJSON(context.Background(), "foo", "1.0.0")
	if err != nil {
		t.Fatalf("GetSourceJSON: %v", err)
	}
	got := readAllAndClose(t, rc)
	if got != `{"primary":"yes"}` {
		t.Errorf("expected primary, got %q", got)
	}
}

// ---- TestCascade_FallthroughToUpstream ----

func TestCascade_FallthroughToUpstream(t *testing.T) {
	primary := newStub() // empty; everything 404

	srv := upstreamServer(t, map[string]upstreamResp{
		"modules/foo/1.0.0/source.json": {status: 200, body: `{"upstream":"yes"}`},
	}, 0)

	c, _ := NewCascade(CascadeConfig{
		Primary:   primary,
		Upstreams: []*Upstream{{URL: srv.URL}},
	})
	rc, err := c.GetSourceJSON(context.Background(), "foo", "1.0.0")
	if err != nil {
		t.Fatalf("GetSourceJSON: %v", err)
	}
	if got := readAllAndClose(t, rc); got != `{"upstream":"yes"}` {
		t.Errorf("expected upstream body, got %q", got)
	}
}

// ---- TestCascade_AllUpstreamsNotFound ----

func TestCascade_AllUpstreamsNotFound(t *testing.T) {
	primary := newStub()
	a := upstreamServer(t, map[string]upstreamResp{}, 0)
	b := upstreamServer(t, map[string]upstreamResp{}, 0)

	c, _ := NewCascade(CascadeConfig{
		Primary:   primary,
		Upstreams: []*Upstream{{URL: a.URL}, {URL: b.URL}},
	})
	_, err := c.GetSourceJSON(context.Background(), "missing", "1.0.0")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

// ---- TestCascade_AllUpstreamsErrorReturns503 ----

func TestCascade_AllUpstreamsErrorReturns503(t *testing.T) {
	primary := newStub()
	// Both upstreams reply 502 (transient error, not authoritative).
	a := upstreamServer(t, map[string]upstreamResp{
		"modules/foo/1.0.0/source.json": {status: 502, body: "bad gateway"},
	}, 0)
	b := upstreamServer(t, map[string]upstreamResp{
		"modules/foo/1.0.0/source.json": {status: 503, body: "service unavailable"},
	}, 0)

	c, _ := NewCascade(CascadeConfig{
		Primary:   primary,
		Upstreams: []*Upstream{{URL: a.URL}, {URL: b.URL}},
	})
	_, err := c.GetSourceJSON(context.Background(), "foo", "1.0.0")
	if !errors.Is(err, ErrUpstreamUnavailable) {
		t.Errorf("want ErrUpstreamUnavailable, got %v", err)
	}
}

// ---- TestCascade_MixOf404AndErrorIsNotFound ----

func TestCascade_MixOf404AndErrorIsNotFound(t *testing.T) {
	primary := newStub()
	// Upstream A authoritatively says no; B has a transient error.
	// Mixed result must be 404 (the authoritative "no" wins).
	a := upstreamServer(t, map[string]upstreamResp{}, 0) // 404 on everything
	b := upstreamServer(t, map[string]upstreamResp{
		"modules/foo/1.0.0/source.json": {status: 502, body: "bad gateway"},
	}, 0)

	c, _ := NewCascade(CascadeConfig{
		Primary:   primary,
		Upstreams: []*Upstream{{URL: a.URL}, {URL: b.URL}},
	})
	_, err := c.GetSourceJSON(context.Background(), "foo", "1.0.0")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound (mixed 404+error → 404), got %v", err)
	}
}

// ---- TestCascade_FirstUpstreamWinsRaceCancellable ----

func TestCascade_FirstUpstreamWinsReturnsImmediately(t *testing.T) {
	primary := newStub() // empty
	// fast responds immediately; slow would hang past upstreamProbeTimeout.
	// The cascade must NOT block on the slow probe — the caller takes
	// the fast body and returns; the slow probe continues in a detached
	// goroutine bounded by its own per-call timeout so shadowed-event
	// collision detection can still observe it (Plan 16 Layer D).
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"from":"fast"}`))
	}))
	t.Cleanup(fast.Close)

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(10 * time.Second):
			_, _ = w.Write([]byte(`{"from":"slow"}`))
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(slow.Close)

	c, _ := NewCascade(CascadeConfig{
		Primary:   primary,
		Upstreams: []*Upstream{{URL: fast.URL}, {URL: slow.URL}},
	})
	start := time.Now()
	rc, err := c.GetSourceJSON(context.Background(), "foo", "1.0.0")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("GetSourceJSON: %v", err)
	}
	got := readAllAndClose(t, rc)
	if got != `{"from":"fast"}` {
		t.Errorf("expected fast winner, got %q", got)
	}
	// Must return immediately — caller waiting for slow would mean
	// the cascade is serially-blocking, which defeats parallel probe.
	// Allow up to 1s for scheduler jitter; slow handler hangs 10s.
	if elapsed > 1*time.Second {
		t.Errorf("cascade blocked on slow upstream: returned after %v", elapsed)
	}
}

// ---- TestCascade_PerCallTimeout ----

func TestCascade_PerCallTimeout(t *testing.T) {
	primary := newStub()
	// Upstream stalls past upstreamProbeTimeout (5s). Test would take
	// 5s; SKIP unless run in -timeout long mode.
	if testing.Short() {
		t.Skip("skipping 5s upstream timeout test in -short mode")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	c, _ := NewCascade(CascadeConfig{
		Primary:   primary,
		Upstreams: []*Upstream{{URL: srv.URL}},
	})
	start := time.Now()
	_, err := c.GetSourceJSON(context.Background(), "foo", "1.0.0")
	elapsed := time.Since(start)
	if !errors.Is(err, ErrUpstreamUnavailable) {
		t.Errorf("want ErrUpstreamUnavailable on timeout, got %v", err)
	}
	if elapsed < 4*time.Second || elapsed > 7*time.Second {
		t.Errorf("expected ~5s per-call timeout, got %v", elapsed)
	}
}

// ---- TestCascade_PrimaryNonNotFoundErrorSurfacesDirectly ----

func TestCascade_PrimaryNonNotFoundErrorSurfacesDirectly(t *testing.T) {
	primary := newStub()
	primary.errors["modules/foo/1.0.0/source.json"] = errors.New("disk i/o error")

	srv := upstreamServer(t, map[string]upstreamResp{
		"modules/foo/1.0.0/source.json": {status: 200, body: "shouldnt-reach"},
	}, 0)

	c, _ := NewCascade(CascadeConfig{
		Primary:   primary,
		Upstreams: []*Upstream{{URL: srv.URL}},
	})
	_, err := c.GetSourceJSON(context.Background(), "foo", "1.0.0")
	if err == nil || !strings.Contains(err.Error(), "disk i/o error") {
		t.Errorf("primary non-404 errors should surface, got %v", err)
	}
}

// ---- TestCascade_NoUpstreamsConfigured ----

func TestCascade_NoUpstreamsConfigured(t *testing.T) {
	primary := newStub()
	primary.files["bazel_registry.json"] = []byte(`{"primary":"only"}`)

	c, _ := NewCascade(CascadeConfig{Primary: primary})
	rc, err := c.GetBazelRegistryJSON(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := readAllAndClose(t, rc)
	if got != `{"primary":"only"}` {
		t.Errorf("got %q", got)
	}

	// Cascade with no upstreams must still propagate ErrNotFound from primary.
	_, err = c.GetSourceJSON(context.Background(), "missing", "1.0.0")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound from primary, got %v", err)
	}
}

// ---- TestCascade_GetBlobBypassesUpstream ----

func TestCascade_GetBlobBypassesUpstream(t *testing.T) {
	primary := newStub()
	primary.files["blobs/abc123"] = []byte("tarball-bytes")

	// Upstream would return a different body — but Cascade.GetBlob
	// must NOT hit it. Tarballs go to URLs in source.json, not /blobs/.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("upstream must not be called for /blobs/; got %s", r.URL.Path)
	}))
	t.Cleanup(srv.Close)

	c, _ := NewCascade(CascadeConfig{
		Primary:   primary,
		Upstreams: []*Upstream{{URL: srv.URL}},
	})
	rc, err := c.GetBlob(context.Background(), "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if got := readAllAndClose(t, rc); got != "tarball-bytes" {
		t.Errorf("got %q", got)
	}
}

// TestCascade_CascadeFetchDoesNotTouchProbeState pins the new
// semantics (2026-05-21 fix): cascade fetches DO NOT update the
// upstream's reachable / lastProbe / errMsg fields. Probe state is
// owned exclusively by ProbeUpstream (boot + background loop). This
// prevents pollution from caller-context-cancel ("context canceled"
// errors when the bazel client gives up, or drainShadowed killing
// runner-up probes) showing up as a probe failure in
// /api/v1/upstreams.
func TestCascade_CascadeFetchDoesNotTouchProbeState(t *testing.T) {
	primary := newStub()
	srv := upstreamServer(t, map[string]upstreamResp{
		"bazel_registry.json":           {status: 200, body: `{"mirrors":[]}`},
		"modules/foo/1.0.0/source.json": {status: 200, body: "{}"},
	}, 0)

	up := &Upstream{URL: srv.URL}
	c, _ := NewCascade(CascadeConfig{Primary: primary, Upstreams: []*Upstream{up}})

	// Initial state: never probed.
	reachable, lastProbe, _, _ := up.Reachable()
	if reachable {
		t.Error("upstream should NOT be reachable before any probe")
	}
	if !lastProbe.IsZero() {
		t.Errorf("lastProbe should be zero before any probe, got %v", lastProbe)
	}

	// Drive a cascade fetch. Probe state must stay at initial values.
	rc, err := c.GetSourceJSON(context.Background(), "foo", "1.0.0")
	if err != nil {
		t.Fatalf("GetSourceJSON: %v", err)
	}
	rc.Close()

	reachable, lastProbe, _, _ = up.Reachable()
	if reachable {
		t.Error("cascade fetch must not flip reachable from false to true; only ProbeUpstream does")
	}
	if !lastProbe.IsZero() {
		t.Errorf("cascade fetch must not set lastProbe, got %v", lastProbe)
	}

	// Now run an explicit probe — state should update.
	if err := c.ProbeUpstream(context.Background(), up); err != nil {
		t.Fatalf("ProbeUpstream: %v", err)
	}
	reachable, lastProbe, _, _ = up.Reachable()
	if !reachable {
		t.Error("ProbeUpstream should mark reachable=true on 200")
	}
	if lastProbe.IsZero() {
		t.Error("ProbeUpstream should populate lastProbe")
	}
}

// ---- TestProbeUpstream_NotBCRHardFail ----

func TestProbeUpstream_NotBCRHardFail(t *testing.T) {
	// Upstream returns 401 — that's "not a BCR-shape registry I can use"
	// → ProbeNotBCR (hard-fail at boot).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	c, _ := NewCascade(CascadeConfig{Primary: newStub()})
	up := &Upstream{URL: srv.URL}
	err := c.ProbeUpstream(context.Background(), up)
	if err == nil {
		t.Fatal("expected ProbeError")
	}
	if IsProbeTransient(err) {
		t.Error("401 should NOT be classified as transient")
	}
}

// ---- TestProbeUpstream_TransientSoftFail ----

func TestProbeUpstream_TransientSoftFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server error", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	c, _ := NewCascade(CascadeConfig{Primary: newStub()})
	up := &Upstream{URL: srv.URL}
	err := c.ProbeUpstream(context.Background(), up)
	if err == nil {
		t.Fatal("expected ProbeError")
	}
	if !IsProbeTransient(err) {
		t.Error("500 should be classified as transient (soft-fail at boot)")
	}
}

// ---- TestProbeUpstream_OKMarksReachable ----

func TestProbeUpstream_OKMarksReachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"mirrors":[]}`))
	}))
	t.Cleanup(srv.Close)

	c, _ := NewCascade(CascadeConfig{Primary: newStub()})
	up := &Upstream{URL: srv.URL}
	if err := c.ProbeUpstream(context.Background(), up); err != nil {
		t.Fatalf("ProbeUpstream: %v", err)
	}
	if r, _, _, _ := up.Reachable(); !r {
		t.Error("upstream should be marked reachable after 200 probe")
	}
}

// ----- Layer D: collision-logger hook firing -----

// collisionEvent captures one (m, v, source, kind) tuple seen by the
// hook. The cascade calls the hook in a detached goroutine, so the
// recorder is mutex-guarded.
type collisionEvent struct{ module, version, source, kind string }

type collisionRecorder struct {
	mu     sync.Mutex
	events []collisionEvent
}

func (r *collisionRecorder) hook(module, version, source, kind string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, collisionEvent{module, version, source, kind})
}

// waitForEvents polls the recorder until events reach the wanted
// count or the deadline expires. The cascade fires hooks async so a
// direct read after GetSourceJSON returns can race.
func (r *collisionRecorder) waitForEvents(t *testing.T, want int) []collisionEvent {
	t.Helper()
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		n := len(r.events)
		r.mu.Unlock()
		if n >= want {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]collisionEvent, len(r.events))
	copy(out, r.events)
	return out
}

// TestCascade_CollisionLogger_PrimaryHit: when the primary serves a
// modules/<m>/<v>/... path, the hook fires once with kind='local'
// and source='local'.
func TestCascade_CollisionLogger_PrimaryHit(t *testing.T) {
	primary := newStub()
	primary.files["modules/foo/1.0.0/source.json"] = []byte(`{}`)

	rec := &collisionRecorder{}
	c, _ := NewCascade(CascadeConfig{Primary: primary})
	c.SetCollisionLogger(rec.hook)

	rc, err := c.GetSourceJSON(context.Background(), "foo", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()

	events := rec.waitForEvents(t, 1)
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d: %+v", len(events), events)
	}
	ev := events[0]
	if ev.module != "foo" || ev.version != "1.0.0" || ev.kind != CollisionKindLocal || ev.source != "local" {
		t.Errorf("event = %+v, want {foo, 1.0.0, local, local}", ev)
	}
}

// TestCascade_CollisionLogger_PrimaryHitNonVersionedPathSkipsHook:
// the hook should NOT fire on bazel_registry.json or metadata.json —
// only paths with (module, version) get logged.
func TestCascade_CollisionLogger_PrimaryHitNonVersionedPathSkipsHook(t *testing.T) {
	primary := newStub()
	primary.files["bazel_registry.json"] = []byte(`{"mirrors":[]}`)
	primary.files["modules/foo/metadata.json"] = []byte(`{}`)

	rec := &collisionRecorder{}
	c, _ := NewCascade(CascadeConfig{Primary: primary})
	c.SetCollisionLogger(rec.hook)

	if rc, err := c.GetBazelRegistryJSON(context.Background()); err != nil {
		t.Fatal(err)
	} else {
		rc.Close()
	}
	if rc, err := c.GetMetadata(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	} else {
		rc.Close()
	}

	// Give async hooks a window in case they DO fire (we want zero).
	time.Sleep(50 * time.Millisecond)
	rec.mu.Lock()
	n := len(rec.events)
	rec.mu.Unlock()
	if n != 0 {
		t.Errorf("hook fired on non-versioned paths: %d events", n)
	}
}

// TestCascade_CollisionLogger_UpstreamWinFires: when local 404s and an
// upstream returns 200, the winner-side hook fires with kind=upstream.
// This is the deterministic half of the upstream-win + shadowed flow.
// The shadowed-side hook fire is best-effort by design (plan 16's
// "5-min coalesce + repeated probes over time eventually surface every
// real collision") — the cascade's cancel-after-winner can race-cancel
// the runner-up's in-flight response read, so a single-run shadowed
// observation isn't deterministic. The logCollision contract for all
// three kinds is tested directly in TestCascade_LogCollision_AllKinds
// below.
func TestCascade_CollisionLogger_UpstreamWinFires(t *testing.T) {
	primary := newStub() // empty → all 404
	srv := upstreamServer(t, map[string]upstreamResp{
		"modules/foo/1.0.0/source.json": {status: 200, body: `{}`},
	}, 0)

	rec := &collisionRecorder{}
	c, _ := NewCascade(CascadeConfig{
		Primary:   primary,
		Upstreams: []*Upstream{{URL: srv.URL}},
	})
	c.SetCollisionLogger(rec.hook)

	rc, err := c.GetSourceJSON(context.Background(), "foo", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()

	events := rec.waitForEvents(t, 1)
	if len(events) != 1 {
		t.Fatalf("want 1 event (winner), got %d: %+v", len(events), events)
	}
	ev := events[0]
	if ev.module != "foo" || ev.version != "1.0.0" || ev.kind != CollisionKindUpstream || ev.source != srv.URL {
		t.Errorf("event = %+v, want {foo, 1.0.0, %s, %s}", ev, CollisionKindUpstream, srv.URL)
	}
}

// TestCascade_LogCollision_AllKinds: direct contract test on the
// logCollision helper. Verifies the hook fires once per
// (m, v, source, kind) tuple within the coalesce window, with all
// args passed through correctly. Avoids the HTTP / race-cancel
// nondeterminism of testing through probeUpstreams.
func TestCascade_LogCollision_AllKinds(t *testing.T) {
	rec := &collisionRecorder{}
	c, _ := NewCascade(CascadeConfig{Primary: newStub()})
	c.SetCollisionLogger(rec.hook)

	c.logCollision("foo", "1.0.0", "local", CollisionKindLocal)
	c.logCollision("foo", "1.0.0", "https://up1.example/r", CollisionKindUpstream)
	c.logCollision("foo", "1.0.0", "https://up2.example/r", CollisionKindShadowed)

	events := rec.waitForEvents(t, 3)
	if len(events) != 3 {
		t.Fatalf("want 3 events (one per kind), got %d: %+v", len(events), events)
	}
	byKind := map[string]collisionEvent{}
	for _, ev := range events {
		byKind[ev.kind] = ev
	}
	for _, kind := range []string{CollisionKindLocal, CollisionKindUpstream, CollisionKindShadowed} {
		ev, ok := byKind[kind]
		if !ok {
			t.Errorf("missing event for kind=%q", kind)
			continue
		}
		if ev.module != "foo" || ev.version != "1.0.0" {
			t.Errorf("event for kind=%q has wrong module@version: %+v", kind, ev)
		}
	}
	if got := byKind[CollisionKindLocal].source; got != "local" {
		t.Errorf("local source = %q, want 'local'", got)
	}
	if got := byKind[CollisionKindUpstream].source; got != "https://up1.example/r" {
		t.Errorf("upstream source = %q", got)
	}
	if got := byKind[CollisionKindShadowed].source; got != "https://up2.example/r" {
		t.Errorf("shadowed source = %q", got)
	}
}

// TestCascade_CollisionLogger_CoalesceSuppressesDuplicates: the 5-min
// coalesce window means a second GetSourceJSON within that window
// for the same (m, v) should NOT re-fire the hook.
func TestCascade_CollisionLogger_CoalesceSuppressesDuplicates(t *testing.T) {
	primary := newStub()
	primary.files["modules/foo/1.0.0/source.json"] = []byte(`{}`)

	rec := &collisionRecorder{}
	c, _ := NewCascade(CascadeConfig{Primary: primary})
	c.SetCollisionLogger(rec.hook)

	for i := 0; i < 5; i++ {
		rc, err := c.GetSourceJSON(context.Background(), "foo", "1.0.0")
		if err != nil {
			t.Fatal(err)
		}
		rc.Close()
	}

	// 5 reads but only 1 event should land within the coalesce window.
	events := rec.waitForEvents(t, 1)
	time.Sleep(50 * time.Millisecond) // any spurious extras would arrive
	rec.mu.Lock()
	n := len(rec.events)
	rec.mu.Unlock()
	if n != 1 {
		t.Errorf("coalesce window broken: got %d events for 5 identical reads, want 1", n)
	}
	_ = events
}

// TestCascade_CollisionLogger_HookCleared: SetCollisionLogger(nil)
// must un-wire the hook — useful for tests / dynamic reconfig.
func TestCascade_CollisionLogger_HookCleared(t *testing.T) {
	primary := newStub()
	primary.files["modules/foo/1.0.0/source.json"] = []byte(`{}`)

	rec := &collisionRecorder{}
	c, _ := NewCascade(CascadeConfig{Primary: primary})
	c.SetCollisionLogger(rec.hook)
	c.SetCollisionLogger(nil) // clear

	rc, err := c.GetSourceJSON(context.Background(), "foo", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()

	time.Sleep(50 * time.Millisecond)
	rec.mu.Lock()
	n := len(rec.events)
	rec.mu.Unlock()
	if n != 0 {
		t.Errorf("hook fired after SetCollisionLogger(nil): %d events", n)
	}
}

// ----- Per-upstream auth (URL userinfo → Basic header) -----

// TestCascade_AuthInjectedFromURLUserinfo verifies that an upstream
// configured with `https://user:pass@host/...` has the userinfo
// stripped from the public URL and rendered into the Authorization
// header on every probe + cascadeGet request. Without this, the
// corporate-private-forge upstream case (Plan 16 use case #1) can't
// authenticate.
func TestCascade_AuthInjectedFromURLUserinfo(t *testing.T) {
	var seenAuth atomic.Value
	seenAuth.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth.Store(r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	// Build the upstream URL with userinfo. The httptest server URL is
	// already http://127.0.0.1:NNNN; inject "oauth2:secret@" into it.
	authedURL := strings.Replace(srv.URL, "http://", "http://oauth2:my-secret-pat@", 1)

	primary := newStub()
	primary.files["bazel_registry.json"] = []byte(`{}`) // we won't test cascade-fallthrough here
	up := &Upstream{URL: authedURL}
	c, err := NewCascade(CascadeConfig{
		Primary:   primary,
		Upstreams: []*Upstream{up},
	})
	if err != nil {
		t.Fatalf("NewCascade: %v", err)
	}
	// After construction the public URL should be sanitized.
	if strings.Contains(up.URL, "my-secret-pat") {
		t.Errorf("upstream URL leaked credential: %q", up.URL)
	}
	if strings.Contains(up.URL, "oauth2:") {
		t.Errorf("upstream URL still contains userinfo: %q", up.URL)
	}

	// ProbeUpstream should inject Basic auth.
	if err := c.ProbeUpstream(t.Context(), up); err != nil {
		t.Fatalf("ProbeUpstream: %v", err)
	}
	got := seenAuth.Load().(string)
	if !strings.HasPrefix(got, "Basic ") {
		t.Errorf("Authorization header not Basic-shaped: %q", got)
	}
	// Decode + verify: base64(oauth2:my-secret-pat).
	expected := "Basic " + base64.StdEncoding.EncodeToString([]byte("oauth2:my-secret-pat"))
	if got != expected {
		t.Errorf("Authorization header = %q, want %q", got, expected)
	}
}

// TestCascade_AnonymousUpstreamNoAuthHeader verifies that an upstream
// configured WITHOUT userinfo doesn't get an Authorization header
// injected. Required for public BCR-style upstreams (bcr.bazel.build).
func TestCascade_AnonymousUpstreamNoAuthHeader(t *testing.T) {
	var seenAuth atomic.Value
	seenAuth.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth.Store(r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	up := &Upstream{URL: srv.URL}
	c, _ := NewCascade(CascadeConfig{
		Primary:   newStub(),
		Upstreams: []*Upstream{up},
	})
	if err := c.ProbeUpstream(t.Context(), up); err != nil {
		t.Fatal(err)
	}
	if got := seenAuth.Load().(string); got != "" {
		t.Errorf("unexpected Authorization on anonymous upstream: %q", got)
	}
}

// TestCascade_AuthOnUpstreamGet verifies the auth header rides on
// real BCR-shape fetches (cascadeGet), not just the boot probe.
// Without this, the probe succeeds but every actual cascade lookup
// 401s.
func TestCascade_AuthOnUpstreamGet(t *testing.T) {
	var seenAuth atomic.Value
	seenAuth.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth.Store(r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"served":"with-auth"}`))
	}))
	t.Cleanup(srv.Close)

	authedURL := strings.Replace(srv.URL, "http://", "http://oauth2:tok@", 1)
	primary := newStub() // empty → all 404 → cascade to upstream
	c, _ := NewCascade(CascadeConfig{
		Primary:   primary,
		Upstreams: []*Upstream{{URL: authedURL}},
	})
	rc, err := c.GetSourceJSON(t.Context(), "foo", "1.0.0")
	if err != nil {
		t.Fatalf("GetSourceJSON: %v", err)
	}
	got := readAllAndClose(t, rc)
	if got != `{"served":"with-auth"}` {
		t.Errorf("body = %q, want auth-served", got)
	}
	if a := seenAuth.Load().(string); !strings.HasPrefix(a, "Basic ") {
		t.Errorf("Authorization on cascade GET = %q, want Basic-prefixed", a)
	}
}

// TestCascade_CollisionLogger_TwoUpstreamShadowDetected: now that
// cascade no longer cancels siblings on winner-detect (probes run on
// context.WithoutCancel + the tail of the tally is detached into
// drainShadowed), an upstream's late-arriving 200 IS observed as
// shadowed. Before the 2026-05-21 fix this test was flaky because
// the runner-up's response was killed by the cancel signal arriving
// from the tally — net effect: shadowed events never fired in
// production.
func TestCascade_CollisionLogger_TwoUpstreamShadowDetected(t *testing.T) {
	primary := newStub() // empty → all 404
	// Both upstreams return 200 for the same (m, v). The cascade
	// hands the first winner to the caller and lets the second
	// finish in drainShadowed; we expect 2 events with distinct kinds.
	a := upstreamServer(t, map[string]upstreamResp{
		"modules/foo/1.0.0/source.json": {status: 200, body: `{"from":"a"}`},
	}, 0)
	b := upstreamServer(t, map[string]upstreamResp{
		"modules/foo/1.0.0/source.json": {status: 200, body: `{"from":"b"}`},
	}, 0)

	rec := &collisionRecorder{}
	c, _ := NewCascade(CascadeConfig{
		Primary:   primary,
		Upstreams: []*Upstream{{URL: a.URL}, {URL: b.URL}},
	})
	c.SetCollisionLogger(rec.hook)

	rc, err := c.GetSourceJSON(context.Background(), "foo", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()

	events := rec.waitForEvents(t, 2)
	if len(events) != 2 {
		t.Fatalf("want 2 events (winner + shadowed), got %d: %+v", len(events), events)
	}

	var winnerURL, shadowedURL string
	for _, ev := range events {
		if ev.module != "foo" || ev.version != "1.0.0" {
			t.Errorf("unexpected event: %+v", ev)
			continue
		}
		switch ev.kind {
		case CollisionKindUpstream:
			winnerURL = ev.source
		case CollisionKindShadowed:
			shadowedURL = ev.source
		default:
			t.Errorf("unexpected kind %q in event %+v", ev.kind, ev)
		}
	}
	if winnerURL == "" {
		t.Error("missing CollisionKindUpstream event")
	}
	if shadowedURL == "" {
		t.Error("missing CollisionKindShadowed event")
	}
	if winnerURL == shadowedURL {
		t.Errorf("winner + shadowed point at the same URL: %s", winnerURL)
	}
}

// TestCascade_DisableShadowDetection_NoShadowEvent verifies the
// operator opt-out: with DisableShadowDetection=true, the runner-up
// upstream is canceled on winner-detect and the collision-shadowed
// event NEVER fires. Mirror image of
// TestCascade_CollisionLogger_TwoUpstreamShadowDetected, which pins
// the default-on behavior.
func TestCascade_DisableShadowDetection_NoShadowEvent(t *testing.T) {
	primary := newStub() // empty → all 404
	a := upstreamServer(t, map[string]upstreamResp{
		"modules/foo/1.0.0/source.json": {status: 200, body: `{"from":"a"}`},
	}, 0)
	b := upstreamServer(t, map[string]upstreamResp{
		"modules/foo/1.0.0/source.json": {status: 200, body: `{"from":"b"}`},
	}, 0)

	rec := &collisionRecorder{}
	c, _ := NewCascade(CascadeConfig{
		Primary:                primary,
		Upstreams:              []*Upstream{{URL: a.URL}, {URL: b.URL}},
		DisableShadowDetection: true,
	})
	c.SetCollisionLogger(rec.hook)

	rc, err := c.GetSourceJSON(context.Background(), "foo", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()

	// Winner event always fires; shadowed event must NOT (the whole
	// point of the opt-out). Allow time for any spurious shadowed
	// event to arrive before asserting.
	winnerEvents := rec.waitForEvents(t, 1)
	if len(winnerEvents) < 1 {
		t.Fatalf("missing winner event: %+v", winnerEvents)
	}
	time.Sleep(200 * time.Millisecond)
	rec.mu.Lock()
	total := len(rec.events)
	rec.mu.Unlock()
	if total != 1 {
		t.Errorf("DisableShadowDetection=true should yield exactly 1 collision event (winner), got %d: %+v", total, rec.events)
	}
	// The single event must be the upstream-winner kind.
	if rec.events[0].kind != CollisionKindUpstream {
		t.Errorf("event kind = %q, want %q", rec.events[0].kind, CollisionKindUpstream)
	}
}

// ----- Background probe loop -----

// TestRunProbeLoop_RefreshesReachability runs the loop with a 30ms
// interval against a server whose status flips mid-test. Verifies the
// loop observes the change without manual intervention — the boot
// probe was once-and-done, this is the gap the loop closes.
func TestRunProbeLoop_RefreshesReachability(t *testing.T) {
	var status atomic.Int32
	status.Store(200)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write([]byte(`{"mirrors":[]}`))
	}))
	t.Cleanup(srv.Close)

	// Upstream must be registered with the cascade so RunProbeLoop
	// iterates over it; otherwise the loop sees an empty slice.
	up := &Upstream{URL: srv.URL}
	c, _ := NewCascade(CascadeConfig{
		Primary:   newStub(),
		Upstreams: []*Upstream{up},
	})

	// Boot probe: reachable.
	if err := c.ProbeUpstream(t.Context(), up); err != nil {
		t.Fatalf("boot probe: %v", err)
	}
	if r, _, _, _ := up.Reachable(); !r {
		t.Fatal("upstream should be reachable after initial 200")
	}

	// Start the loop with a short interval, then flip the server to
	// 500. The loop should observe the failure within a few ticks.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go c.RunProbeLoop(ctx, 30*time.Millisecond)

	status.Store(500)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r, _, _, _ := up.Reachable(); !r {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if r, _, _, _ := up.Reachable(); r {
		t.Error("probe loop did not observe the 500-flip; upstream still flagged reachable")
	}

	// And flip back to 200 — loop should recover.
	status.Store(200)
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r, _, _, _ := up.Reachable(); r {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if r, _, _, _ := up.Reachable(); !r {
		t.Error("probe loop did not observe 200 recovery")
	}
}

// TestRunProbeLoop_ZeroIntervalReturnsImmediately: ≤0 interval is the
// "don't run" signal — used by tests / one-shot invocations.
func TestRunProbeLoop_ZeroIntervalReturnsImmediately(t *testing.T) {
	c, _ := NewCascade(CascadeConfig{Primary: newStub()})
	// If the loop didn't bail, this test would deadlock; t.Context()
	// cancellation only fires at test end.
	done := make(chan struct{})
	go func() {
		c.RunProbeLoop(t.Context(), 0)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Error("RunProbeLoop(_, 0) did not return immediately")
	}
}

// TestRunProbeLoop_ContextCancellationStops: explicit ctx.cancel
// must terminate the loop promptly.
func TestRunProbeLoop_ContextCancellationStops(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	c, _ := NewCascade(CascadeConfig{
		Primary:   newStub(),
		Upstreams: []*Upstream{{URL: srv.URL}},
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		c.RunProbeLoop(ctx, 20*time.Millisecond)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Error("RunProbeLoop did not stop within 500ms of ctx cancel")
	}
}

// TestCascade_UpstreamBodyCapped — a compromised upstream serving a
// body larger than MaxCascadeBodyBytes must be treated as a failed
// probe so the cascade doesn't OOM buffering the response into the
// in-process cache. The test asserts the request fails (the body
// never lands as a cache entry) rather than letting an unbounded
// response succeed.
func TestCascade_UpstreamBodyCapped(t *testing.T) {
	primary := newStub() // empty; will 404
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Stream MaxCascadeBodyBytes+1 bytes — one over the cap.
		buf := make([]byte, 64*1024)
		for i := range buf {
			buf[i] = 'x'
		}
		for written := int64(0); written <= MaxCascadeBodyBytes; written += int64(len(buf)) {
			if _, err := w.Write(buf); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)

	c, err := NewCascade(CascadeConfig{
		Primary:                primary,
		Upstreams:              []*Upstream{{URL: srv.URL}},
		DisableShadowDetection: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.GetSourceJSON(context.Background(), "huge", "1.0.0")
	if err == nil {
		t.Fatal("expected error when upstream body exceeds cap")
	}
	// The cascade collapses single-upstream failures into
	// ErrUpstreamUnavailable. The underlying probeOne error wraps
	// the cap sentinel; we just confirm the outer surface marks the
	// upstream as failed (no success was returned).
	if !errors.Is(err, ErrUpstreamUnavailable) && !errors.Is(err, ErrCascadeBodyTooLarge) {
		t.Errorf("want ErrUpstreamUnavailable or ErrCascadeBodyTooLarge, got %v", err)
	}
}
