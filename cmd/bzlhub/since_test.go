package main

import (
	"testing"
	"time"
)

func TestParseSince(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"", 0, false},
		{"1h", time.Hour, false},
		{"30m", 30 * time.Minute, false},
		{"168h", 168 * time.Hour, false},
		{"1d", 24 * time.Hour, false},
		{"7d", 7 * 24 * time.Hour, false},
		{"30d", 30 * 24 * time.Hour, false},
		{"0d", 0, false},
		{"-1h", 0, true},
		{"-1d", 0, true},
		{"not-a-duration", 0, true},
		{"d", 0, true},
		{"5x", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseSince(tc.in)
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v; wantErr = %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("got = %v; want %v", got, tc.want)
			}
		})
	}
}
