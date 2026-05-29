package server

import "testing"

// TestValidCacheKey_RejectsTraversal locks in the security boundary
// for OG image cache paths. The validation is the only defence
// between an attacker-controlled URL and a filesystem path under
// MirrorRoot; if this test ever weakens, treat it as a CVE.
func TestValidCacheKey_RejectsTraversal(t *testing.T) {
	rejected := []struct {
		module, version, why string
	}{
		{"..", "1.0", "module=.. escapes mirror root"},
		{"foo", "..", "version=.. escapes mirror root"},
		{"foo/bar", "1.0", "module contains path separator"},
		{"foo", "1.0/etc", "version contains path separator"},
		{`foo\bar`, "1.0", "module contains backslash"},
		{"foo", `1.0\bar`, "version contains backslash"},
		{"foo..bar", "1.0", "module contains substring .."},
		{"foo", "1.0..0", "version contains substring .."},
		{"", "1.0", "empty module"},
		{"foo-bar", "1.0", "module with dash (BCR uses underscores)"},
		{"foo.bar", "1.0", "module with dot"},
		{"foo bar", "1.0", "module with space"},
	}
	for _, c := range rejected {
		t.Run(c.why, func(t *testing.T) {
			if validCacheKey(c.module, c.version) {
				t.Errorf("validCacheKey(%q, %q) = true; expected false (%s)", c.module, c.version, c.why)
			}
		})
	}
}

func TestValidCacheKey_AcceptsRealistic(t *testing.T) {
	accepted := []struct {
		module, version string
	}{
		{"rules_go", "0.50.1"},
		{"bazel_skylib", "1.7.1"},
		{"container_structure_test", "1.19.1"},
		{"protobuf", "29.0-rc2"},
		{"foo123", "1.0.0"},
		{"a", "0"},
		{"some_long_module_name_with_underscores", "0.50.1.1"},
		{"foo", "1.0.0-rc1+build.123"}, // version regex doesn't include +; this should reject
	}
	for i, c := range accepted {
		t.Run(c.module+"@"+c.version, func(t *testing.T) {
			got := validCacheKey(c.module, c.version)
			// The last case (with +) should be rejected — the version
			// pattern doesn't include +. Other cases should accept.
			want := i < len(accepted)-1
			if got != want {
				t.Errorf("validCacheKey(%q, %q) = %v, want %v", c.module, c.version, got, want)
			}
		})
	}
}

func TestValidCacheKey_EmptyVersionOK(t *testing.T) {
	// Module-without-version is a legitimate shape (the /og/<module>.png
	// route). validCacheKey accepts version="" as valid.
	if !validCacheKey("rules_go", "") {
		t.Error("validCacheKey with empty version should accept (module-only OG)")
	}
}
