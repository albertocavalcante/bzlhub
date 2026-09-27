package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

const (
	maxActionJSONBody     int64 = 64 * 1024
	maxRequestJSONBody    int64 = 256 * 1024
	maxTransitionJSONBody int64 = 64 * 1024
	maxMaintainerJSONBody int64 = 16 * 1024
)

// decodeJSONBody strictly decodes exactly one bounded JSON value.
// It rejects unknown fields and trailing values so callers cannot
// smuggle ignored data past the audit and validation layers.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, maxBytes int64, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	defer r.Body.Close()

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeJSONDecodeError(w, err)
		return false
	}

	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			http.Error(w, "bad request: body must contain exactly one JSON value", http.StatusBadRequest)
		} else {
			writeJSONDecodeError(w, err)
		}
		return false
	}
	return true
}

func writeJSONDecodeError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
}
