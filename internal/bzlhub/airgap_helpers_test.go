package bzlhub

import (
	"testing"
)

func TestExtensionApparentLabel(t *testing.T) {
	cases := []struct {
		name, module, file, want string
	}{
		{"nested .bzl", "rules_go", "go/extensions.bzl", "@rules_go//go:extensions.bzl"},
		{"root-level .bzl", "rules_python", "extensions.bzl", "@rules_python//:extensions.bzl"},
		{"deep path", "rules_jvm_external", "private/extensions/maven.bzl", "@rules_jvm_external//private/extensions:maven.bzl"},
		{"trailing slash trimmed", "x", "a/", "@x//a:"}, // pathological but defined
		{"empty module", "", "ext.bzl", ""},
		{"empty file", "rules_go", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extensionApparentLabel(c.module, c.file)
			if got != c.want {
				t.Errorf("extensionApparentLabel(%q, %q) = %q, want %q",
					c.module, c.file, got, c.want)
			}
		})
	}
}

func TestConfidenceFor(t *testing.T) {
	cases := []struct {
		name     string
		tainted  bool
		platform string
		want     string
	}{
		{"tainted wins over platform", true, "linux/amd64", "tainted"},
		{"tainted wins over any-platform", true, "any", "tainted"},
		{"tainted wins over empty", true, "", "tainted"},
		{"platform-specific", false, "linux/amd64", "platform-specific"},
		{"any-platform → resolved", false, "any", "resolved"},
		{"empty platform → resolved", false, "", "resolved"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := confidenceFor(c.tainted, c.platform); got != c.want {
				t.Errorf("confidenceFor(%v, %q) = %q, want %q",
					c.tainted, c.platform, got, c.want)
			}
		})
	}
}

func TestExtractMirrorHost(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"https with path", "https://mirror.example.com/", "mirror.example.com"},
		{"http with path", "http://mirror.example.com/foo/", "mirror.example.com"},
		{"with port", "https://mirror.example.com:8443/", "mirror.example.com:8443"},
		{"with userinfo + port", "https://user:pass@mirror.example.com:443/path/", "mirror.example.com:443"},
		{"no scheme fallback", "mirror.example.com/", "mirror.example.com"},
		{"no path", "https://mirror.example.com", "mirror.example.com"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractMirrorHost(c.in); got != c.want {
				t.Errorf("extractMirrorHost(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
