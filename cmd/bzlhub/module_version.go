package main

func splitModuleVersion(s string) (name, version string, ok bool) {
	at := -1
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '@' {
			at = i
			break
		}
	}
	if at <= 0 || at == len(s)-1 {
		return "", "", false
	}
	return s[:at], s[at+1:], true
}
