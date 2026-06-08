package admit

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newFetcherFor(t *testing.T, maxBytes int64) *HTTPFetcher {
	t.Helper()
	return NewHTTPFetcher(http.DefaultClient, maxBytes)
}

func TestHTTPFetcher_HappyPath(t *testing.T) {
	body := []byte("hello-from-upstream")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	var sink bytes.Buffer
	n, err := newFetcherFor(t, 1024).Fetch(context.Background(), srv.URL, &sink)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(body)) {
		t.Errorf("n = %d, want %d", n, len(body))
	}
	if sink.String() != string(body) {
		t.Errorf("sink = %q, want %q", sink.String(), body)
	}
}

func TestHTTPFetcher_RespectsSizeCap(t *testing.T) {
	body := strings.Repeat("a", 2048) // 2 KiB
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	var sink bytes.Buffer
	_, err := newFetcherFor(t, 1024).Fetch(context.Background(), srv.URL, &sink)
	if err == nil {
		t.Fatal("want oversize error")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("err = %q, want 'too large'", err)
	}
}

func TestHTTPFetcher_404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer srv.Close()

	var sink bytes.Buffer
	_, err := newFetcherFor(t, 1024).Fetch(context.Background(), srv.URL, &sink)
	if err == nil {
		t.Fatal("want HTTP error")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("err = %q, want '404'", err)
	}
}

func TestHTTPFetcher_5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	var sink bytes.Buffer
	_, err := newFetcherFor(t, 1024).Fetch(context.Background(), srv.URL, &sink)
	if err == nil {
		t.Fatal("want HTTP error")
	}
}

func TestHTTPFetcher_NetworkError(t *testing.T) {
	// Unrouted localhost port (closed) — connection refused.
	var sink bytes.Buffer
	_, err := newFetcherFor(t, 1024).Fetch(context.Background(),
		"http://127.0.0.1:1/no-such-server", &sink)
	if err == nil {
		t.Fatal("want network error")
	}
}

func TestHTTPFetcher_CtxCancel(t *testing.T) {
	// Server hangs forever; ctx cancel should abort the read.
	hang := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-hang
	}))
	defer close(hang)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before request

	var sink bytes.Buffer
	_, err := newFetcherFor(t, 1024).Fetch(ctx, srv.URL, &sink)
	if err == nil {
		t.Fatal("want cancellation error")
	}
}

// Helper-import sentinel — keeps the test file's import block honest.
var _ = errors.Is
