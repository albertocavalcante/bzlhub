package drift

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// writeLocalMirror sets up modulesDir layout with metadata.json per module.
func writeLocalMirror(t *testing.T, modules map[string][]string) string {
	t.Helper()
	root := t.TempDir()
	for name, versions := range modules {
		dir := filepath.Join(root, "modules", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		md := map[string]any{"versions": versions}
		b, _ := json.Marshal(md)
		if err := os.WriteFile(filepath.Join(dir, "metadata.json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// fakeUpstream serves metadata.json per module.
func fakeUpstream(t *testing.T, modules map[string]map[string]any) (string, func()) {
	t.Helper()
	mux := http.NewServeMux()
	for name, meta := range modules {
		// capture
		path := "/modules/" + name + "/metadata.json"
		m := meta
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(m)
		})
	}
	srv := httptest.NewServer(mux)
	return srv.URL, srv.Close
}

func TestComputeBehind(t *testing.T) {
	mirror := writeLocalMirror(t, map[string][]string{
		"foo": {"1.0.0", "1.1.0"},
	})
	up, stop := fakeUpstream(t, map[string]map[string]any{
		"foo": {"versions": []string{"1.0.0", "1.1.0", "2.0.0", "2.0.1"}},
	})
	defer stop()

	r, err := Compute(context.Background(), mirror, up, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Modules) != 1 {
		t.Fatalf("modules=%d", len(r.Modules))
	}
	m := r.Modules[0]
	if m.Status != Behind {
		t.Fatalf("status=%v want Behind", m.Status)
	}
	if m.LocalLatest != "1.1.0" || m.UpstreamLatest != "2.0.1" {
		t.Fatalf("latest %s vs %s", m.LocalLatest, m.UpstreamLatest)
	}
	wantNewer := []string{"2.0.0", "2.0.1"}
	if len(m.NewerUpstream) != len(wantNewer) {
		t.Fatalf("newer=%v want %v", m.NewerUpstream, wantNewer)
	}
}

func TestComputeYanked(t *testing.T) {
	mirror := writeLocalMirror(t, map[string][]string{
		"foo": {"1.0.0", "1.1.0"},
	})
	up, stop := fakeUpstream(t, map[string]map[string]any{
		"foo": {
			"versions":        []string{"1.0.0", "1.1.0"},
			"yanked_versions": map[string]string{"1.0.0": "bad release"},
		},
	})
	defer stop()

	r, err := Compute(context.Background(), mirror, up, Options{})
	if err != nil {
		t.Fatal(err)
	}
	m := r.Modules[0]
	if m.Status != YankedUpstream {
		t.Fatalf("status=%v want YankedUpstream", m.Status)
	}
	if len(m.YankedAtUpstream) != 1 || m.YankedAtUpstream[0] != "1.0.0" {
		t.Fatalf("yanked=%v", m.YankedAtUpstream)
	}
}

func TestComputeLocalOnly404(t *testing.T) {
	mirror := writeLocalMirror(t, map[string][]string{
		"private": {"0.1.0"},
	})
	// Upstream serves 404 for any metadata path (no handlers registered).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	r, err := Compute(context.Background(), mirror, srv.URL, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Modules[0].Status != LocalOnly {
		t.Fatalf("status=%v want LocalOnly", r.Modules[0].Status)
	}
}

func TestComputeInSync(t *testing.T) {
	mirror := writeLocalMirror(t, map[string][]string{
		"foo": {"1.0.0", "1.1.0", "2.0.0"},
	})
	up, stop := fakeUpstream(t, map[string]map[string]any{
		"foo": {"versions": []string{"1.0.0", "1.1.0", "2.0.0"}},
	})
	defer stop()

	r, _ := Compute(context.Background(), mirror, up, Options{})
	if r.Modules[0].Status != InSync {
		t.Fatalf("status=%v want InSync", r.Modules[0].Status)
	}
}

func TestVersionComparator(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.7.1", "1.8.2", -1},
		{"1.7.1", "1.7.1", 0},
		{"2.0.0", "1.99.99", 1},
		{"0.0.10", "0.0.9", 1},   // numeric, not string!
		{"1.0.0", "1.0.0.1", -1}, // canopy variant convention
		{"", "1.0.0", -1},
	}
	for _, c := range cases {
		got := compareVersions(c.a, c.b)
		if got != c.want {
			t.Errorf("compare(%q,%q) = %d want %d", c.a, c.b, got, c.want)
		}
	}
}
