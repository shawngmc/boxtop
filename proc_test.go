package main

import (
	"os"
	"strings"
	"testing"
)

// A realistic /proc/<pid>/stat line. Fields after the last ')' are, in
// order: state ppid pgrp session tty_nr tpgid flags minflt cminflt majflt
// cmajflt utime stime ... — so utime is the 12th token here (1500) and
// stime the 13th (300), giving (1500+300)/clkTck = 18.0 CPU seconds.
const sampleStatBash = "1234 (bash) S 1 1234 1234 34816 1234 4194304 100 0 200 0 1500 300 40 20 20 0 1 0 987654 4194304 600 18446744073709551615\n"

func TestParseStatNameCPU(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		wantName string
		wantCPU  float64
		wantRSS  int64
		wantOK   bool
	}{
		// sampleStatBash's rss field (field 24) is 600 pages.
		{"normal", sampleStatBash, "bash", 18.0, 600 * int64(pageSizeKB), true},
		{"comm with spaces", "42 (foo bar) S 1 1 1 0 0 0 0 0 0 0 500 100 0 0 0 0 0 0 0 0 0 0", "foo bar", 6.0, 0, true},
		{"comm with parens", "42 (a (b) c) S 1 1 1 0 0 0 0 0 0 0 100 0 0 0 0 0 0 0 0 0 0 0", "a (b) c", 1.0, 0, true},
		{"empty comm", "42 () S 1 1 1 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0", "?", 0.0, 0, true},
		{"control char in comm sanitized", "42 (a\tb) S 1 1 1 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0", "a b", 0.0, 0, true},
		{"rss field parsed", "42 (x) S 1 1 1 0 0 0 0 0 0 0 10 20 0 0 0 0 0 0 0 0 700 0", "x", 0.3, 700 * int64(pageSizeKB), true},
		{"no parens", "42 bash S 1 2 3", "", 0.0, 0, false},
		{"too few fields after comm", "42 (bash) S 1 2 3", "bash", 0.0, 0, false},
		{"non-numeric utime", "42 (bash) S 1 1 1 0 0 0 0 0 0 0 x 300 0 0 0 0 0 0 0 0 0 0", "bash", 0.0, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotName, gotCPU, gotRSS, gotOK := parseStatNameCPU(tc.raw)
			if gotName != tc.wantName {
				t.Errorf("name = %q, want %q", gotName, tc.wantName)
			}
			if gotOK != tc.wantOK {
				t.Errorf("ok = %v, want %v", gotOK, tc.wantOK)
			}
			if gotOK && gotCPU != tc.wantCPU {
				t.Errorf("cpuSecs = %v, want %v", gotCPU, tc.wantCPU)
			}
			if gotOK && gotRSS != tc.wantRSS {
				t.Errorf("rssKb = %d, want %d", gotRSS, tc.wantRSS)
			}
		})
	}
}

func TestParseCmdline(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		comm string
		want string
	}{
		{"nul separated args", []byte("/usr/bin/foo\x00--flag\x00arg\x00"), "foo", "/usr/bin/foo --flag arg"},
		{"trailing nuls trimmed", []byte("prog\x00\x00"), "prog", "prog"},
		{"empty falls back to bracketed name", nil, "bash", "[bash]"},
		{"empty with unknown name", nil, "?", "[unknown]"},
		{"control char sanitized", []byte("a\tb\x00c"), "x", "a b c"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseCmdline(tc.data, tc.comm); got != tc.want {
				t.Errorf("parseCmdline(%q, %q) = %q, want %q", tc.data, tc.comm, got, tc.want)
			}
		})
	}
}

func TestBracketName(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"bash", "[bash]"},
		{"", "[unknown]"},
		{"?", "[unknown]"},
	}
	for _, tc := range tests {
		if got := bracketName(tc.in); got != tc.want {
			t.Errorf("bracketName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// cmdFor must return a cached value without touching /proc. Passing a pid
// that cannot exist proves the cache short-circuits the file read.
func TestCmdForUsesCache(t *testing.T) {
	cache := map[int]string{-1: "cached-cmd"}
	if got := cmdFor(-1, "ignored", cache); got != "cached-cmd" {
		t.Errorf("cmdFor cache hit = %q, want %q", got, "cached-cmd")
	}
}

// Smoke test against the live /proc of the test process itself: the readers
// and buildProcesses must parse real kernel output, and the cmdline cache
// must be populated for the pids seen.
func TestBuildProcessesLive(t *testing.T) {
	self := os.Getpid()

	if name, _, rss, ok := readStatNameCPU(self); !ok || name == "" || rss <= 0 {
		t.Fatalf("readStatNameCPU(self) = (%q, _, %d, %v), want a name, positive rss, and ok", name, rss, ok)
	}

	state := newMonitorState()

	procs := buildProcesses(state, 1.0)
	if len(procs) == 0 {
		t.Fatal("buildProcesses returned no processes")
	}

	found := false
	for _, p := range procs {
		if p.PID == self {
			found = true
			if p.Name == "" || p.Cmd == "" {
				t.Errorf("self process has empty Name/Cmd: %+v", p)
			}
			if p.NameLower != strings.ToLower(p.Name) {
				t.Errorf("NameLower = %q, want lowercased %q", p.NameLower, p.Name)
			}
		}
	}
	if !found {
		t.Errorf("buildProcesses did not include self pid %d", self)
	}
	if _, ok := state.cmdCache[self]; !ok {
		t.Errorf("cmdCache not populated for self pid %d", self)
	}
	if !state.procSeen[self] {
		t.Errorf("procSeen not populated for self pid %d", self)
	}

	// A second pass with the same state should reuse the baseline/cmd caches
	// and the cleared procSeen scratch set.
	procs2 := buildProcesses(state, 1.0)
	if len(procs2) == 0 {
		t.Fatal("second buildProcesses returned no processes")
	}
}
