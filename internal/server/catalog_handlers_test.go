package server

import (
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/albertocavalcante/canopy/internal/api"
)

func TestFilterModuleList(t *testing.T) {
	mods := []api.ModuleSummary{
		{Name: "alpha", VersionCount: 1, MaintainerCount: 2, UsageCount: 3},
		{Name: "rules_go", VersionCount: 7, MaintainerCount: 4, UsageCount: 20, HasSourceIndex: true},
		{Name: "rules_rust", VersionCount: 9, MaintainerCount: 5, UsageCount: 12, HasSourceIndex: true},
		{Name: "beta", VersionCount: 3, MaintainerCount: 1, UsageCount: 20, HasSourceIndex: true},
	}

	tests := []struct {
		name      string
		query     string
		wantNames []string
	}{
		{
			name:      "default usage sort desc with name tie break",
			wantNames: []string{"beta", "rules_go", "rules_rust", "alpha"},
		},
		{
			name:      "query and source filters with name sort",
			query:     "?q=RULES&source=true&sort=name",
			wantNames: []string{"rules_go", "rules_rust"},
		},
		{
			name:      "maintainer sort desc",
			query:     "?sort=maintainers",
			wantNames: []string{"rules_rust", "rules_go", "alpha", "beta"},
		},
		{
			name:      "version sort desc",
			query:     "?sort=versions",
			wantNames: []string{"rules_rust", "rules_go", "beta", "alpha"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterModuleList(append([]api.ModuleSummary(nil), mods...), httptest.NewRequest("GET", "/modules"+tt.query, nil))
			if gotNames := moduleSummaryNames(got); !reflect.DeepEqual(gotNames, tt.wantNames) {
				t.Fatalf("names = %#v, want %#v", gotNames, tt.wantNames)
			}
		})
	}
}

func moduleSummaryNames(mods []api.ModuleSummary) []string {
	out := make([]string, len(mods))
	for i, mod := range mods {
		out[i] = mod.Name
	}
	return out
}
