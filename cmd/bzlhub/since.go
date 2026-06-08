package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// parseSince accepts a Go-style duration ("1h", "30m", "168h") with
// one extension: "Nd" for days. time.ParseDuration rejects "d" so
// operators wanting "events from the last 7 days" otherwise have
// to do mental math on hours.
func parseSince(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	if strings.HasSuffix(s, "d") {
		nStr := strings.TrimSuffix(s, "d")
		n, err := strconv.Atoi(nStr)
		if err != nil {
			return 0, fmt.Errorf("--since %q: %w", s, err)
		}
		if n < 0 {
			return 0, fmt.Errorf("--since %q: negative duration", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("--since: %w", err)
	}
	if d < 0 {
		return 0, fmt.Errorf("--since %q: negative duration", s)
	}
	return d, nil
}
