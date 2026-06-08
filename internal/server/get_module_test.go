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

// TestGetModule_HappyPath checks the per-module endpoint returns
// the same ModuleSummary shape one entry of /modules would carry.
// Powers the cross-module HoverCard.
func TestGetModule_HappyPath(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.WriteReport(ctx, &report.ModuleReport{Name: "rules_x", Version: "1.0.0"}); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	if err := s.WriteReport(ctx, &report.ModuleReport{Name: "rules_x", Version: "2.0.0"}); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}

	ts := httptest.NewServer(server.New(nil, bzlhub.New(s), nil))
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + paths.ModuleDetail("rules_x"))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("status=%d body=%s", res.StatusCode, body)
	}
	var got api.ModuleSummary
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Name != "rules_x" {
		t.Errorf("name = %q, want rules_x", got.Name)
	}
	if got.VersionCount != 2 {
		t.Errorf("version_count = %d, want 2", got.VersionCount)
	}
}

// TestGetModule_404ForUnknownName returns the structured "not
// indexed here" payload the HoverCard renders as its empty state.
func TestGetModule_404ForUnknownName(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ts := httptest.NewServer(server.New(nil, bzlhub.New(s), nil))
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + paths.ModuleDetail("nope"))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", res.StatusCode)
	}
	var got struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Error == "" {
		t.Error("404 must carry an error field for the hover-card empty-state copy")
	}
}
