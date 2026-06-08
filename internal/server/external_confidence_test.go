package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/albertocavalcante/assay/report"
	"github.com/albertocavalcante/bzlhub/internal/api"
	"github.com/albertocavalcante/bzlhub/internal/api/paths"
	"github.com/albertocavalcante/bzlhub/internal/bzlhub"
	"github.com/albertocavalcante/bzlhub/internal/server"
	"github.com/albertocavalcante/bzlhub/internal/store"
)

func TestExternalSurface_ConfidenceClassification(t *testing.T) {
	ctx := context.Background()
	s, _ := store.Open(ctx, ":memory:")
	t.Cleanup(func() { s.Close() })
	s.WriteReport(ctx, &report.ModuleReport{Name: "m", Version: "1"})
	s.WriteExternalRefs(ctx, "m", "1", []store.ExternalRef{
		{URL: "a", Host: "a", Platform: "any"},
		{URL: "b", Host: "b", Platform: "linux/amd64"},
		{URL: "c", Host: "c", Platform: "any", Tainted: true},
	}, nil)
	ts := httptest.NewServer(server.New(nil, bzlhub.New(s), nil))
	t.Cleanup(ts.Close)
	res, _ := http.Get(ts.URL + paths.External("m", "1"))
	body, _ := io.ReadAll(res.Body)
	var got api.ExternalSurfaceResponse
	json.Unmarshal(body, &got)
	want := map[string]string{"a": "resolved", "b": "platform-specific", "c": "tainted"}
	for _, r := range got.Refs {
		if want[r.URL] != r.Confidence {
			t.Errorf("url=%s got confidence=%q want %q", r.URL, r.Confidence, want[r.URL])
		}
	}
}
