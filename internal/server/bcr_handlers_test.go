package server

import "testing"

func TestContentTypeFor(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{name: "metadata.json", want: "application/json"},
		{name: "archive.tar.gz", want: "application/gzip"},
		{name: "archive.tgz", want: "application/gzip"},
		{name: "archive.tar", want: "application/x-tar"},
		{name: "archive.zip", want: "application/zip"},
		{name: "fix.patch", want: "text/x-patch"},
		{name: "fix.diff", want: "text/x-patch"},
		{name: "MODULE.bazel", want: "text/plain; charset=utf-8"},
		{name: "defs.bzl", want: "text/plain; charset=utf-8"},
		{name: "blob.bin", want: "application/octet-stream"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := contentTypeFor(tt.name); got != tt.want {
				t.Fatalf("contentTypeFor(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}
