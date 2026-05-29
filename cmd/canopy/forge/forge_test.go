package forge

import (
	"strings"
	"testing"

	"github.com/albertocavalcante/bigorna"
)

func TestNew_KnownKinds(t *testing.T) {
	// Each known kind must construct without error when its required
	// fields are present. bitbucketdc + forgejo need an explicit
	// baseURL (no canonical host); github + gitlab accept "" and
	// fall through to the underlying client's default.
	cases := []struct {
		kind    string
		baseURL string
	}{
		{"github", ""},
		{"gitlab", ""},
		{"bitbucketdc", "https://bitbucket.example.com"},
		{"forgejo", "https://codeberg.org"},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			f, err := New(tc.kind, bigorna.Repo{Owner: "o", Name: "r"}, tc.baseURL, "t0k3n", "canopy/test")
			if err != nil {
				t.Fatalf("New(%q): %v", tc.kind, err)
			}
			if f == nil {
				t.Fatalf("New(%q) returned nil forge", tc.kind)
			}
		})
	}
}

func TestNew_UnknownKind(t *testing.T) {
	_, err := New("gerrit", bigorna.Repo{Owner: "o", Name: "r"}, "", "", "")
	if err == nil {
		t.Fatal("want error for unknown forge kind")
	}
	if !strings.Contains(err.Error(), "unhandled forge") {
		t.Errorf("error message should name the kind: %v", err)
	}
	if !strings.Contains(err.Error(), `"gerrit"`) {
		t.Errorf("error should quote the bad kind: %v", err)
	}
}

func TestDisplayName(t *testing.T) {
	cases := map[string]string{
		"github":      "GitHub",
		"gitlab":      "GitLab",
		"bitbucketdc": "Bitbucket DC",
		"forgejo":     "Forgejo",
		// Unknown kinds pass through verbatim — callers use this in
		// error messages, where echoing the operator's typo is more
		// useful than silently mapping to "Unknown".
		"gerrit": "gerrit",
		"":       "",
	}
	for in, want := range cases {
		if got := DisplayName(in); got != want {
			t.Errorf("DisplayName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestNew_DisplayName_KindsAgree guards against drift between the two
// switches: any kind that New accepts MUST also have a non-passthrough
// DisplayName, otherwise error messages will read e.g. "$TOKEN not
// set; set it to a gerrit PAT" instead of "Forgejo PAT".
func TestNew_DisplayName_KindsAgree(t *testing.T) {
	kinds := []string{"github", "gitlab", "bitbucketdc", "forgejo"}
	for _, k := range kinds {
		if DisplayName(k) == k {
			t.Errorf("DisplayName(%q) returns passthrough; the kind is supported by New but lacks a friendly name", k)
		}
	}
}
