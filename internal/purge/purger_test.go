package purge

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	cdnpurge "github.com/albertocavalcante/go-cdn-purge"
)

func TestNoOp_PurgeNoErr(t *testing.T) {
	p := NewNoOp()
	if err := p.Purge(context.Background(), []string{"https://example.com/a"}); err != nil {
		t.Errorf("NoOp.Purge returned err=%v, want nil", err)
	}
	if got := p.Name(); got != "noop" {
		t.Errorf("Name=%q, want noop", got)
	}
}

// fakeUpstream is a hand-rolled cdnpurge.Provider for adapter tests.
type fakeUpstream struct {
	purges      [][]string
	returnErr   error
	failures    map[string]error
	submitted   []string
	requests    int
	name        string
}

func (f *fakeUpstream) Purge(_ context.Context, urls []string) (cdnpurge.PurgeResult, error) {
	f.purges = append(f.purges, urls)
	res := cdnpurge.PurgeResult{
		Submitted: f.submitted,
		Failures:  f.failures,
		Requests:  f.requests,
	}
	return res, f.returnErr
}

func (f *fakeUpstream) Name() string { return f.name }

func TestAdapter_DelegatesToUpstream(t *testing.T) {
	up := &fakeUpstream{name: "test-cdn", submitted: []string{"https://x"}, requests: 1}
	a := NewAdapter(up, slog.Default())
	if err := a.Purge(context.Background(), []string{"https://x"}); err != nil {
		t.Errorf("Purge err=%v, want nil", err)
	}
	if len(up.purges) != 1 || up.purges[0][0] != "https://x" {
		t.Errorf("upstream not called with the URL: %v", up.purges)
	}
	if got := a.Name(); got != "test-cdn" {
		t.Errorf("Name=%q, want test-cdn", got)
	}
}

func TestAdapter_EmptyURLsShortCircuits(t *testing.T) {
	up := &fakeUpstream{name: "noop"}
	a := NewAdapter(up, slog.Default())
	if err := a.Purge(context.Background(), nil); err != nil {
		t.Errorf("err=%v on empty input, want nil", err)
	}
	if len(up.purges) != 0 {
		t.Errorf("upstream called on empty input: %v", up.purges)
	}
}

func TestAdapter_UpstreamErrorPropagates(t *testing.T) {
	want := errors.New("upstream 503")
	up := &fakeUpstream{name: "test", returnErr: want}
	a := NewAdapter(up, slog.Default())
	err := a.Purge(context.Background(), []string{"https://x"})
	if !errors.Is(err, want) {
		t.Errorf("err=%v, want errors.Is %v", err, want)
	}
}

func TestAdapter_PerURLFailuresAreLoggedNotReturned(t *testing.T) {
	up := &fakeUpstream{
		name:      "test",
		submitted: []string{"https://ok"},
		failures:  map[string]error{"https://bad": errors.New("bad")},
		requests:  1,
	}
	a := NewAdapter(up, slog.Default())
	if err := a.Purge(context.Background(), []string{"https://ok", "https://bad"}); err != nil {
		t.Errorf("per-URL failures should not return error; got %v", err)
	}
}

func TestURLsForModule_HappyPath(t *testing.T) {
	got := URLsForModule("https://bcr.bzlhub.com", "rules_python")
	want := []string{
		"https://bcr.bzlhub.com/modules/rules_python/metadata.json",
		"https://bcr.bzlhub.com/bazel_registry.json",
	}
	if len(got) != len(want) {
		t.Fatalf("len(got)=%d, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("got[%d]=%q, want %q", i, got[i], w)
		}
	}
}

func TestURLsForModule_TrimsTrailingSlash(t *testing.T) {
	got := URLsForModule("https://bcr.bzlhub.com/", "rules_python")
	if got[0] != "https://bcr.bzlhub.com/modules/rules_python/metadata.json" {
		t.Errorf("got[0]=%q, double-slash leak?", got[0])
	}
}

func TestURLsForModule_EmptyInputReturnsNil(t *testing.T) {
	if got := URLsForModule("", "rules_python"); got != nil {
		t.Errorf("empty baseURL: got %v, want nil", got)
	}
	if got := URLsForModule("https://x", ""); got != nil {
		t.Errorf("empty module: got %v, want nil", got)
	}
}
