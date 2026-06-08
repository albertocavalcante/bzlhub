package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/albertocavalcante/assay/report"
	scipproto "github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"

	"github.com/albertocavalcante/bzlhub/internal/api"
	"github.com/albertocavalcante/bzlhub/internal/api/paths"
	"github.com/albertocavalcante/bzlhub/internal/bzlhub"
	"github.com/albertocavalcante/bzlhub/internal/server"
	"github.com/albertocavalcante/bzlhub/internal/store"
)

// Plan 07 cross-corpus consumer endpoint. The pipeline:
//
//   1. resolve (module, version, name) → SCIP symbol via the
//      ModuleReport's Rule/Provider/Macro/RepoRule/ModuleExtension
//      provenance
//   2. LookupXRefs(symbol, includeDefinition=false)
//   3. filter the defining module's own occurrences (unless
//      include_self=true)
//   4. return grouped ConsumersResult
//
// Test seeds two modules:
//   - producer@1 declares my_rule at rules/lib.bzl. SCIP symbol:
//     "bzlmod producer@1 rules/lib.bzl#my_rule"
//   - consumer@1 has a SCIP blob with a non-definition occurrence
//     of that symbol at uses/foo.bzl line 7.
//
// Expectations: GET /consumers/my_rule returns one ConsumerEntry
// for consumer@1 with one CallSite at uses/foo.bzl:7, with the
// defining module filtered out.
func TestConsumers_EndToEnd(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Producer module: declares my_rule with provenance at rules/lib.bzl.
	producer := &report.ModuleReport{
		Name: "producer", Version: "1",
		Rules: []report.RuleSpec{
			{Name: "my_rule", Provenance: report.Provenance{File: "rules/lib.bzl"}},
		},
	}
	if err := s.WriteReport(ctx, producer); err != nil {
		t.Fatal(err)
	}

	// Consumer module: empty report (we only need the SCIP blob).
	if err := s.WriteReport(ctx, &report.ModuleReport{Name: "consumer", Version: "1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteScipBlob(ctx, "consumer", "1", scipBlobWithReference(t,
		"bzlmod producer@1 rules/lib.bzl#my_rule",
		"uses/foo.bzl", 7,
	)); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(server.New(nil, bzlhub.New(s), nil))
	t.Cleanup(ts.Close)

	t.Run("happy path filters self by default", func(t *testing.T) {
		res, err := http.Get(ts.URL + paths.Consumers("producer", "1", "my_rule"))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status %d body=%s", res.StatusCode, body)
		}
		var got api.ConsumersResult
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("unmarshal: %v body=%s", err, body)
		}
		if got.Kind != "rule" {
			t.Errorf("kind = %q, want rule", got.Kind)
		}
		if got.File != "rules/lib.bzl" {
			t.Errorf("file = %q, want rules/lib.bzl", got.File)
		}
		if got.Symbol != "bzlmod producer@1 rules/lib.bzl#my_rule" {
			t.Errorf("symbol = %q", got.Symbol)
		}
		if got.ConsumerCount != 1 {
			t.Fatalf("consumer_count = %d, want 1; consumers=%+v", got.ConsumerCount, got.Consumers)
		}
		if got.Consumers[0].Module != "consumer" || got.Consumers[0].Version != "1" {
			t.Errorf("consumer entry = %q@%q", got.Consumers[0].Module, got.Consumers[0].Version)
		}
		if len(got.Consumers[0].CallSites) != 1 {
			t.Fatalf("call_sites = %d, want 1", len(got.Consumers[0].CallSites))
		}
		cs := got.Consumers[0].CallSites[0]
		if cs.File != "uses/foo.bzl" || cs.Line != 7 {
			t.Errorf("call site = %q:%d, want uses/foo.bzl:7", cs.File, cs.Line)
		}
		// Code-nav href must match the UI-side shape exactly
		// (codeNavFileHref in ui/src/lib/links.ts) — segment-encoded
		// path under /code-nav/file/, line as query param.
		wantHref := "/modules/consumer/1/code-nav/file/uses/foo.bzl?line=7"
		if cs.Href != wantHref {
			t.Errorf("href = %q, want %q", cs.Href, wantHref)
		}
		if got.TotalCallSites != 1 {
			t.Errorf("total_call_sites = %d, want 1", got.TotalCallSites)
		}
	})

	t.Run("unknown name → 404", func(t *testing.T) {
		res, err := http.Get(ts.URL + paths.Consumers("producer", "1", "no_such_rule"))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("status = %d, want 404", res.StatusCode)
		}
	})

	t.Run("unknown module → 404", func(t *testing.T) {
		res, err := http.Get(ts.URL + paths.Consumers("nope", "0", "anything"))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("status = %d, want 404", res.StatusCode)
		}
	})
}

// scipBlobWithReference returns a minimal SCIP index containing a
// single non-definition occurrence of `symbol` at (file, line). The
// blob is the wire shape LookupXRefs walks.
func scipBlobWithReference(t *testing.T, symbol, file string, line int32) []byte {
	t.Helper()
	idx := &scipproto.Index{
		Metadata: &scipproto.Metadata{Version: 0},
		Documents: []*scipproto.Document{{
			RelativePath: file,
			Occurrences: []*scipproto.Occurrence{{
				Symbol: symbol,
				// Range: [startLine, startChar, endLine, endChar]
				// (3-element variant means same-line, char only).
				Range: []int32{line, 0, line, 7},
				// SymbolRoles: 0 means "plain reference" (not
				// Definition, not Import, not WriteAccess). This is
				// what scip-bazel emits for a rule call in a BUILD/
				// .bzl file.
				SymbolRoles: 0,
			}},
		}},
	}
	b, err := proto.Marshal(idx)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
