package bzlhub

import "testing"

func TestDeriveRepoLabel_GitHubRepositoryEntry(t *testing.T) {
	got := deriveRepoLabel([]string{"github:bazel-contrib/bazel-lib"}, "")
	if got != "bazel-contrib/bazel-lib" {
		t.Fatalf("got %q", got)
	}
}

func TestDeriveRepoLabel_PrefersRepositoryOverHomepage(t *testing.T) {
	got := deriveRepoLabel(
		[]string{"github:owner/repo"},
		"https://github.com/different/place",
	)
	if got != "owner/repo" {
		t.Fatalf("got %q", got)
	}
}

func TestDeriveRepoLabel_FallsBackToGithubHomepage(t *testing.T) {
	got := deriveRepoLabel(nil, "https://github.com/bazelbuild/bazel-skylib")
	if got != "bazelbuild/bazel-skylib" {
		t.Fatalf("got %q", got)
	}
}

func TestDeriveRepoLabel_EmptyWhenNeitherUseful(t *testing.T) {
	got := deriveRepoLabel(nil, "https://docs.example.com")
	if got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestDeriveRepoLabel_HandlesPathSuffix(t *testing.T) {
	// homepage might include a sub-path like /tree/main; we just
	// want the first two components.
	got := deriveRepoLabel(nil, "https://github.com/bazel-contrib/rules_lib/tree/main/docs")
	if got != "bazel-contrib/rules_lib" {
		t.Fatalf("got %q", got)
	}
}
