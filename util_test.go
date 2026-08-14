package main

import "testing"

func TestFormatBytesCompact(t *testing.T) {
	const mib = 1024 * 1024
	const gib = 1024 * mib

	tests := []struct {
		bytes int64
		want  string
	}{
		{0, "0M"},
		{512 * mib, "512M"},
		{1023 * mib, "1023M"},
		{gib, "1.0G"},
		{gib + gib/2, "1.5G"},
		{4 * gib, "4.0G"},
		{-1, "0M"}, // negative inputs (shouldn't occur) clamp rather than underflow
	}
	for _, tc := range tests {
		if got := formatBytesCompact(tc.bytes); got != tc.want {
			t.Errorf("formatBytesCompact(%d) = %q, want %q", tc.bytes, got, tc.want)
		}
	}
}
