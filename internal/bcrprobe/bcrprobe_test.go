package bcrprobe

import (
	"context"
	"errors"
	"testing"

	"github.com/albertocavalcante/bzlhub/internal/fetch"
)

// fakeProber is a deterministic Prober — each call looks up the (m,v)
// in maps, returning the recorded result. Lets the tests pin the
// exact behavior we care about without spinning up an HTTP server.
type fakeProber struct {
	source   map[string]*fetch.SourceJSON // key "m@v"
	metadata map[string]*fetch.MetadataJSON
	// Optional non-404 error injection per key, to simulate 5xx / TLS
	// failures.
	sourceErr   map[string]error
	metadataErr map[string]error
}

func (f *fakeProber) GetSourceJSON(_ context.Context, _, m, v string) (*fetch.SourceJSON, error) {
	k := m + "@" + v
	if e, ok := f.sourceErr[k]; ok {
		return nil, e
	}
	if s, ok := f.source[k]; ok {
		return s, nil
	}
	return nil, fetch.ErrNotFound
}

func (f *fakeProber) GetMetadata(_ context.Context, _, m string) (*fetch.MetadataJSON, error) {
	if e, ok := f.metadataErr[m]; ok {
		return nil, e
	}
	if md, ok := f.metadata[m]; ok {
		return md, nil
	}
	return nil, fetch.ErrNotFound
}

func TestProbe_VersionExists_ShortCircuits(t *testing.T) {
	p := &fakeProber{
		source: map[string]*fetch.SourceJSON{"rules_go@0.50.1": {URL: "https://example/"}},
		// metadata intentionally not stocked — the probe must NOT
		// fetch it once source.json succeeds. If it did, we'd 404
		// here, breaking the test.
	}
	res, err := Probe(context.Background(), p, "https://bcr/", "rules_go", "0.50.1")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !res.VersionExists {
		t.Error("VersionExists = false, want true")
	}
	if !res.ModuleExists {
		t.Error("ModuleExists = false, want true (implied by version)")
	}
	if res.VersionsAvailable != nil {
		t.Error("VersionsAvailable should be nil when version probe short-circuited")
	}
}

func TestProbe_ModuleExistsButVersionDoesnt_SuggestsLatest(t *testing.T) {
	p := &fakeProber{
		// no entry for rules_go@99.0.0 → source.json 404s
		metadata: map[string]*fetch.MetadataJSON{
			"rules_go": {Versions: []string{"0.49.0", "0.50.0", "0.50.1"}},
		},
	}
	res, err := Probe(context.Background(), p, "https://bcr/", "rules_go", "99.0.0")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if res.VersionExists {
		t.Error("VersionExists = true, want false")
	}
	if !res.ModuleExists {
		t.Error("ModuleExists = false, want true")
	}
	if res.LatestVersion != "0.50.1" {
		t.Errorf("LatestVersion = %q, want 0.50.1 (BCR-canonical last)", res.LatestVersion)
	}
	if len(res.VersionsAvailable) != 3 {
		t.Errorf("VersionsAvailable len = %d, want 3", len(res.VersionsAvailable))
	}
}

func TestProbe_ModuleDoesntExist_BothFalse(t *testing.T) {
	p := &fakeProber{} // nothing stocked → everything 404s
	res, err := Probe(context.Background(), p, "https://bcr/", "rules_js", "2.6.1")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if res.VersionExists || res.ModuleExists {
		t.Errorf("both should be false, got VersionExists=%v ModuleExists=%v", res.VersionExists, res.ModuleExists)
	}
	if res.LatestVersion != "" {
		t.Errorf("LatestVersion = %q, want empty", res.LatestVersion)
	}
	if res.Module != "rules_js" || res.Version != "2.6.1" {
		t.Errorf("coordinates not echoed: got %+v", res)
	}
}

func TestProbe_TransportError_PropagatesNon404(t *testing.T) {
	// 5xx from BCR is an operational issue (canopy can't tell the
	// user anything useful) — must surface as error, not as a
	// "module doesn't exist" answer.
	boom := errors.New("GET https://bcr/...: HTTP 503")
	p := &fakeProber{
		sourceErr: map[string]error{"rules_go@0.50.1": boom},
	}
	_, err := Probe(context.Background(), p, "https://bcr/", "rules_go", "0.50.1")
	if err == nil {
		t.Fatal("Probe: want error, got nil")
	}
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want wraps boom", err)
	}
}

func TestProbe_MetadataTransportErrorPropagates(t *testing.T) {
	// Even when source.json 404s correctly, a non-404 metadata
	// failure must surface — otherwise the UI would render "module
	// doesn't exist" when actually we don't know yet.
	boom := errors.New("GET https://bcr/.../metadata.json: HTTP 503")
	p := &fakeProber{
		metadataErr: map[string]error{"rules_go": boom},
	}
	_, err := Probe(context.Background(), p, "https://bcr/", "rules_go", "0.50.1")
	if err == nil {
		t.Fatal("Probe: want error, got nil")
	}
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want wraps boom", err)
	}
}
