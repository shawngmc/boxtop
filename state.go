package main

import (
	"fmt"
	"sort"
	"strings"
	"syscall"
	"time"
)

type sortColumn int

const (
	sortRSS sortColumn = iota
	sortCPU
	sortPID
	sortName
)

// sortColumnKeys ports SORT_COLUMN_KEYS: one key per column.
var sortColumnKeys = map[rune]sortColumn{
	'm': sortRSS,
	'c': sortCPU,
	'p': sortPID,
	'n': sortName,
}

// sortDefaultReverse ports SORT_DEFAULT_REVERSE: numeric columns default
// descending (biggest offender first), name defaults ascending.
var sortDefaultReverse = map[sortColumn]bool{
	sortRSS:  true,
	sortCPU:  true,
	sortPID:  false,
	sortName: false,
}

// monitorState replaces the Python version's module-level globals
// (sort_column, sort_reverse, scroll_offset, _last_page_size,
// _proc_cpu_prev, _cpu_prev_usage_usec, _cpu_prev_time) with an explicit
// struct — same mutable state, just not package-global, so a future
// multi-instance or test scenario isn't fighting shared globals.
type monitorState struct {
	sortCol      sortColumn
	sortReverse  bool
	scrollOffset int
	lastPageSize int

	// sortDirty is set whenever the sort order becomes stale — a fresh poll
	// produced a new unsorted process slice, or the sort column/direction
	// changed. drawFrame re-sorts only when it's set, so scroll/page/home/end
	// redraws (which don't change order) skip the O(n log n) sort entirely.
	sortDirty bool

	procCPUPrev map[int]cpuSample // per-pid CPU sampling baseline
	cmdCache    map[int]string    // per-pid COMMAND string, read once per process lifetime
	procSeen    map[int]bool      // scratch set of pids seen this tick, cleared+reused each frame
	readBuf     []byte            // scratch buffer for raw /proc reads, reused across pids and ticks

	// filterMode is true while the incremental filter's text input has
	// focus (opened via '/', ported from htop's F4 filter bar). filterQuery
	// persists after the input is closed (Enter) so re-opening it resumes
	// editing the same text, and is cleared on Esc.
	filterMode  bool
	filterQuery string

	// cursor is the highlighted row's absolute index into currentProcs (the
	// current sorted+filtered process list) — a real selection, independent
	// of scrollOffset (the viewport's top row). Up/Down/j/k/PgUp/PgDn/
	// Home/End move this; scrollOffset auto-follows via syncScrollToCursor.
	cursor int

	// currentProcs is the sorted+filtered process slice from the most
	// recent drawFrame call, recorded so the kill-confirm key ('x') and
	// cursor clamping can resolve "which process is under the cursor"
	// without re-deriving sort/filter order outside of drawFrame.
	currentProcs []Process

	// killConfirmMode is true while the kill-confirmation prompt (opened
	// via 'x') has focus — mirrors filterMode. killTargetPID/
	// killTargetName snapshot the process it's asking about, captured once
	// when 'x' was pressed, so a poll tick mid-confirm can't retarget a
	// different process out from under the prompt.
	killConfirmMode bool
	killTargetPID   int
	killTargetName  string

	// killStatusMsg is the one-line result of the last kill attempt, shown
	// in place of the footer help line until the next keypress clears it.
	killStatusMsg string

	cgroupCPUPrevUsageUsec int64
	cgroupCPUPrevTime      time.Time
	cgroupCPUHasBaseline   bool

	// Header click targets, recorded by drawFrame each frame so mouse clicks
	// map to the same columns that were actually rendered (rather than
	// duplicating the layout math in the event handler).
	headerRow    int
	sortHitboxes []sortHitbox

	// Process row click targets, recorded by drawFrame each frame alongside
	// the header hitboxes above — tableTop is the screen row of the first
	// drawn process, tableRowCount how many rows were actually drawn, so a
	// click can map back to an index into currentProcs via scrollOffset.
	tableTop      int
	tableRowCount int
}

// sortHitbox is the half-open column span [x0, x1) on headerRow that selects
// a sort column when clicked.
type sortHitbox struct {
	x0, x1 int
	col    sortColumn
}

func newMonitorState() *monitorState {
	return &monitorState{
		sortCol:      sortRSS,
		sortReverse:  true,
		lastPageSize: 10,
		procCPUPrev:  make(map[int]cpuSample),
		cmdCache:     make(map[int]string),
		procSeen:     make(map[int]bool),
		readBuf:      make([]byte, 4096), // one page-ish; grows only for rare large cmdlines
	}
}

// handleKey ports handle_key(): updates cursor/scroll state for a single
// keypress. Arrow/page/home/end come in pre-decoded from tcell's
// EventKey.Key() (see render.go's event switch) rather than through a
// symbolic-string table like ESCAPE_SEQUENCES — tcell already parses
// those multi-byte sequences for us.
//
// These move the selection cursor, not the viewport directly — deliberately
// unclamped (mirroring the old scroll methods), since the true bound
// (len(currentProcs)) is only known at the next drawFrame via clampCursor.
// The viewport (scrollOffset) auto-follows via syncScrollToCursor.
func (s *monitorState) cursorUp()       { s.cursor = max(0, s.cursor-1) }
func (s *monitorState) cursorDown()     { s.cursor++ }
func (s *monitorState) cursorPageUp()   { s.cursor = max(0, s.cursor-s.lastPageSize) }
func (s *monitorState) cursorPageDown() { s.cursor += s.lastPageSize }
func (s *monitorState) cursorHome()     { s.cursor = 0 }
func (s *monitorState) cursorEnd()      { s.cursor = 1 << 30 } // clamped in clampCursor

// handleRuneKey ports the ch := key.lower() branch of handle_key(): the
// j/k vim-style scroll keys, 'r' to flip direction, and the m/c/p/n
// sort-column keys (pressing the already-active column's key flips its
// direction, same as clicking an already-sorted table header again).
func (s *monitorState) handleRuneKey(r rune) {
	lower := []rune(strings.ToLower(string(r)))[0]

	switch lower {
	case 'j':
		s.cursorDown()
		return
	case 'k':
		s.cursorUp()
		return
	case 'r':
		s.sortReverse = !s.sortReverse
		s.sortDirty = true
		return
	}

	if col, ok := sortColumnKeys[lower]; ok {
		s.setSortColumn(col)
	}
}

// setSortColumn selects a sort column, flipping direction if it is already
// active (same behavior whether triggered by a sort key or a header click).
func (s *monitorState) setSortColumn(col sortColumn) {
	if col == s.sortCol {
		s.sortReverse = !s.sortReverse
	} else {
		s.sortCol = col
		s.sortReverse = sortDefaultReverse[col]
		// Switching the sort key reorders the whole list, so the scroll
		// offset (a row index into the old order) no longer means anything —
		// jump back to the top. A direction flip on the same column keeps the
		// offset, matching the "flip an already-sorted header" behavior.
		s.scrollOffset = 0
		s.cursor = 0
	}
	s.sortDirty = true
}

// scrollBy moves the scroll offset by delta rows, clamping at the top; the
// bottom is clamped per-frame in visibleProcesses.
func (s *monitorState) scrollBy(delta int) {
	s.scrollOffset += delta
	if s.scrollOffset < 0 {
		s.scrollOffset = 0
	}
	// Wheel scroll moves the viewport independently of the cursor (unlike
	// arrow/page/home/end, which move the cursor and let the viewport
	// follow). If the wheel scrolls the cursor out of view, pull it back to
	// the nearest visible row instead of leaving an invisible, stale
	// selection — using lastPageSize (the previous frame's viewport
	// height) as the same "good enough" approximation cursorPageUp/Down
	// already rely on between polls.
	if s.cursor < s.scrollOffset {
		s.cursor = s.scrollOffset
	} else if s.cursor > s.scrollOffset+s.lastPageSize-1 {
		s.cursor = s.scrollOffset + s.lastPageSize - 1
	}
}

// appendFilterRune adds a typed character to the incremental filter and, as
// with a sort-column change, jumps back to the top — the old scrollOffset is
// a row index into a match set that just changed.
func (s *monitorState) appendFilterRune(r rune) {
	s.filterQuery += string(r)
	s.scrollOffset = 0
	s.cursor = 0
}

// filterBackspace removes the last rune of the filter query, if any.
func (s *monitorState) filterBackspace() {
	if s.filterQuery == "" {
		return
	}
	runes := []rune(s.filterQuery)
	s.filterQuery = string(runes[:len(runes)-1])
	s.scrollOffset = 0
	s.cursor = 0
}

// clearFilter empties the filter query, ported from htop's Esc-clears-F4
// behavior.
func (s *monitorState) clearFilter() {
	s.filterQuery = ""
	s.scrollOffset = 0
	s.cursor = 0
}

// filterProcesses returns the subset of procs whose name or command
// contains query, case-insensitively — ports htop's F4 incremental filter.
// Order is preserved, so filtering an already-sorted slice needs no re-sort.
func filterProcesses(procs []Process, query string) []Process {
	q := strings.ToLower(query)
	out := make([]Process, 0, len(procs))
	for _, p := range procs {
		if strings.Contains(p.NameLower, q) || strings.Contains(strings.ToLower(p.Cmd), q) {
			out = append(out, p)
		}
	}
	return out
}

// sortColumnAt maps a click position to a header sort column, if the click
// landed on the header row within a column's span.
func (s *monitorState) sortColumnAt(x, y int) (sortColumn, bool) {
	if y != s.headerRow {
		return 0, false
	}
	for _, h := range s.sortHitboxes {
		if x >= h.x0 && x < h.x1 {
			return h.col, true
		}
	}
	return 0, false
}

// rowAt maps a click's screen row to an absolute index into currentProcs, if
// the click landed within the drawn table body (not the header, footer, or a
// short table's empty trailing space). Any column selects the row — unlike
// sortColumnAt, there's no per-column behavior to distinguish.
func (s *monitorState) rowAt(y int) (int, bool) {
	if y < s.tableTop || y >= s.tableTop+s.tableRowCount {
		return 0, false
	}
	return s.scrollOffset + (y - s.tableTop), true
}

// sortProcesses ports sort_processes(): sorts in place per current state.
// A nil CPUPct (no baseline sample yet) sorts as if it were 0, same as
// the Python version's `p["cpu_pct"] if ... is not None else 0`.
func (s *monitorState) sortProcesses(procs []Process) {
	less := func(i, j int) bool {
		var a, b float64
		switch s.sortCol {
		case sortRSS:
			a, b = float64(procs[i].RSSKb), float64(procs[j].RSSKb)
		case sortCPU:
			a, b = cpuOrZero(procs[i].CPUPct), cpuOrZero(procs[j].CPUPct)
		case sortPID:
			a, b = float64(procs[i].PID), float64(procs[j].PID)
		case sortName:
			ni, nj := procs[i].NameLower, procs[j].NameLower
			if s.sortReverse {
				return ni > nj
			}
			return ni < nj
		}
		if s.sortReverse {
			return a > b
		}
		return a < b
	}
	sort.SliceStable(procs, less)
}

func cpuOrZero(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// visibleProcesses returns the scrolled+sorted window of procs that fits
// in maxRows, clamping scrollOffset to what's valid for this frame's
// list length — same "best effort" scrolling as the Python version:
// scroll_offset is a row index into the current sort order, not a pointer
// to a specific process, so it holds roughly steady across resorts rather
// than tracking one process around.
func (s *monitorState) visibleProcesses(procs []Process, maxRows int) []Process {
	if maxRows < 1 {
		maxRows = 1
	}
	s.lastPageSize = maxRows

	total := len(procs)
	maxOffset := max(0, total-maxRows)
	if s.scrollOffset > maxOffset {
		s.scrollOffset = maxOffset
	}
	if s.scrollOffset < 0 {
		s.scrollOffset = 0
	}

	end := s.scrollOffset + maxRows
	if end > total {
		end = total
	}
	return procs[s.scrollOffset:end]
}

// clampCursor keeps the cursor within [0, total-1] (0 for an empty list).
// Needed because buildProcesses produces a brand-new slice every tick with
// no guaranteed same-index identity — if the process the cursor pointed at
// exited (or was just killed), this lands the cursor on whatever now
// occupies that row rather than pointing past the end of the slice.
func (s *monitorState) clampCursor(total int) {
	if s.cursor < 0 {
		s.cursor = 0
	}
	if s.cursor > total-1 {
		s.cursor = max(0, total-1)
	}
}

// syncScrollToCursor adjusts scrollOffset just enough to bring the cursor
// back into the visible window — the auto-scroll half of the cursor +
// auto-scroll design; cursorUp/cursorDown/etc. move only the cursor and
// rely on this (called from drawFrame) to keep it on screen.
func (s *monitorState) syncScrollToCursor(maxRows int) {
	if s.cursor < s.scrollOffset {
		s.scrollOffset = s.cursor
	} else if s.cursor > s.scrollOffset+maxRows-1 {
		s.scrollOffset = s.cursor - maxRows + 1
	}
}

// startKillConfirm opens the kill-confirmation prompt for the process
// currently under the cursor, snapshotting its PID/name so a poll tick that
// rebuilds currentProcs mid-confirm can't retarget a different process.
// Returns false (no-op) if the list is empty or the cursor is out of
// range — e.g. an empty filter result — so 'x' with nothing selected does
// nothing rather than opening a prompt for a nonexistent process.
func (s *monitorState) startKillConfirm() bool {
	if s.cursor < 0 || s.cursor >= len(s.currentProcs) {
		return false
	}
	p := s.currentProcs[s.cursor]
	s.killTargetPID = p.PID
	s.killTargetName = p.Name
	s.killConfirmMode = true
	s.killStatusMsg = ""
	return true
}

// cancelKillConfirm closes the prompt without sending a signal (Esc/n/N).
func (s *monitorState) cancelKillConfirm() {
	s.killConfirmMode = false
}

// sendKillSignal signals the process captured by startKillConfirm and
// records a one-line status message for the footer, then closes the
// prompt.
func (s *monitorState) sendKillSignal(sig syscall.Signal) {
	sigName := "SIGTERM"
	if sig == syscall.SIGKILL {
		sigName = "SIGKILL"
	}
	err := syscall.Kill(s.killTargetPID, sig)
	s.killStatusMsg = killResultMessage(s.killTargetPID, s.killTargetName, sigName, err)
	s.killConfirmMode = false
}

// killResultMessage formats a syscall.Kill outcome into footer text. Split
// out from sendKillSignal, like parseStatNameCPU is split from
// readStatNameCPU, so the interesting edge cases (ESRCH: already gone;
// EPERM: not ours to signal) are unit-testable without actually sending a
// real signal.
func killResultMessage(pid int, name, sigName string, err error) string {
	switch {
	case err == nil:
		return fmt.Sprintf("Sent %s to PID %d (%s)", sigName, pid, name)
	case err == syscall.ESRCH:
		return fmt.Sprintf("PID %d (%s) no longer exists", pid, name)
	case err == syscall.EPERM:
		return fmt.Sprintf("Permission denied signaling PID %d (%s)", pid, name)
	default:
		return fmt.Sprintf("Failed to signal PID %d (%s): %v", pid, name, err)
	}
}

// columnLabel ports col_header()'s arrow-appending: adds ▼ (descending)
// or ▲ (ascending) to the label when this is the active sort column.
func (s *monitorState) columnLabel(label string, col sortColumn) string {
	if s.sortCol != col {
		return label
	}
	if s.sortReverse {
		return label + "▼"
	}
	return label + "▲"
}

// Note: this file relies on Go's builtin max() (generic, added in Go
// 1.21) for the int comparisons above — no local helper needed.

// sampleCgroupCPUPct ports sample_cpu_pct(): cgroup-wide CPU% since the
// last call, or nil on the first call (no baseline yet) or if usage data
// isn't available. State that persisted as module globals in Python
// (_cpu_prev_usage_usec, _cpu_prev_time) lives on monitorState here.
func (s *monitorState) sampleCgroupCPUPct(coresLimit float64) *float64 {
	usageUsec, ok := readCgroupCPUUsageUsec()
	now := time.Now()

	var pct *float64
	if ok && coresLimit > 0 && s.cgroupCPUHasBaseline {
		dt := now.Sub(s.cgroupCPUPrevTime).Seconds()
		if dt > 0 {
			v := float64(usageUsec-s.cgroupCPUPrevUsageUsec) / 1_000_000 / dt / coresLimit * 100
			pct = &v
		}
	}

	if ok {
		s.cgroupCPUPrevUsageUsec = usageUsec
		s.cgroupCPUPrevTime = now
		s.cgroupCPUHasBaseline = true
	}
	return pct
}
