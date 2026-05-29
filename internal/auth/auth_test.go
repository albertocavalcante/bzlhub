package auth

import (
	"context"
	"reflect"
	"testing"
)

func TestAnonymous_DefaultIdentity(t *testing.T) {
	a := Anonymous()
	if a.Source != SourceAnonymous {
		t.Fatalf("Source = %q, want %q", a.Source, SourceAnonymous)
	}
	if a.IsAuthenticated() {
		t.Fatal("anonymous identity should not be authenticated")
	}
	if a.DisplayName() != "" {
		t.Fatalf("DisplayName = %q, want empty", a.DisplayName())
	}
}

func TestIsAuthenticated_PerSource(t *testing.T) {
	cases := []struct {
		src  Source
		want bool
	}{
		{"", false},
		{SourceAnonymous, false},
		{SourceHeader, true},
		{SourceBearer, true},
		{SourceOIDC, true},
	}
	for _, tc := range cases {
		got := Identity{Source: tc.src}.IsAuthenticated()
		if got != tc.want {
			t.Errorf("Source=%q IsAuthenticated()=%v, want %v", tc.src, got, tc.want)
		}
	}
}

func TestDisplayName_EmailWinsOverUser(t *testing.T) {
	id := Identity{User: "alice", Email: "alice@example.com", Source: SourceHeader}
	if got := id.DisplayName(); got != "alice@example.com" {
		t.Fatalf("DisplayName = %q, want email", got)
	}
}

func TestDisplayName_FallsBackToUser(t *testing.T) {
	id := Identity{User: "alice", Source: SourceHeader}
	if got := id.DisplayName(); got != "alice" {
		t.Fatalf("DisplayName = %q, want alice", got)
	}
}

func TestContext_RoundTrip(t *testing.T) {
	id := Identity{User: "bob", Email: "bob@example.com", Source: SourceHeader}
	ctx := WithContext(context.Background(), id)
	got, ok := FromContext(ctx)
	if !ok {
		t.Fatal("FromContext should report ok when set")
	}
	if !reflect.DeepEqual(got, id) {
		t.Fatalf("round-trip mismatch: %+v vs %+v", got, id)
	}
}

func TestFromContext_NoIdentityYieldsAnonymous(t *testing.T) {
	got, ok := FromContext(context.Background())
	if ok {
		t.Fatal("FromContext should report !ok on a bare ctx")
	}
	if !reflect.DeepEqual(got, Anonymous()) {
		t.Fatalf("got %+v, want Anonymous()", got)
	}
}

func TestFromContext_IgnoresWrongTypeValue(t *testing.T) {
	type otherKey struct{}
	ctx := context.WithValue(context.Background(), otherKey{}, "not-an-identity")
	got, ok := FromContext(ctx)
	if ok {
		t.Fatal("should not find an Identity under a different key")
	}
	if !reflect.DeepEqual(got, Anonymous()) {
		t.Fatalf("got %+v, want Anonymous()", got)
	}
}
