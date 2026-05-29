package gobzlmod

import "errors"

// Sentinel errors for common registry failures.
var (
	// ErrModuleNotFound indicates the requested module does not exist in any registry.
	ErrModuleNotFound = errors.New("module not found")

	// ErrVersionNotFound indicates the requested version does not exist.
	ErrVersionNotFound = errors.New("version not found")

	// ErrRateLimited indicates the registry is rate limiting requests.
	ErrRateLimited = errors.New("rate limited")

	// ErrUnauthorized indicates authentication is required or failed.
	ErrUnauthorized = errors.New("unauthorized")
)
