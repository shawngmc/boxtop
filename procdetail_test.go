package main

import (
	"os"
	"testing"
)

func TestParseVmKb(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"12345 kB", 12345},
		{"0 kB", 0},
		{"", 0},
		{"garbage", 0},
	}
	for _, tc := range tests {
		if got := parseVmKb(tc.in); got != tc.want {
			t.Errorf("parseVmKb(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestResolveUser(t *testing.T) {
	// uid 0 is root on every Linux system this runs on.
	if got := resolveUser("0\t0\t0\t0"); got != "root (uid 0)" {
		t.Errorf("resolveUser(root uid) = %q, want %q", got, "root (uid 0)")
	}
	// An implausibly large uid should have no /etc/passwd entry, exercising
	// the fallback path.
	if got := resolveUser("4000000000"); got != "uid 4000000000" {
		t.Errorf("resolveUser(unknown uid) = %q, want %q", got, "uid 4000000000")
	}
	if got := resolveUser(""); got != "" {
		t.Errorf("resolveUser(empty) = %q, want empty", got)
	}
}

func TestParseStatNice(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		wantNice int
		wantOK   bool
	}{
		// Fields after ')': state ppid pgrp session tty_nr tpgid flags minflt
		// cminflt majflt cmajflt utime stime cutime cstime priority nice ...
		// — nice is the 17th token (index 16), here -5.
		{"normal", "1234 (bash) S 1 1234 1234 34816 1234 4194304 100 0 200 0 1500 300 40 20 20 -5 1 0 987654 4194304 600 0", -5, true},
		{"too few fields", "42 (bash) S 1 2 3", 0, false},
		{"no parens", "42 bash S 1 2 3", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotNice, gotOK := parseStatNice(tc.raw)
			if gotOK != tc.wantOK {
				t.Errorf("ok = %v, want %v", gotOK, tc.wantOK)
			}
			if gotOK && gotNice != tc.wantNice {
				t.Errorf("nice = %d, want %d", gotNice, tc.wantNice)
			}
		})
	}
}

// Smoke test against the live /proc of the test process itself.
func TestBuildProcessDetailLive(t *testing.T) {
	self := os.Getpid()
	p := Process{PID: self, Name: "self", Cmd: "self-cmd", RSSKb: 123}

	d := buildProcessDetail(p)
	if d.PID != self || d.Name != "self" || d.Cmd != "self-cmd" || d.RSSKb != 123 {
		t.Errorf("buildProcessDetail did not preserve table fields: %+v", d)
	}
	if !d.HaveExtra {
		t.Fatal("buildProcessDetail(self) HaveExtra = false, want true (status should be readable)")
	}
	if d.State == "" {
		t.Error("State is empty")
	}
	if d.Threads < 1 {
		t.Errorf("Threads = %d, want >= 1", d.Threads)
	}
	if d.User == "" {
		t.Error("User is empty")
	}
	if d.VmSizeKb <= 0 {
		t.Errorf("VmSizeKb = %d, want > 0", d.VmSizeKb)
	}
}

func TestParseProcStatusUnreadablePid(t *testing.T) {
	if _, ok := parseProcStatus(-1); ok {
		t.Error("parseProcStatus(-1) ok = true, want false")
	}
}
