package publish

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/albertocavalcante/bigorna"
	"github.com/albertocavalcante/bzlhub/internal/publish"
)

func TestSplitModuleAtVersion(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantModule  string
		wantVersion string
		wantErr     bool
	}{
		{name: "valid", input: "rules_go@0.50.1", wantModule: "rules_go", wantVersion: "0.50.1"},
		{name: "extra separator", input: "scope@module@1.0.0", wantErr: true},
		{name: "missing separator", input: "rules_go", wantErr: true},
		{name: "missing module", input: "@1.0.0", wantErr: true},
		{name: "missing version", input: "rules_go@", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotModule, gotVersion, err := splitModuleAtVersion(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("splitModuleAtVersion(%q) err = nil, want error", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("splitModuleAtVersion(%q): %v", tt.input, err)
			}
			if gotModule != tt.wantModule || gotVersion != tt.wantVersion {
				t.Fatalf("splitModuleAtVersion(%q) = %q, %q; want %q, %q",
					tt.input, gotModule, gotVersion, tt.wantModule, tt.wantVersion)
			}
		})
	}
}

func TestBranchForMode(t *testing.T) {
	if got := branchForMode(false, "main", "canopy/add-foo-1.0.0"); got != "canopy/add-foo-1.0.0" {
		t.Fatalf("PR mode branch = %q", got)
	}
	if got := branchForMode(true, "main", "canopy/add-foo-1.0.0"); got != "main" {
		t.Fatalf("commit mode branch = %q", got)
	}
}

func TestPublishOutput_JSONResult(t *testing.T) {
	var stdout, stderr bytes.Buffer
	o := &publishOutput{json: true, stdout: &stdout, stderr: &stderr}

	err := o.emitResult(publishResult{
		Module:     "rules_go",
		Version:    "0.50.1",
		PRNumber:   42,
		PRURL:      "https://example.test/pr/42",
		HeadBranch: "canopy/add-rules_go-0.50.1",
		BaseBranch: "main",
		Strategy:   "pr",
		DurationMs: 123,
	})
	if err != nil {
		t.Fatalf("emitResult: %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("json output wrote stderr: %q", stderr.String())
	}
	var got publishResult
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode json result %q: %v", stdout.String(), err)
	}
	if got.Module != "rules_go" || got.PRNumber != 42 || got.Strategy != "pr" {
		t.Fatalf("decoded result = %+v", got)
	}
}

func TestPublishOutput_ShowConfigRedactsToken(t *testing.T) {
	var stderr bytes.Buffer
	o := &publishOutput{stderr: &stderr}
	o.showConfig(
		publishConfig{
			worktree:   "/registry",
			forge:      "github",
			repo:       bigorna.Repo{Owner: "owner", Name: "repo"},
			tokenEnv:   "BZLHUB_GITHUB_TOKEN",
			token:      "ghp_real_secret",
			baseBranch: "main",
			bot:        publish.Identity{Name: "canopy", Email: "canopy@example.test"},
			requester:  publish.Identity{Name: "Ada", Email: "ada@example.test"},
		},
		publishSource{from: "https://bcr.bazel.build"},
		"rules_go",
		"0.50.1",
	)
	got := stderr.String()
	if strings.Contains(got, "ghp_real_secret") {
		t.Fatalf("showConfig leaked token:\n%s", got)
	}
	if !strings.Contains(got, "$BZLHUB_GITHUB_TOKEN (set; redacted)") {
		t.Fatalf("showConfig did not show redacted token env:\n%s", got)
	}
}
