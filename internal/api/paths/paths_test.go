package paths

import (
	"testing"
)

func TestComposers_ShapeMatchesPlan13(t *testing.T) {
	// Spot-check that the composer functions emit the shape locked
	// in by Plan 13. If any of these flip, downstream tests + the UI
	// client both break — by design.
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"Search", Search(), "/api/v1/search"},
		{"ModulesIndex", ModulesIndex(), "/api/v1/modules"},
		{"ModuleVersions", ModuleVersions("rules_go"), "/api/v1/modules/rules_go/versions"},
		{"ModuleVersionDetail", ModuleVersionDetail("rules_go", "0.52.0"), "/api/v1/modules/rules_go/versions/0.52.0"},
		{"External", External("rules_go", "0.52.0"), "/api/v1/modules/rules_go/versions/0.52.0/external"},
		{"ClosureGraph", ClosureGraph("rules_go", "0.52.0"), "/api/v1/modules/rules_go/versions/0.52.0/closure/graph"},
		{"AirgapDownloaderConfig", AirgapDownloaderConfig("rules_go", "0.52.0"), "/api/v1/modules/rules_go/versions/0.52.0/airgap/downloader-config"},
		{"ActionBump", ActionBump(), "/api/v1/actions/bump"},
		{"ActionIngestMissing", ActionIngestMissing("rules_go", "0.52.0"), "/api/v1/actions/modules/rules_go/versions/0.52.0/ingest-missing"},
		{"ActivityHistory", ActivityHistory(), "/api/v1/activity/history"},
		{"SystemVersion", SystemVersion(), "/api/v1/system/version"},
		{"ModuleDiffClosure", ModuleDiffClosure("rules_go"), "/api/v1/modules/rules_go/diff/closure"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.got != c.want {
				t.Errorf("got %q, want %q", c.got, c.want)
			}
		})
	}
}

func TestComposers_URLEscape(t *testing.T) {
	// Module/version inputs come from URL params today (canopy
	// doesn't allow weird names in practice), but the composers must
	// URL-escape so callers can't accidentally build malformed URLs
	// or smuggle path segments via crafted inputs.
	got := External("my mod", "1.0/leak")
	want := "/api/v1/modules/my%20mod/versions/1.0%2Fleak/external"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
