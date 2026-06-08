package admit

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildSourceJSON_HappyPath(t *testing.T) {
	got, err := BuildSourceJSON(
		"https://github.com/x/y/archive/v1.0.tar.gz",
		"sha256-abc123==",
		"y-1.0",
	)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("output not valid json: %v", err)
	}
	if parsed["url"] != "https://github.com/x/y/archive/v1.0.tar.gz" {
		t.Errorf("url = %v", parsed["url"])
	}
	if parsed["integrity"] != "sha256-abc123==" {
		t.Errorf("integrity = %v", parsed["integrity"])
	}
	if parsed["strip_prefix"] != "y-1.0" {
		t.Errorf("strip_prefix = %v", parsed["strip_prefix"])
	}
}

func TestBuildSourceJSON_OmitsEmptyStripPrefix(t *testing.T) {
	got, err := BuildSourceJSON("https://example.com/x.tar.gz", "sha256-deadbeef==", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "strip_prefix") {
		t.Errorf("output should omit strip_prefix when empty: %s", got)
	}
}

func TestBuildSourceJSON_RejectsEmptyURL(t *testing.T) {
	_, err := BuildSourceJSON("", "sha256-abc==", "")
	if err == nil {
		t.Error("want error on empty url")
	}
}

func TestBuildSourceJSON_RejectsEmptyIntegrity(t *testing.T) {
	_, err := BuildSourceJSON("https://example.com/x.tar.gz", "", "")
	if err == nil {
		t.Error("want error on empty integrity")
	}
}

func TestBuildSourceJSON_RejectsMalformedIntegrity(t *testing.T) {
	// SRI prefix required — must be "sha256-..." or similar.
	_, err := BuildSourceJSON("https://example.com/x.tar.gz", "deadbeef", "")
	if err == nil {
		t.Error("want error on integrity missing SRI scheme prefix")
	}
}

func TestSRIFromSHA256_Stable(t *testing.T) {
	// 32 zero bytes → known SRI value.
	zeros := make([]byte, 32)
	got := SRIFromSHA256(zeros)
	want := "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	if got != want {
		t.Errorf("SRIFromSHA256(zeros) = %q, want %q", got, want)
	}
}

func TestDetectStripPrefix(t *testing.T) {
	cases := []struct {
		name    string
		entries []string
		want    string
	}{
		{"github archive", []string{"rules_python-1.5.0/", "rules_python-1.5.0/MODULE.bazel", "rules_python-1.5.0/BUILD"}, "rules_python-1.5.0"},
		{"no common prefix", []string{"foo/x", "bar/y"}, ""},
		{"empty", []string{}, ""},
		{"single file at root", []string{"MODULE.bazel"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := DetectStripPrefix(c.entries)
			if got != c.want {
				t.Errorf("DetectStripPrefix(%v) = %q, want %q", c.entries, got, c.want)
			}
		})
	}
}
