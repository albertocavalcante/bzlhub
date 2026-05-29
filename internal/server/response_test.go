package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteJSONWithETag(t *testing.T) {
	body := map[string]string{"module": "rules_go"}

	first := httptest.NewRecorder()
	writeJSONWithETag(first, httptest.NewRequest(http.MethodGet, "/api/test", nil), http.StatusOK, body)
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, want %d", first.Code, http.StatusOK)
	}
	if got := first.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type = %q, want application/json", got)
	}
	if got := first.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("cache-control = %q, want no-cache", got)
	}
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}

	secondReq := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	secondReq.Header.Set("If-None-Match", etag)
	second := httptest.NewRecorder()
	writeJSONWithETag(second, secondReq, http.StatusOK, body)
	if second.Code != http.StatusNotModified {
		t.Fatalf("second status = %d, want %d", second.Code, http.StatusNotModified)
	}
	if second.Body.Len() != 0 {
		t.Fatalf("304 body length = %d, want 0", second.Body.Len())
	}
}
