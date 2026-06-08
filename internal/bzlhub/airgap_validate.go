package bzlhub

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// validateBazelrcURL guards the strings that get templated into
// canopy's .bazelrc / downloader-config artifacts. Both files are
// downloaded by operators and consumed by Bazel; an attacker who can
// control a query-string value should not be able to inject an
// additional `common --foo=...` directive (via embedded newlines) or
// comment-out the canopy directive (via `#`).
//
// Rules:
//   - control characters (anything < 0x20 plus DEL) are rejected
//     outright — these would break the line-oriented .bazelrc syntax;
//   - the value must parse as a URL with an http or https scheme.
//
// Returns the trimmed value on success. Callers that want a "leave
// empty → apply default" semantic should check for "" before calling
// this function (or after, since an empty input returns "" + nil
// error from the strings.TrimSpace then propagates through url.Parse
// — see the explicit empty short-circuit below).
func validateBazelrcURL(field, raw string) (string, error) {
	// Trim ASCII space only — strings.TrimSpace would strip \r/\n/\t
	// as whitespace and silently bury an injection at the boundary.
	v := strings.Trim(raw, " ")
	if v == "" {
		return "", nil
	}
	for i, r := range v {
		if r < 0x20 || r == 0x7F {
			return "", fmt.Errorf("%s: control character at offset %d", field, i)
		}
	}
	u, err := url.Parse(v)
	if err != nil {
		return "", fmt.Errorf("%s: %w", field, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("%s: scheme must be http or https, got %q", field, u.Scheme)
	}
	return v, nil
}

// ErrInvalidBazelrcURL is returned by airgap emitters when a
// caller-supplied URL fails validation.
var ErrInvalidBazelrcURL = errors.New("invalid bazelrc URL")
