package main

import (
	"sort"
	"strings"
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

	cgroupCPUPrevUsageUsec int64
	cgroupCPUPrevTime      time.Time
	cgroupCPUHasBaseline   bool

	// Header click targets, recorded by drawFrame each frame so mouse clicks
	// map to the same columns that were actually rendered (rather than
	// duplicating the layout math in the event handler).
	headerRow    int
	sortHitboxes []sortHitbox
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

// handleKey ports handle_key(): updates sort/scroll state for a single
// keypress. Arrow/page/home/end come in pre-decoded from tcell's
// EventKey.Key() (see render.go's event switch) rather than through a
// symbolic-string table like ESCAPE_SEQUENCES — tcell already parses
// those multi-byte sequences for us.
func (s *monitorState) scrollUp()   { s.scrollOffset = max(0, s.scrollOffset-1) }
func (s *monitorState) scrollDown() { s.scrollOffset++ }
func (s *monitorState) pageUp()     { s.scrollOffset = max(0, s.scrollOffset-s.lastPageSize) }
func (s *monitorState) pageDown()   { s.scrollOffset += s.lastPageSize }
func (s *monitorState) scrollHome() { s.scrollOffset = 0 }
func (s *monitorState) scrollEnd()  { s.scrollOffset = 1 << 30 } // clamped in visibleProcesses

// handleRuneKey ports the ch := key.lower() branch of handle_key(): the
// j/k vim-style scroll keys, 'r' to flip direction, and the m/c/p/n
// sort-column keys (pressing the already-active column's key flips its
// direction, same as clicking an already-sorted table header again).
func (s *monitorState) handleRuneKey(r rune) {
	lower := []rune(strings.ToLower(string(r)))[0]

	switch lower {
	case 'j':
		s.scrollOffset++
		return
	case 'k':
		s.scrollOffset = max(0, s.scrollOffset-1)
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
