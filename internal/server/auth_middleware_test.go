package server

import (
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/albertocavalcante/canopy/internal/auth"
)

func mustCIDR(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, c, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestHeaderAuth_TrustedProxyInjectsIdentity(t *testing.T) {
	var got auth.Identity
	handler := headerAuth([]*net.IPNet{mustCIDR(t, "127.0.0.1/32")})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, ok := auth.FromContext(r.Context())
			if !ok {
				t.Fatal("expected identity in ctx")
			}
			got = id
			w.WriteHeader(http.StatusOK)
		}),
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(HeaderForwardedUser, "alice")
	req.Header.Set(HeaderForwardedEmail, "alice@example.com")
	req.Header.Set(HeaderForwardedGroups, "ops, admins")
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	want := auth.Identity{
		User:   "alice",
		Email:  "alice@example.com",
		Groups: []string{"ops", "admins"},
		Source: auth.SourceHeader,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("identity mismatch:\n got: %+v\nwant: %+v", got, want)
	}
}

func TestHeaderAuth_UntrustedSourceIgnoresHeaders(t *testing.T) {
	handler := headerAuth([]*net.IPNet{mustCIDR(t, "127.0.0.1/32")})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, ok := auth.FromContext(r.Context())
			if ok {
				t.Fatalf("untrusted source should not inject identity; got %+v", id)
			}
			w.WriteHeader(http.StatusOK)
		}),
	)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(HeaderForwardedUser, "spoofed")
	req.Header.Set(HeaderForwardedEmail, "evil@example.com")
	req.RemoteAddr = "203.0.113.42:31337" // not in 127.0.0.1/32
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
}

func TestHeaderAuth_EmptyCIDRListDisablesTrust(t *testing.T) {
	handler := headerAuth(nil)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := auth.FromContext(r.Context()); ok {
				t.Fatal("nil CIDRs should disable header trust")
			}
			w.WriteHeader(http.StatusOK)
		}),
	)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(HeaderForwardedUser, "would-have-been-trusted")
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
}

func TestHeaderAuth_MissingHeadersStayAnonymous(t *testing.T) {
	handler := headerAuth([]*net.IPNet{mustCIDR(t, "127.0.0.1/32")})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := auth.FromContext(r.Context()); ok {
				t.Fatal("no headers → no identity")
			}
		}),
	)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	handler.ServeHTTP(httptest.NewRecorder(), req)
}

func TestParseTrustedProxyCIDRs(t *testing.T) {
	cidrs, err := ParseTrustedProxyCIDRs(" 127.0.0.1/32 , 10.0.0.0/8 ")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(cidrs) != 2 {
		t.Fatalf("got %d cidrs, want 2", len(cidrs))
	}
}

func TestParseTrustedProxyCIDRs_EmptyOK(t *testing.T) {
	cidrs, err := ParseTrustedProxyCIDRs("")
	if err != nil {
		t.Fatalf("empty input should be valid: %v", err)
	}
	if len(cidrs) != 0 {
		t.Fatalf("expected empty slice, got %d", len(cidrs))
	}
}

func TestParseTrustedProxyCIDRs_BadInputErrors(t *testing.T) {
	if _, err := ParseTrustedProxyCIDRs("not-a-cidr"); err == nil {
		t.Fatal("expected parse error")
	}
}
