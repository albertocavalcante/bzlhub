package canopy

import (
	"strings"
	"testing"
)

func TestValidateBazelrcURL(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantOK    bool
		wantErrIn string // substring of err.Error() if !wantOK
	}{
		{"empty passes through", "", true, ""},
		{"whitespace trims to empty", "   ", true, ""},
		{"https accepted", "https://mirror.internal/", true, ""},
		{"http accepted", "http://mirror.internal/", true, ""},
		{"ftp rejected", "ftp://mirror.internal/", false, "scheme must be http or https"},
		{"javascript scheme rejected", "javascript:alert(1)", false, "scheme must be http or https"},
		{"file scheme rejected", "file:///etc/passwd", false, "scheme must be http or https"},
		{"newline injection rejected", "http://mirror/\ncommon --evil", false, "control character"},
		{"CR injection rejected", "http://mirror/\rcommon --evil", false, "control character"},
		{"tab rejected", "http://mirror/\tx", false, "control character"},
		{"NUL rejected", "http://mirror/\x00", false, "control character"},
		{"DEL rejected", "http://mirror/\x7F", false, "control character"},
		{"no scheme rejected", "mirror.internal/", false, "scheme must be http or https"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := validateBazelrcURL("field", c.in)
			if c.wantOK {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got value %q", c.wantErrIn, got)
			}
			if !strings.Contains(err.Error(), c.wantErrIn) {
				t.Errorf("err = %v, want substring %q", err, c.wantErrIn)
			}
		})
	}
}
