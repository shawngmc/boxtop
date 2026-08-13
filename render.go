package main

import (
	"fmt"

	"github.com/gdamore/tcell/v2"
)

const (
	nameWidth = 16
	cpuWidth  = 7
	rssWidth  = 9
	pidWidth  = 7
)

// drawText writes a string into the screen buffer starting at (x, y), one
// rune per cell, and returns the x position just past the text — the
// tcell equivalent of appending a colored substring to render()'s frame
// string in the Python version.
func drawText(screen tcell.Screen, x, y int, s string, style tcell.Style) int {
	for _, r := range s {
		screen.SetContent(x, y, r, nil, style)
		x++
	}
	return x
}

// drawBar renders a filled/empty block-character progress bar, matching
// the "█"*filled + "░"*(len-filled) bar in render(), colored as a single
// gradient style for the whole bar (same as Python: colorize() wraps the
// entire bar string in one escape based on the overall fraction, not a
// per-character gradient).
func drawBar(screen tcell.Screen, x, y, length int, frac float64, stops []colorStop) int {
	style := gradientStyle(frac, stops)
	filled := int(float64(length) * frac)
	if filled > length {
		filled = length
	}
	for i := 0; i < length; i++ {
		ch := '░'
		if i < filled {
			ch = '█'
		}
		screen.SetContent(x+i, y, ch, nil, style)
	}
	return x + length
}

// frameData is an immutable snapshot of everything a frame needs to draw,
// captured once per poll by collectFrame. Splitting collection from drawing
// is what lets keypresses (scroll/sort) trigger a cheap redraw from the last
// snapshot without re-reading /proc and /sys — and, importantly, without
// re-running the time-delta CPU sampling, which a per-keystroke poll would
// corrupt with near-zero intervals.
type frameData struct {
	maxBytes, currBytes int64
	noLimit             bool
	coresLimit          float64
	cpuSource           cpuLimitSource
	haveCPULimit        bool
	cpuPct              *float64 // summary cgroup CPU %, nil while measuring
	procs               []Process
	totalRSSKb          int64
}

// collectFrame ports the per-tick data gathering: cgroup memory/CPU limits,
// the whole-cgroup CPU sample, and the process table. All of the
// time-sensitive sampling lives here so it runs exactly once per refresh,
// never on a keystroke redraw.
func collectFrame(state *monitorState) (frameData, error) {
	maxBytes, okMax := readCgroupVal("memory.max")
	if !okMax {
		maxBytes, okMax = readCgroupVal("memory.limit_in_bytes")
	}
	currBytes, okCurr := readCgroupVal("memory.current")
	if !okCurr {
		currBytes, okCurr = readCgroupVal("memory.usage_in_bytes")
	}
	if !okMax || !okCurr {
		return frameData{}, fmt.Errorf("could not read cgroup memory values — are resource limits set?")
	}

	// When memory.max is "max", readCgroupVal falls through to
	// memory.limit_in_bytes, which reports the kernel's "unlimited"
	// sentinel (~LONG_MAX). That renders as a nonsensical exabyte-scale
	// limit, so treat any limit at or above host RAM as "no cgroup limit"
	// and use the host's total memory as the denominator instead.
	noLimit := false
	if hostTotal, ok := readHostMemTotal(); ok && (maxBytes <= 0 || maxBytes > hostTotal) {
		maxBytes = hostTotal
		noLimit = true
	}

	coresLimit, cpuSource, haveCPULimit := readCgroupCPULimit()
	var cpuPct *float64
	if haveCPULimit {
		cpuPct = state.sampleCgroupCPUPct(coresLimit)
	}

	procs := buildProcesses(coresLimit, state.procCPUPrev, state.cmdCache)
	var totalRSSKb int64
	for _, p := range procs {
		totalRSSKb += p.RSSKb
	}

	return frameData{
		maxBytes:     maxBytes,
		currBytes:    currBytes,
		noLimit:      noLimit,
		coresLimit:   coresLimit,
		cpuSource:    cpuSource,
		haveCPULimit: haveCPULimit,
		cpuPct:       cpuPct,
		procs:        procs,
		totalRSSKb:   totalRSSKb,
	}, nil
}

// drawFrame ports render(): builds one frame's contents into the screen
// buffer from a frameData snapshot. tcell's Show() diffs this against the
// previous frame and only writes the changed cells to the terminal, which
// is what replaces the Python version's manual \033[K / \033[J
// flicker-avoidance bookkeeping — none of that logic needs porting here.
// Sorting happens here (not in collectFrame) so a sort-key keypress reorders
// the cached snapshot instantly, with no re-poll.
func drawFrame(screen tcell.Screen, state *monitorState, data frameData) {
	screen.Clear()
	w, h := screen.Size()

	maxBytes := data.maxBytes
	maxMB := maxBytes / (1024 * 1024)
	currMB := data.currBytes / (1024 * 1024)
	availMB := maxMB - currMB
	var pctFrac float64
	if maxMB > 0 {
		pctFrac = float64(currMB) / float64(maxMB)
	}

	y := 0

	// --- RAM summary (left side) ---
	limitLabel := "Used/Limit"
	if data.noLimit {
		limitLabel = "Used/Host"
	}
	memUsage := fmt.Sprintf("RAM Usage : %d/%d MB (%s), %d MB Free", currMB, maxMB, limitLabel, availMB)
	drawText(screen, 0, y, memUsage, tcell.StyleDefault)

	// --- CPU summary (right side): starts at the horizontal midpoint of
	// the terminal so it scales with width, rather than the Python
	// version's fixed DIVIDER_COL padding.
	dividerCol := w / 2
	coresLimit := data.coresLimit
	if data.haveCPULimit {
		sourceLabel := map[cpuLimitSource]string{
			cpuSourceQuota:  "cgroup quota",
			cpuSourceCPUSet: "cpuset",
			cpuSourceHost:   "host, no limit set",
		}[data.cpuSource]
		cpuLimitText := fmt.Sprintf("CPU Limit : %.2f cores (%s)", coresLimit, sourceLabel)
		drawText(screen, dividerCol, y, cpuLimitText, tcell.StyleDefault)
	} else {
		drawText(screen, dividerCol, y, "CPU Limit : unavailable (no cgroup cpu controller found)", tcell.StyleDefault)
	}
	y++

	drawText(screen, 0, y, "  Percent : [", tcell.StyleDefault)
	barX := drawBar(screen, 13, y, 30, pctFrac, summaryStops)
	drawText(screen, barX, y, fmt.Sprintf("] %5.1f%%", pctFrac*100), gradientStyle(pctFrac, summaryStops))

	if data.haveCPULimit {
		if data.cpuPct == nil {
			drawText(screen, dividerCol, y, "  Percent : measuring...", tcell.StyleDefault)
		} else {
			cpuFrac := *data.cpuPct / 100
			drawText(screen, dividerCol, y, "  Percent : [", tcell.StyleDefault)
			cbarX := drawBar(screen, dividerCol+13, y, 30, cpuFrac, summaryStops)
			drawText(screen, cbarX, y, fmt.Sprintf("] %5.1f%%", *data.cpuPct), gradientStyle(cpuFrac, summaryStops))
		}
	}
	y++

	drawText(screen, 0, y, repeatRune('=', w), tcell.StyleDefault)
	y++

	// --- process table ---
	fixedWidth := 1 + pidWidth + 2 + nameWidth + 2 + cpuWidth + 2 + rssWidth + 2
	cmdWidth := max(15, w-fixedWidth)

	header := fmt.Sprintf(" %*s  %-*s  %*s  %*s  %-*s",
		pidWidth, state.columnLabel("PID", sortPID),
		nameWidth, state.columnLabel("NAME", sortName),
		cpuWidth, state.columnLabel("CPU%", sortCPU),
		rssWidth, state.columnLabel("RSS (MB)", sortRSS),
		cmdWidth, "COMMAND")
	drawText(screen, 0, y, truncateVisible(header, w), tcell.StyleDefault.Bold(true))

	// Record click targets matching the field layout above (leading space,
	// then each column separated by two spaces), so a header click sorts.
	hx := 1
	state.headerRow = y
	state.sortHitboxes = state.sortHitboxes[:0]
	for _, c := range []struct {
		width int
		col   sortColumn
	}{
		{pidWidth, sortPID},
		{nameWidth, sortName},
		{cpuWidth, sortCPU},
		{rssWidth, sortRSS},
	} {
		state.sortHitboxes = append(state.sortHitboxes, sortHitbox{x0: hx, x1: hx + c.width, col: c.col})
		hx += c.width + 2
	}
	y++
	rule := repeatRune('-', min(w, fixedWidth+cmdWidth))
	drawText(screen, 0, y, rule, tcell.StyleDefault)
	y++

	tableTop := y
	footerRows := 3 // footer rule + summary line + key-help line
	maxProcRows := max(3, h-tableTop-footerRows)

	procs := data.procs
	state.sortProcesses(procs)
	totalRSSKb := data.totalRSSKb

	total := len(procs)
	truncated := total > maxProcRows
	shownRows := maxProcRows
	if truncated {
		shownRows = max(1, maxProcRows-1) // reserve a row for the scroll-position status line
	}
	visible := state.visibleProcesses(procs, shownRows)

	maxKb := float64(maxBytes) / 1024

	for _, p := range visible {
		name := p.Name
		if len([]rune(name)) > nameWidth {
			name = string([]rune(name)[:nameWidth-1]) + "…"
		}
		cmd := p.Cmd
		if len([]rune(cmd)) > cmdWidth {
			cmd = string([]rune(cmd)[:cmdWidth-1]) + "…"
		}

		var rssFrac float64
		if maxKb > 0 {
			rssFrac = float64(p.RSSKb) / maxKb
		}
		rssStyle := gradientStyle(rssFrac, processStops)

		var cpuText string
		var cpuStyle tcell.Style
		if p.CPUPct == nil {
			cpuText = fmt.Sprintf("%*s", cpuWidth, "--")
			cpuStyle = tcell.StyleDefault
		} else {
			var cpuFrac float64
			if coresLimit > 0 {
				cpuFrac = *p.CPUPct / 100
			}
			cpuText = fmt.Sprintf("%*.1f%%", cpuWidth-1, *p.CPUPct)
			cpuStyle = gradientStyle(cpuFrac, processCPUStops)
		}

		x := drawText(screen, 0, y, fmt.Sprintf(" %*d  %-*s  ", pidWidth, p.PID, nameWidth, name), tcell.StyleDefault)
		x = drawText(screen, x, y, cpuText, cpuStyle)
		x = drawText(screen, x, y, "  ", tcell.StyleDefault)
		x = drawText(screen, x, y, fmt.Sprintf("%*.1f", rssWidth, float64(p.RSSKb)/1024), rssStyle)
		x = drawText(screen, x, y, "  ", tcell.StyleDefault)
		drawText(screen, x, y, fmt.Sprintf("%-*s", cmdWidth, cmd), tcell.StyleDefault)
		y++
	}

	if truncated {
		first := state.scrollOffset + 1
		last := state.scrollOffset + len(visible)
		drawText(screen, 0, y, fmt.Sprintf(" ... showing %d-%d of %d — scroll for more ...", first, last, total), tcell.StyleDefault)
		y++
	}

	drawText(screen, 0, y, rule, tcell.StyleDefault)
	y++
	drawText(screen, 0, y, fmt.Sprintf(" Processes: %-5d  Sum of RSS: %.1f MB  (cgroup usage may include cache/shared mem not in RSS)",
		total, float64(totalRSSKb)/1024), tcell.StyleDefault)
	y++
	drawText(screen, 0, y, " Ctrl+C/q exit | Sort: [m]em [c]pu [p]id [n]ame  [r]everse | Scroll: ↑↓/j/k PgUp/PgDn Home/End | Mouse: wheel scroll, click header to sort", tcell.StyleDefault)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func repeatRune(r rune, n int) string {
	if n < 0 {
		n = 0
	}
	runes := make([]rune, n)
	for i := range runes {
		runes[i] = r
	}
	return string(runes)
}

// truncateVisible trims s to at most w visible runes — a simpler
// stand-in for the Python version's vljust()/[:term_cols] slicing, since
// tcell cells are drawn individually rather than via a padded string, so
// most padding concerns don't apply here; this only guards the one
// pre-built header string against overflowing a narrow terminal.
func truncateVisible(s string, w int) string {
	runes := []rune(s)
	if len(runes) <= w {
		return s
	}
	if w < 0 {
		w = 0
	}
	return string(runes[:w])
}
