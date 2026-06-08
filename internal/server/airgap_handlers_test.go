package server

import (
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/albertocavalcante/bzlhub/internal/api"
)

func TestApplyRefFilters(t *testing.T) {
	refs := []api.ExternalRef{
		{URL: "https://github.com/a/archive.tar.gz", Host: "github.com", Class: "github-archive", Mutability: "mutable-host"},
		{URL: "https://github.com/b/archive.tar.gz", Host: "github.com", Class: "github-archive", Mutability: "mutable-host", Tainted: true},
		{URL: "https://dl.google.com/go.tar.gz", Host: "dl.google.com", Class: "vendor-http", Mutability: "immutable"},
		{URL: "https://opaque.example/blob", Host: "opaque.example", Class: "unknown", Mutability: "unknown", Tainted: true},
	}

	tests := []struct {
		name       string
		query      string
		wantURLs   []string
		wantCounts map[string]int
	}{
		{
			name:       "no filters keeps all refs and recomputes counts",
			wantURLs:   []string{"https://github.com/a/archive.tar.gz", "https://github.com/b/archive.tar.gz", "https://dl.google.com/go.tar.gz", "https://opaque.example/blob"},
			wantCounts: map[string]int{"github-archive": 2, "vendor-http": 1, "unknown": 1},
		},
		{
			name:       "comma list class filter",
			query:      "?class=github-archive,vendor-http",
			wantURLs:   []string{"https://github.com/a/archive.tar.gz", "https://github.com/b/archive.tar.gz", "https://dl.google.com/go.tar.gz"},
			wantCounts: map[string]int{"github-archive": 2, "vendor-http": 1},
		},
		{
			name:       "combined host class and tainted exclusion",
			query:      "?host=github.com&class=github-archive&tainted=exclude",
			wantURLs:   []string{"https://github.com/a/archive.tar.gz"},
			wantCounts: map[string]int{"github-archive": 1},
		},
		{
			name:       "mutability and tainted only",
			query:      "?mutability=unknown&tainted=only",
			wantURLs:   []string{"https://opaque.example/blob"},
			wantCounts: map[string]int{"unknown": 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotCounts := applyRefFilters(append([]api.ExternalRef(nil), refs...), httptest.NewRequest("GET", "/external"+tt.query, nil))
			if gotURLs := externalRefURLs(got); !reflect.DeepEqual(gotURLs, tt.wantURLs) {
				t.Fatalf("URLs = %#v, want %#v", gotURLs, tt.wantURLs)
			}
			if !reflect.DeepEqual(gotCounts, tt.wantCounts) {
				t.Fatalf("counts = %#v, want %#v", gotCounts, tt.wantCounts)
			}
		})
	}
}

func TestFilterClosureRefs(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		wantURLs   []string
		wantCounts map[string]int
	}{
		{
			name:       "bare module name",
			query:      "?module=leaf",
			wantURLs:   []string{"https://leaf.example/archive.tar.gz", "https://leaf.example/checksum.txt"},
			wantCounts: map[string]int{"vendor-http": 1, "metadata": 1},
		},
		{
			name:       "qualified module name",
			query:      "?module=root@1.0.0",
			wantURLs:   []string{"https://root.example/archive.tar.gz"},
			wantCounts: map[string]int{"github-archive": 1},
		},
		{
			name:       "module filter combines with ref filters",
			query:      "?module=leaf&class=vendor-http&tainted=exclude",
			wantURLs:   []string{"https://leaf.example/archive.tar.gz"},
			wantCounts: map[string]int{"vendor-http": 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &api.ClosureSurfaceResponse{
				Refs: []api.ExternalRef{
					{URL: "https://root.example/archive.tar.gz", Host: "root.example", Class: "github-archive", Mutability: "mutable-host", SourceModule: "root@1.0.0"},
					{URL: "https://leaf.example/archive.tar.gz", Host: "leaf.example", Class: "vendor-http", Mutability: "immutable", SourceModule: "leaf@1.0.0"},
					{URL: "https://leaf.example/checksum.txt", Host: "leaf.example", Class: "metadata", Mutability: "immutable", SourceModule: "leaf@1.0.0", Tainted: true},
					{URL: "https://unknown.example/blob", Host: "unknown.example", Class: "unknown", Mutability: "unknown"},
				},
			}
			filterClosureRefs(resp, httptest.NewRequest("GET", "/surface"+tt.query, nil))
			if gotURLs := externalRefURLs(resp.Refs); !reflect.DeepEqual(gotURLs, tt.wantURLs) {
				t.Fatalf("URLs = %#v, want %#v", gotURLs, tt.wantURLs)
			}
			if !reflect.DeepEqual(resp.ClassCounts, tt.wantCounts) {
				t.Fatalf("counts = %#v, want %#v", resp.ClassCounts, tt.wantCounts)
			}
		})
	}
}

func externalRefURLs(refs []api.ExternalRef) []string {
	out := make([]string, len(refs))
	for i, ref := range refs {
		out[i] = ref.URL
	}
	return out
}
