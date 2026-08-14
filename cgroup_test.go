package main

import "testing"

func TestFirstLineWithPrefix(t *testing.T) {
	const meminfo = "MemTotal:       16384000 kB\nMemFree:         8192000 kB\nMemAvailable:   12000000 kB\n"

	tests := []struct {
		name     string
		s        string
		prefix   string
		wantLine string
		wantOK   bool
	}{
		{"first line", meminfo, "MemTotal:", "MemTotal:       16384000 kB", true},
		{"middle line", meminfo, "MemFree:", "MemFree:         8192000 kB", true},
		{"last line", meminfo, "MemAvailable:", "MemAvailable:   12000000 kB", true},
		{"no match", meminfo, "SwapTotal:", "", false},
		{"no trailing newline", "usage_usec 42", "usage_usec", "usage_usec 42", true},
		{"empty input", "", "MemTotal:", "", false},
		{"prefix is not a whole-line anchor", "xMemTotal: 1\n", "MemTotal:", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotLine, gotOK := firstLineWithPrefix(tc.s, tc.prefix)
			if gotOK != tc.wantOK {
				t.Errorf("ok = %v, want %v", gotOK, tc.wantOK)
			}
			if gotLine != tc.wantLine {
				t.Errorf("line = %q, want %q", gotLine, tc.wantLine)
			}
		})
	}
}
