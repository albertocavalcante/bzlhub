package paths

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func reqWithQuery(t *testing.T, q string) *http.Request {
	t.Helper()
	r := httptest.NewRequest("GET", "/?"+q, nil)
	return r
}

func TestQueryString(t *testing.T) {
	cases := []struct {
		name, query, want string
	}{
		{"absent", "", ""},
		{"present", "q=cc_library", "cc_library"},
		{"trims whitespace", "q=%20cc_library%20", "cc_library"},
		{"empty value", "q=", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := QueryString(reqWithQuery(t, c.query), "q"); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestQueryList(t *testing.T) {
	cases := []struct {
		name, query string
		want        []string
	}{
		{"absent", "", nil},
		{"single", "class=github-archive", []string{"github-archive"}},
		{"multiple", "class=github-archive,vendor-http", []string{"github-archive", "vendor-http"}},
		{"trims whitespace", "class=%20a%20,%20b%20", []string{"a", "b"}},
		{"drops empty entries", "class=a,,b,", []string{"a", "b"}},
		{"empty value", "class=", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := QueryList(reqWithQuery(t, c.query), "class")
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestQueryTristate(t *testing.T) {
	cases := []struct {
		name, query string
		want        Tristate
	}{
		{"absent", "", TristateUnset},
		{"only", "tainted=only", TristateOnly},
		{"exclude", "tainted=exclude", TristateExclude},
		{"invalid", "tainted=banana", TristateUnset},
		{"true (rejected)", "tainted=true", TristateUnset},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := QueryTristate(reqWithQuery(t, c.query), "tainted"); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestQueryBool(t *testing.T) {
	cases := []struct {
		name, query string
		want        bool
	}{
		{"absent", "", false},
		{"true", "recursive=true", true},
		{"bare", "recursive", true},
		{"false", "recursive=false", false},
		{"empty value", "recursive=", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := QueryBool(reqWithQuery(t, c.query), "recursive"); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestQueryInt(t *testing.T) {
	cases := []struct {
		name, query string
		defaultV    int
		want        int
	}{
		{"absent", "", 1, 1},
		{"valid", "page=5", 1, 5},
		{"invalid", "page=banana", 1, 1},
		{"negative", "page=-3", 1, -3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := QueryInt(reqWithQuery(t, c.query), "page", c.defaultV); got != c.want {
				t.Errorf("got %d, want %d", got, c.want)
			}
		})
	}
}
