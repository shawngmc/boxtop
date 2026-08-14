package main

import (
	"errors"
	"syscall"
	"testing"
)

func TestSortDirtyFlag(t *testing.T) {
	s := newMonitorState()
	s.sortDirty = false

	// Selecting a new column marks the order stale and resets scroll/cursor
	// to top.
	s.scrollOffset = 25
	s.cursor = 25
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
	if s.cursor != 0 {
		t.Errorf("setSortColumn(new column) left cursor = %d, want 0", s.cursor)
	}

	// Re-selecting the active column flips direction, stays dirty, and keeps
	// the current scroll offset and cursor (only a column change resets them).
	s.sortDirty = false
	s.scrollOffset = 25
	s.cursor = 25
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
	if s.cursor != 25 {
		t.Errorf("setSortColumn(active column) changed cursor to %d, want 25 preserved", s.cursor)
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

	// Cursor movement must NOT re-dirty the sort — that's the whole point of Tier 3.
	for _, move := range []func(){s.cursorUp, s.cursorDown, s.cursorPageUp, s.cursorPageDown, s.cursorHome, s.cursorEnd} {
		s.sortDirty = false
		move()
		if s.sortDirty {
			t.Error("a cursor-movement operation set sortDirty; scroll redraws should reuse the sorted slice")
		}
	}
}

func pctPtr(v float64) *float64 { return &v }

func TestFilterQueryEditing(t *testing.T) {
	s := newMonitorState()
	s.scrollOffset = 25
	s.cursor = 25

	s.appendFilterRune('c')
	s.appendFilterRune('h')
	if s.filterQuery != "ch" {
		t.Errorf("filterQuery = %q, want %q", s.filterQuery, "ch")
	}
	if s.scrollOffset != 0 {
		t.Errorf("appendFilterRune left scrollOffset = %d, want 0", s.scrollOffset)
	}
	if s.cursor != 0 {
		t.Errorf("appendFilterRune left cursor = %d, want 0", s.cursor)
	}

	s.scrollOffset = 25
	s.cursor = 25
	s.filterBackspace()
	if s.filterQuery != "c" {
		t.Errorf("filterQuery after backspace = %q, want %q", s.filterQuery, "c")
	}
	if s.scrollOffset != 0 {
		t.Errorf("filterBackspace left scrollOffset = %d, want 0", s.scrollOffset)
	}
	if s.cursor != 0 {
		t.Errorf("filterBackspace left cursor = %d, want 0", s.cursor)
	}

	// Backspace on an empty query is a no-op, not a panic.
	s.filterQuery = ""
	s.filterBackspace()
	if s.filterQuery != "" {
		t.Errorf("filterBackspace on empty query = %q, want empty", s.filterQuery)
	}

	s.filterQuery = "abc"
	s.scrollOffset = 25
	s.cursor = 25
	s.clearFilter()
	if s.filterQuery != "" {
		t.Errorf("clearFilter left filterQuery = %q, want empty", s.filterQuery)
	}
	if s.scrollOffset != 0 {
		t.Errorf("clearFilter left scrollOffset = %d, want 0", s.scrollOffset)
	}
	if s.cursor != 0 {
		t.Errorf("clearFilter left cursor = %d, want 0", s.cursor)
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

func TestCursorMovementAndClamping(t *testing.T) {
	s := newMonitorState()
	s.lastPageSize = 5
	const total = 8

	s.cursorDown()
	s.clampCursor(total)
	if s.cursor != 1 {
		t.Errorf("cursorDown from 0 = %d, want 1", s.cursor)
	}

	s.cursor = 0
	s.cursorUp()
	s.clampCursor(total)
	if s.cursor != 0 {
		t.Errorf("cursorUp at 0 = %d, want 0 (floor)", s.cursor)
	}

	s.cursorEnd()
	s.clampCursor(total)
	if s.cursor != total-1 {
		t.Errorf("cursorEnd clamped = %d, want %d", s.cursor, total-1)
	}

	s.cursorHome()
	s.clampCursor(total)
	if s.cursor != 0 {
		t.Errorf("cursorHome = %d, want 0", s.cursor)
	}

	// First page-down (0 -> 5) stays within range; the second (5 -> 10) runs
	// past the end and clampCursor pulls it back to total-1.
	s.cursorPageDown()
	s.clampCursor(total)
	if s.cursor != 5 {
		t.Errorf("cursorPageDown from 0 = %d, want 5", s.cursor)
	}
	s.cursorPageDown()
	s.clampCursor(total)
	if s.cursor != total-1 {
		t.Errorf("cursorPageDown past the end, clamped = %d, want %d", s.cursor, total-1)
	}

	s.cursorPageUp()
	s.clampCursor(total)
	if s.cursor != 2 {
		t.Errorf("cursorPageUp from %d = %d, want 2", total-1, s.cursor)
	}
}

func TestClampCursorAfterListShrinks(t *testing.T) {
	s := newMonitorState()

	s.cursor = 9
	s.clampCursor(3)
	if s.cursor != 2 {
		t.Errorf("clampCursor(3) with cursor=9 = %d, want 2", s.cursor)
	}

	s.cursor = 5
	s.clampCursor(0)
	if s.cursor != 0 {
		t.Errorf("clampCursor(0) with cursor=5 = %d, want 0", s.cursor)
	}
}

func TestSyncScrollToCursor(t *testing.T) {
	tests := []struct {
		name         string
		cursor       int
		scrollOffset int
		maxRows      int
		want         int
	}{
		{"cursor above window scrolls up to it", 2, 10, 5, 2},
		{"cursor below window scrolls down to it", 20, 0, 5, 16},
		{"cursor already inside window is a no-op", 3, 0, 5, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newMonitorState()
			s.cursor = tc.cursor
			s.scrollOffset = tc.scrollOffset
			s.syncScrollToCursor(tc.maxRows)
			if s.scrollOffset != tc.want {
				t.Errorf("scrollOffset = %d, want %d", s.scrollOffset, tc.want)
			}
		})
	}
}

func TestScrollByClampsCursorIntoView(t *testing.T) {
	s := newMonitorState()
	s.lastPageSize = 5
	s.cursor = 50
	s.scrollOffset = 40

	s.scrollBy(-30) // scrollOffset -> 10, viewport [10,14]
	if s.scrollOffset != 10 {
		t.Fatalf("scrollOffset = %d, want 10", s.scrollOffset)
	}
	if s.cursor != 14 {
		t.Errorf("cursor after scrollBy = %d, want 14 (pulled to bottom of new viewport)", s.cursor)
	}

	s.cursor = 0
	s.scrollBy(5) // scrollOffset -> 15, viewport [15,19]
	if s.cursor != 15 {
		t.Errorf("cursor after scrollBy = %d, want 15 (pulled to top of new viewport)", s.cursor)
	}
}

func TestStartKillConfirm(t *testing.T) {
	s := newMonitorState()
	s.currentProcs = []Process{
		{PID: 1, Name: "init"},
		{PID: 2, Name: "bash"},
		{PID: 3, Name: "sleep"},
	}
	s.cursor = 2

	if !s.startKillConfirm() {
		t.Fatal("startKillConfirm() = false, want true")
	}
	if !s.killConfirmMode {
		t.Error("startKillConfirm did not set killConfirmMode")
	}
	if s.killTargetPID != 3 || s.killTargetName != "sleep" {
		t.Errorf("killTarget = (%d, %q), want (3, \"sleep\")", s.killTargetPID, s.killTargetName)
	}

	s2 := newMonitorState()
	if s2.startKillConfirm() {
		t.Error("startKillConfirm() on empty currentProcs = true, want false")
	}
	if s2.killConfirmMode {
		t.Error("startKillConfirm set killConfirmMode with an empty process list")
	}

	s3 := newMonitorState()
	s3.currentProcs = []Process{{PID: 1, Name: "init"}}
	s3.cursor = 5
	if s3.startKillConfirm() {
		t.Error("startKillConfirm() with out-of-range cursor = true, want false")
	}
}

func TestRowAt(t *testing.T) {
	s := newMonitorState()
	s.scrollOffset = 5
	s.tableTop = 10
	s.tableRowCount = 3 // rows at screen y = 10, 11, 12

	tests := []struct {
		y       int
		wantIdx int
		wantOK  bool
	}{
		{9, 0, false},  // above the table
		{10, 5, true},  // first visible row -> scrollOffset + 0
		{12, 7, true},  // last visible row -> scrollOffset + 2
		{13, 0, false}, // below the table
	}
	for _, tc := range tests {
		idx, ok := s.rowAt(tc.y)
		if ok != tc.wantOK || (ok && idx != tc.wantIdx) {
			t.Errorf("rowAt(%d) = (%d, %v), want (%d, %v)", tc.y, idx, ok, tc.wantIdx, tc.wantOK)
		}
	}
}

func TestKillResultMessage(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"success", nil, "Sent SIGTERM to PID 42 (sleep)"},
		{"already gone", syscall.ESRCH, "PID 42 (sleep) no longer exists"},
		{"not permitted", syscall.EPERM, "Permission denied signaling PID 42 (sleep)"},
		{"other error", errors.New("boom"), "Failed to signal PID 42 (sleep): boom"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := killResultMessage(42, "sleep", "SIGTERM", tc.err)
			if got != tc.want {
				t.Errorf("killResultMessage = %q, want %q", got, tc.want)
			}
		})
	}
}
