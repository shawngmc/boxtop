package main

import "testing"

func TestSortDirtyFlag(t *testing.T) {
	s := newMonitorState()
	s.sortDirty = false

	// Selecting a new column marks the order stale and resets scroll to top.
	s.scrollOffset = 25
	s.setSortColumn(sortCPU)
	if !s.sortDirty {
		t.Error("setSortColumn(new column) did not set sortDirty")
	}
	if s.sortCol != sortCPU {
		t.Errorf("sortCol = %v, want sortCPU", s.sortCol)
	}
	if s.scrollOffset != 0 {
		t.Errorf("setSortColumn(new column) left scrollOffset = %d, want 0", s.scrollOffset)
	}

	// Re-selecting the active column flips direction, stays dirty, and keeps
	// the current scroll offset (only a column change resets it).
	s.sortDirty = false
	s.scrollOffset = 25
	prevReverse := s.sortReverse
	s.setSortColumn(sortCPU)
	if !s.sortDirty {
		t.Error("setSortColumn(active column) did not set sortDirty")
	}
	if s.sortReverse == prevReverse {
		t.Error("setSortColumn(active column) did not flip sortReverse")
	}
	if s.scrollOffset != 25 {
		t.Errorf("setSortColumn(active column) changed scrollOffset to %d, want 25 preserved", s.scrollOffset)
	}

	// 'r' toggles direction and marks stale.
	s.sortDirty = false
	prevReverse = s.sortReverse
	s.handleRuneKey('r')
	if !s.sortDirty {
		t.Error("handleRuneKey('r') did not set sortDirty")
	}
	if s.sortReverse == prevReverse {
		t.Error("handleRuneKey('r') did not flip sortReverse")
	}

	// Scrolling must NOT re-dirty the sort — that's the whole point of Tier 3.
	for _, scroll := range []func(){s.scrollUp, s.scrollDown, s.pageUp, s.pageDown, s.scrollHome, s.scrollEnd} {
		s.sortDirty = false
		scroll()
		if s.sortDirty {
			t.Error("a scroll operation set sortDirty; scroll redraws should reuse the sorted slice")
		}
	}
}

func pctPtr(v float64) *float64 { return &v }

func TestFilterQueryEditing(t *testing.T) {
	s := newMonitorState()
	s.scrollOffset = 25

	s.appendFilterRune('c')
	s.appendFilterRune('h')
	if s.filterQuery != "ch" {
		t.Errorf("filterQuery = %q, want %q", s.filterQuery, "ch")
	}
	if s.scrollOffset != 0 {
		t.Errorf("appendFilterRune left scrollOffset = %d, want 0", s.scrollOffset)
	}

	s.scrollOffset = 25
	s.filterBackspace()
	if s.filterQuery != "c" {
		t.Errorf("filterQuery after backspace = %q, want %q", s.filterQuery, "c")
	}
	if s.scrollOffset != 0 {
		t.Errorf("filterBackspace left scrollOffset = %d, want 0", s.scrollOffset)
	}

	// Backspace on an empty query is a no-op, not a panic.
	s.filterQuery = ""
	s.filterBackspace()
	if s.filterQuery != "" {
		t.Errorf("filterBackspace on empty query = %q, want empty", s.filterQuery)
	}

	s.filterQuery = "abc"
	s.scrollOffset = 25
	s.clearFilter()
	if s.filterQuery != "" {
		t.Errorf("clearFilter left filterQuery = %q, want empty", s.filterQuery)
	}
	if s.scrollOffset != 0 {
		t.Errorf("clearFilter left scrollOffset = %d, want 0", s.scrollOffset)
	}
}

func TestFilterProcesses(t *testing.T) {
	procs := []Process{
		{PID: 1, Name: "chrome", NameLower: "chrome", Cmd: "/usr/bin/chrome --flag"},
		{PID: 2, Name: "bash", NameLower: "bash", Cmd: "/bin/bash"},
		{PID: 3, Name: "sh", NameLower: "sh", Cmd: "/bin/sh -c chrome-helper"},
	}

	tests := []struct {
		name     string
		query    string
		wantPIDs []int
	}{
		{"matches by name, case-insensitive", "CHROME", []int{1, 3}}, // pid 3's cmd also contains "chrome"
		{"matches by command substring", "chrome-helper", []int{3}},
		{"matches multiple", "sh", []int{2, 3}}, // "bash" and "sh" both contain "sh"
		{"no match", "zzz", nil},
		{"empty query matches everything", "", []int{1, 2, 3}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := filterProcesses(procs, tc.query)
			gotPIDs := make([]int, len(got))
			for i, p := range got {
				gotPIDs[i] = p.PID
			}
			if len(gotPIDs) != len(tc.wantPIDs) {
				t.Fatalf("filterProcesses(%q) = %v, want %v", tc.query, gotPIDs, tc.wantPIDs)
			}
			for i := range tc.wantPIDs {
				if gotPIDs[i] != tc.wantPIDs[i] {
					t.Errorf("filterProcesses(%q) = %v, want %v", tc.query, gotPIDs, tc.wantPIDs)
					break
				}
			}
		})
	}
}

func TestSortProcesses(t *testing.T) {
	// Fresh list rebuilt per case since sortProcesses reorders in place.
	newList := func() []Process {
		return []Process{
			{PID: 3, RSSKb: 100, Name: "bbb", NameLower: "bbb", CPUPct: pctPtr(5)},
			{PID: 1, RSSKb: 300, Name: "AAA", NameLower: "aaa", CPUPct: nil},
			{PID: 2, RSSKb: 200, Name: "Ccc", NameLower: "ccc", CPUPct: pctPtr(50)},
		}
	}

	tests := []struct {
		name     string
		col      sortColumn
		reverse  bool
		wantPIDs []int
	}{
		{"RSS descending", sortRSS, true, []int{1, 2, 3}},
		{"RSS ascending", sortRSS, false, []int{3, 2, 1}},
		{"name ascending (case-insensitive)", sortName, false, []int{1, 3, 2}},
		{"name descending", sortName, true, []int{2, 3, 1}},
		{"CPU descending (nil sorts as 0)", sortCPU, true, []int{2, 3, 1}},
		{"PID ascending", sortPID, false, []int{1, 2, 3}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newMonitorState()
			s.sortCol = tc.col
			s.sortReverse = tc.reverse

			procs := newList()
			s.sortProcesses(procs)

			got := make([]int, len(procs))
			for i, p := range procs {
				got[i] = p.PID
			}
			for i := range tc.wantPIDs {
				if got[i] != tc.wantPIDs[i] {
					t.Errorf("order = %v, want %v", got, tc.wantPIDs)
					break
				}
			}
		})
	}
}
