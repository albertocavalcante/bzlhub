package egress_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/albertocavalcante/bzlhub/internal/egress"
)

func TestDefaultHTTPClient_UsesConfiguredPolicy(t *testing.T) {
	egress.ConfigureDefault(egress.Policy{Mode: egress.ModeDeny}, egress.NopSink{})
	t.Cleanup(func() {
		egress.ConfigureDefault(egress.Policy{Mode: egress.ModeAllow}, egress.NopSink{})
	})

	req, err := http.NewRequest(http.MethodGet, "https://example.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = egress.DefaultHTTPClient().Do(req)
	if !errors.Is(err, egress.ErrEgressForbidden) {
		t.Fatalf("Do error = %v, want ErrEgressForbidden", err)
	}
}

func TestDeniedAuditStackNamesExternalCaller(t *testing.T) {
	var body strings.Builder
	egress.ConfigureDefault(egress.Policy{Mode: egress.ModeDeny}, egress.NewJSONLSink(&body))
	t.Cleanup(func() {
		egress.ConfigureDefault(egress.Policy{Mode: egress.ModeAllow}, egress.NopSink{})
	})

	req, err := http.NewRequest(http.MethodGet, "https://example.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = callDefaultClient(req)
	got := body.String()
	if strings.Contains(got, "internal/egress/client.go") {
		t.Fatalf("stack blamed egress internals: %s", got)
	}
	if !strings.Contains(got, "default_test.go") {
		t.Fatalf("stack = %s, want external fixture caller", got)
	}
}

//go:noinline
func callDefaultClient(req *http.Request) (*http.Response, error) {
	return egress.DefaultHTTPClient().Do(req)
}
