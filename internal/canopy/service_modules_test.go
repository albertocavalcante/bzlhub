package canopy

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/albertocavalcante/assay/report"
)

func TestServiceListModules_EnrichesMetadataAndDiffHref(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	svc.MirrorRoot = t.TempDir()

	writeServiceReport(t, ctx, svc, &report.ModuleReport{Name: "alpha", Version: "1.0.0"})
	writeServiceReport(t, ctx, svc, &report.ModuleReport{Name: "alpha", Version: "2.0.0"})
	writeServiceReport(t, ctx, svc, &report.ModuleReport{Name: "beta", Version: "0.0.0"})

	writeMetadata(t, svc.MirrorRoot, "alpha", `{
  "homepage": "https://github.com/example/alpha",
  "maintainers": [{"name": "Ada"}],
  "repository": ["github:example/alpha"],
  "versions": ["1.0.0", "2.0.0"],
  "yanked_versions": {}
}`)
	writeMetadata(t, svc.MirrorRoot, "beta", `{
  "homepage": "https://github.com/example/beta",
  "maintainers": [],
  "versions": ["0.0.0"],
  "yanked_versions": {}
}`)

	got, err := svc.ListModules(ctx)
	if err != nil {
		t.Fatalf("ListModules: %v", err)
	}
	byName := map[string]struct {
		latest        string
		count         int
		diffHref      string
		homepage      string
		repoLabel     string
		maintainers   int
		sourceIndexed bool
	}{}
	for _, m := range got {
		byName[m.Name] = struct {
			latest        string
			count         int
			diffHref      string
			homepage      string
			repoLabel     string
			maintainers   int
			sourceIndexed bool
		}{
			latest:        m.LatestVersion,
			count:         m.VersionCount,
			diffHref:      m.LatestDiffHref,
			homepage:      m.Homepage,
			repoLabel:     m.RepoLabel,
			maintainers:   m.MaintainerCount,
			sourceIndexed: m.HasSourceIndex,
		}
	}

	alpha, ok := byName["alpha"]
	if !ok {
		t.Fatalf("alpha missing from ListModules: %#v", got)
	}
	if alpha.latest != "2.0.0" || alpha.count != 2 {
		t.Fatalf("alpha latest/count = %s/%d, want 2.0.0/2", alpha.latest, alpha.count)
	}
	if alpha.diffHref != "/modules/alpha/diff?from=1.0.0&to=2.0.0" {
		t.Fatalf("alpha diff href = %q", alpha.diffHref)
	}
	if alpha.homepage != "https://github.com/example/alpha" || alpha.repoLabel != "example/alpha" || alpha.maintainers != 1 {
		t.Fatalf("alpha metadata = %#v", alpha)
	}
	if alpha.sourceIndexed {
		t.Fatal("alpha source index = true, want false without SCIP blob")
	}

	beta, ok := byName["beta"]
	if !ok {
		t.Fatalf("beta missing from ListModules: %#v", got)
	}
	if beta.diffHref != "" {
		t.Fatalf("stub-only module diff href = %q, want empty", beta.diffHref)
	}
	if beta.repoLabel != "example/beta" {
		t.Fatalf("beta repo label = %q, want homepage-derived example/beta", beta.repoLabel)
	}
}

func TestDeriveRepoLabel(t *testing.T) {
	tests := []struct {
		name     string
		repos    []string
		homepage string
		want     string
	}{
		{name: "repository wins", repos: []string{"github:owner/repo"}, homepage: "https://github.com/other/repo", want: "owner/repo"},
		{name: "gitlab repository", repos: []string{"gitlab:group/project"}, want: "group/project"},
		{name: "homepage fallback", homepage: "https://github.com/home/project/tree/main", want: "home/project"},
		{name: "unrecognized", homepage: "https://example.com/home/project", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deriveRepoLabel(tt.repos, tt.homepage); got != tt.want {
				t.Fatalf("deriveRepoLabel() = %q, want %q", got, tt.want)
			}
		})
	}
}

func writeMetadata(t *testing.T, root, module, body string) {
	t.Helper()
	path := filepath.Join(root, "modules", module, "metadata.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir metadata dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
}
