package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeJSONBody(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}
	tests := []struct {
		name       string
		body       string
		maxBytes   int64
		wantOK     bool
		wantStatus int
	}{
		{
			name:     "valid",
			body:     `{"name":"bzlhub"}`,
			maxBytes: 64,
			wantOK:   true,
		},
		{
			name:       "unknown field",
			body:       `{"name":"bzlhub","ignored":true}`,
			maxBytes:   64,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "trailing value",
			body:       `{"name":"bzlhub"} {"name":"second"}`,
			maxBytes:   64,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "too large",
			body:       `{"name":"this value exceeds the deliberately tiny test limit"}`,
			maxBytes:   16,
			wantStatus: http.StatusRequestEntityTooLarge,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			var got payload
			ok := decodeJSONBody(rec, req, tc.maxBytes, &got)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v; response=%s", ok, tc.wantOK, rec.Body.String())
			}
			if !tc.wantOK && rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d; response=%s",
					rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}
