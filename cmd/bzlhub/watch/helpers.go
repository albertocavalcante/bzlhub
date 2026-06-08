package watch

// firstNonEmpty returns the first non-empty string from ss, or "".
func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// short truncates a string (typically a git SHA) to the first 12
// characters for log readability. Returns s verbatim if shorter.
func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
