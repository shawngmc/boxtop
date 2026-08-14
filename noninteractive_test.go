package main

import (
	"strings"
	"testing"
)

func TestWriteNonInteractiveFramePrintsFullListAndSimplifiedFooter(t *testing.T) {
	const n = 60 // deliberately more than any interactive viewport would show
	procs := make([]Process, n)
	for i := 0; i < n; i++ {
		name := "proc" + string(rune('A'+i%26))
		procs[i] = Process{PID: i + 1, Name: name, NameLower: strings.ToLower(name), Cmd: name}
	}

	state := newMonitorState()
	data := testFrameData(procs)
	data.totalRSSKb = 12345

	var buf strings.Builder
	writeNonInteractiveFrame(&buf, state, data, 100)
	out := buf.String()

	for _, p := range procs {
		if !strings.Contains(out, p.Name) {
			t.Errorf("output missing process %q — non-interactive mode should print the full list, not a truncated viewport", p.Name)
		}
	}

	if strings.Contains(out, "showing") {
		t.Error("output contains a scroll-truncation message; non-interactive mode should never truncate the process list")
	}

	for _, unwanted := range []string{"Ctrl+C", "Sort:", "Scroll:", "Mouse:", "[/]", "[x]", "[Enter]"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("output contains interactive control hint %q; the non-interactive footer should be simplified", unwanted)
		}
	}

	if !strings.Contains(out, "cgroup RAM") {
		t.Error("output missing cgroup RAM meter line")
	}
	if !strings.Contains(out, "system RAM") {
		t.Error("output missing system RAM meter line")
	}
	if !strings.Contains(out, "Processes: 60") {
		t.Errorf("output missing process-count footer line, got: %q", out)
	}
}

func TestWriteNonInteractiveFrameAppliesFilter(t *testing.T) {
	state := newMonitorState()
	state.filterQuery = "bash"
	data := testFrameData([]Process{
		{PID: 1, Name: "init", NameLower: "init", Cmd: "/sbin/init"},
		{PID: 2, Name: "bash", NameLower: "bash", Cmd: "/bin/bash"},
	})

	var buf strings.Builder
	writeNonInteractiveFrame(&buf, state, data, 100)
	out := buf.String()

	if strings.Contains(out, "init") {
		t.Error("filtered-out process \"init\" appeared in non-interactive output")
	}
	if !strings.Contains(out, "bash") {
		t.Error("matching process \"bash\" missing from non-interactive output")
	}
	if !strings.Contains(out, "Filter: bash") {
		t.Error("output missing the active filter line")
	}
}

func TestWriteNonInteractiveFrameOOMKills(t *testing.T) {
	state := newMonitorState()
	data := testFrameData(nil)
	data.haveOOMData = true
	data.oomKills = 3

	var buf strings.Builder
	writeNonInteractiveFrame(&buf, state, data, 100)
	if !strings.Contains(buf.String(), "OOM Kills: 3") {
		t.Error("output missing OOM kill count")
	}
}

func TestPlainBar(t *testing.T) {
	tests := []struct {
		frac float64
		want string
	}{
		{0, "░░░░"},
		{1, "████"},
		{0.5, "██░░"},
		{2, "████"}, // over 100% clamps rather than overflowing
	}
	for _, tc := range tests {
		if got := plainBar(4, tc.frac); got != tc.want {
			t.Errorf("plainBar(4, %v) = %q, want %q", tc.frac, got, tc.want)
		}
	}
}
