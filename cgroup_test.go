package main

import "testing"

func TestMeminfoInt(t *testing.T) {
	const meminfo = "MemTotal:       16384000 kB\nMemFree:         8192000 kB\n" +
		"MemAvailable:   12000000 kB\nSwapTotal:       2048000 kB\nSwapFree:        2048000 kB\n"

	tests := []struct {
		prefix  string
		wantVal int64
		wantOK  bool
	}{
		{"MemTotal:", 16384000, true},
		{"MemAvailable:", 12000000, true},
		{"SwapTotal:", 2048000, true},
		{"SwapFree:", 2048000, true},
		{"HugePages_Total:", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.prefix, func(t *testing.T) {
			gotVal, gotOK := meminfoInt(meminfo, tc.prefix)
			if gotOK != tc.wantOK || gotVal != tc.wantVal {
				t.Errorf("meminfoInt(_, %q) = (%d, %v), want (%d, %v)", tc.prefix, gotVal, gotOK, tc.wantVal, tc.wantOK)
			}
		})
	}
}

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
