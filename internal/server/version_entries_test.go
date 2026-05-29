package server

import (
	"testing"
	"time"
)

func TestCadenceLabel(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{name: "negative", d: -time.Second, want: ""},
		{name: "sub hour", d: 30 * time.Minute, want: "+<1h"},
		{name: "hours", d: 3 * time.Hour, want: "+3h"},
		{name: "days", d: 3 * 24 * time.Hour, want: "+3d"},
		{name: "months fractional", d: 60 * 24 * time.Hour, want: "+2.0mo"},
		{name: "months whole over year", d: 365 * 24 * time.Hour, want: "+12mo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cadenceLabel(tt.d); got != tt.want {
				t.Fatalf("cadenceLabel(%s) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}

func TestVersionURLs(t *testing.T) {
	if got, want := moduleVersionURL("rules_go", "0.50.0"), "/modules/rules_go/0.50.0"; got != want {
		t.Fatalf("moduleVersionURL = %q, want %q", got, want)
	}
	if got, want := codeNavRootURL("rules_go", "0.50.0"), "/modules/rules_go/0.50.0/code-nav/"; got != want {
		t.Fatalf("codeNavRootURL = %q, want %q", got, want)
	}
	if got, want := diffURL("rules_go", "0.49.0", "0.50.0"), "/modules/rules_go/diff?from=0.49.0&to=0.50.0"; got != want {
		t.Fatalf("diffURL = %q, want %q", got, want)
	}
}
