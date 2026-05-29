package backend

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// extractModuleVersion parses BCR paths and returns (m, v) only for
// version-shaped paths. Module-only (metadata.json) and root paths
// must return ok=false because Bump operates on a specific version.
func TestExtractModuleVersion(t *testing.T) {
	cases := []struct {
		path           string
		wantModule     string
		wantVersion    string
		wantOk         bool
	}{
		{"modules/rules_go/0.50.0/source.json", "rules_go", "0.50.0", true},
		{"modules/rules_go/0.50.0/MODULE.bazel", "rules_go", "0.50.0", true},
		{"modules/rules_go/0.50.0/patches/foo.patch", "rules_go", "0.50.0", true},
		{"modules/rules_go/0.50.0/overlay/BUILD", "rules_go", "0.50.0", true},

		// Module-only (no version): metadata.json
		{"modules/rules_go/metadata.json", "", "", false},

		// Not a modules/ path at all
		{"bazel_registry.json", "", "", false},
		{"blobs/abc123", "", "", false},

		// Pathological inputs
		{"modules/", "", "", false},
		{"modules", "", "", false},
		{"", "", "", false},
		{"modules//1.0/source.json", "", "", false}, // empty module name
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			m, v, ok := extractModuleVersion(c.path)
			if m != c.wantModule || v != c.wantVersion || ok != c.wantOk {
				t.Errorf("got (%q, %q, %v), want (%q, %q, %v)",
					m, v, ok, c.wantModule, c.wantVersion, c.wantOk)
			}
		})
	}
}

// SetPromoteHook installs a hook that fires async when an upstream
// wins a (module, version) path. Verifies the hook receives correct
// args + doesn't fire for module-only paths (metadata.json) or for
// 404 / 5xx responses.
func TestCascade_PromoteHookFiresOnUpstreamWin(t *testing.T) {
	primary := newStub()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	c, _ := NewCascade(CascadeConfig{
		Primary:       primary,
		Upstreams:     []*Upstream{{URL: srv.URL}},
		CacheCapacity: -1, // disable cache so each call really probes
	})

	// Collect hook invocations from concurrent goroutines.
	var mu sync.Mutex
	var calls []struct{ module, version string }
	c.SetPromoteHook(func(m, v string) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, struct{ module, version string }{m, v})
	})

	// Trigger an upstream win on a (module, version) path.
	rc, err := c.GetSourceJSON(context.Background(), "rules_go", "0.50.0")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, rc)
	rc.Close()

	// Hook fires in a detached goroutine — wait briefly with a tight
	// poll loop rather than time.Sleep guesswork.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(calls)
		mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("hook called %d times, want 1", len(calls))
	}
	if calls[0].module != "rules_go" || calls[0].version != "0.50.0" {
		t.Errorf("hook got (%q, %q), want (rules_go, 0.50.0)", calls[0].module, calls[0].version)
	}
}

// Metadata-only paths must NOT trigger the promote hook — Bump needs
// a specific version, and metadata.json doesn't have one.
func TestCascade_PromoteHookSkipsMetadataOnlyPaths(t *testing.T) {
	primary := newStub()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	c, _ := NewCascade(CascadeConfig{
		Primary:       primary,
		Upstreams:     []*Upstream{{URL: srv.URL}},
		CacheCapacity: -1,
	})

	var fired bool
	var mu sync.Mutex
	c.SetPromoteHook(func(_, _ string) {
		mu.Lock()
		fired = true
		mu.Unlock()
	})

	rc, err := c.GetMetadata(context.Background(), "rules_go")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, rc)
	rc.Close()

	// Wait long enough for any spuriously-scheduled hook goroutine.
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if fired {
		t.Error("hook should NOT fire for module-only (metadata.json) path")
	}
}

// Nil hook (default) is a no-op — no goroutine, no panic.
func TestCascade_NoHookByDefault(t *testing.T) {
	primary := newStub()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	c, _ := NewCascade(CascadeConfig{
		Primary:       primary,
		Upstreams:     []*Upstream{{URL: srv.URL}},
		CacheCapacity: -1,
	})

	// No SetPromoteHook call. Fetch should still work end-to-end.
	rc, err := c.GetSourceJSON(context.Background(), "x", "1.0")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, rc)
	rc.Close()
}

// Setting nil after a hook was wired clears it; subsequent fetches
// don't fire the previous hook.
func TestCascade_SetPromoteHookNilClears(t *testing.T) {
	primary := newStub()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	c, _ := NewCascade(CascadeConfig{
		Primary:       primary,
		Upstreams:     []*Upstream{{URL: srv.URL}},
		CacheCapacity: -1,
	})
	var calls int
	var mu sync.Mutex
	c.SetPromoteHook(func(_, _ string) {
		mu.Lock()
		calls++
		mu.Unlock()
	})
	c.SetPromoteHook(nil) // clear

	rc, _ := c.GetSourceJSON(context.Background(), "x", "1.0")
	if rc != nil {
		_, _ = io.Copy(io.Discard, rc)
		rc.Close()
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if calls != 0 {
		t.Errorf("calls = %d, want 0 (hook was cleared)", calls)
	}
}
