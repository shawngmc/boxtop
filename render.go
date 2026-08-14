package main

import (
	"fmt"
	"strings"

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

// meterBarWidth is the fixed bar length for the top bar's side-by-side
// cgroup/system meters — shorter than the 30-char process/summary bars
// used to have, since two of these now have to fit side by side in half a
// terminal's width.
const meterBarWidth = 20

// drawMeter renders one row of the top bar's cgroup/system grid — a label,
// then either a colored bar+percentage+detail, an "unavailable" reason, or
// a "measuring..." placeholder while a CPU meter's first delta sample is
// still pending. width is the column's available width (dividerCol for the
// cgroup column, w-dividerCol for the system column): unlike the rest of
// the frame, where an overlong line just clips at the screen's right edge,
// a meter that ran long here would otherwise get overwritten mid-character
// by the next column's own drawText calls on the same row — so the
// variable-length part of each line (the detail text) is explicitly
// truncated to what's left of the column, never left to chance.
func drawMeter(screen tcell.Screen, x, y, width int, m meter) {
	switch {
	case !m.have:
		text := fmt.Sprintf("%-5s unavailable (%s)", m.label, m.unavailableText)
		drawText(screen, x, y, truncateVisible(text, width), tcell.StyleDefault)
	case m.measuring:
		text := fmt.Sprintf("%-5s measuring...  %s", m.label, m.detail)
		drawText(screen, x, y, truncateVisible(text, width), tcell.StyleDefault)
	default:
		prefix := fmt.Sprintf("%-5s[", m.label)
		tx := drawText(screen, x, y, prefix, tcell.StyleDefault)
		barX := drawBar(screen, tx, y, meterBarWidth, m.frac, m.stops)
		pctPart := fmt.Sprintf("] %s ", m.pctText)
		tx = drawText(screen, barX, y, pctPart, gradientStyle(m.frac, m.stops))
		detailWidth := width - len(prefix) - meterBarWidth - len(pctPart)
		drawText(screen, tx, y, truncateVisible(m.detail, detailWidth), tcell.StyleDefault)
	}
}

// cursorRowStyle applies reverse video when this is the cursor row,
// preserving each column's existing gradient danger-coloring (e.g. a red
// RSS value becomes a red background) instead of replacing it outright.
func cursorRowStyle(base tcell.Style, isCursorRow bool) tcell.Style {
	if isCursorRow {
		return base.Reverse(true)
	}
	return base
}

// meter is one bar-and-text row of the top bar's cgroup/system RAM/CPU/Swap
// grid — a generic value that both drawFrame (colored, side-by-side) and
// writeNonInteractiveFrame (plain, stacked) can render without either one
// re-deriving the underlying numbers, which used to be formatted twice
// (once per renderer) before this type existed.
type meter struct {
	label           string // "RAM", "CPU", "SWAP"
	have            bool   // false -> show unavailableText instead of a bar
	measuring       bool   // true -> show "measuring..." instead of a bar
	unavailableText string
	frac            float64 // 0..1, drives both the bar fill and its gradient color
	pctText         string  // "42.3%", precomputed so callers never re-derive it
	detail          string  // "1.2G/4.0G", "2.00 cores (quota)", "8 cores", ...
	stops           []colorStop
}

// meterRAM/meterCPU/meterSwap index a [3]meter in the fixed row order used
// by both the cgroup and system columns.
const (
	meterRAM = iota
	meterCPU
	meterSwap
)

// memMeter builds a RAM or Swap meter from a used/total byte pair. suffix is
// appended to the detail text (e.g. " (host)" when a cgroup has no real
// limit and is being measured against host totals instead) — pass "" for
// the system-wide meters, which have no such distinction.
func memMeter(label string, usedBytes, totalBytes int64, suffix string) meter {
	var frac float64
	if totalBytes > 0 {
		frac = float64(usedBytes) / float64(totalBytes)
	}
	return meter{
		label:   label,
		have:    true,
		frac:    frac,
		pctText: fmt.Sprintf("%5.1f%%", frac*100),
		detail:  fmt.Sprintf("%s/%s%s", formatBytesCompact(usedBytes), formatBytesCompact(totalBytes), suffix),
		stops:   summaryStops,
	}
}

// unavailableMeter builds a meter with no bar, e.g. a cgroup without a swap
// controller or a host with swap disabled entirely.
func unavailableMeter(label, reason string) meter {
	return meter{label: label, have: false, unavailableText: reason}
}

// cgroupCPUMeter builds the cgroup CPU meter from the same limit/percent
// data drawFrame's CPU Limit line used to format directly.
func cgroupCPUMeter(coresLimit float64, source cpuLimitSource, haveLimit bool, pct *float64) meter {
	if !haveLimit {
		return unavailableMeter("CPU", "no cgroup cpu controller found")
	}
	sourceLabel := map[cpuLimitSource]string{
		cpuSourceQuota:  "quota",
		cpuSourceCPUSet: "cpuset",
		cpuSourceHost:   "host, unlimited",
	}[source]
	detail := fmt.Sprintf("%.2f cores (%s)", coresLimit, sourceLabel)
	if pct == nil {
		return meter{label: "CPU", have: true, measuring: true, detail: detail, stops: summaryStops}
	}
	frac := *pct / 100
	return meter{
		label:   "CPU",
		have:    true,
		frac:    frac,
		pctText: fmt.Sprintf("%5.1f%%", *pct),
		detail:  detail,
		stops:   summaryStops,
	}
}

// systemCPUMeter builds the system-wide CPU meter from sampleHostCPUPct's
// result: ok=false means /proc/stat couldn't be read at all, pct=nil means
// it was read but no baseline exists yet (first tick).
func systemCPUMeter(pct *float64, ok bool, numCPU int) meter {
	if !ok {
		return unavailableMeter("CPU", "could not read /proc/stat")
	}
	detail := fmt.Sprintf("%d cores", numCPU)
	if pct == nil {
		return meter{label: "CPU", have: true, measuring: true, detail: detail, stops: summaryStops}
	}
	frac := *pct / 100
	return meter{
		label:   "CPU",
		have:    true,
		frac:    frac,
		pctText: fmt.Sprintf("%5.1f%%", *pct),
		detail:  detail,
		stops:   summaryStops,
	}
}

// frameData is an immutable snapshot of everything a frame needs to draw,
// captured once per poll by collectFrame. Splitting collection from drawing
// is what lets keypresses (scroll/sort) trigger a cheap redraw from the last
// snapshot without re-reading /proc and /sys — and, importantly, without
// re-running the time-delta CPU sampling, which a per-keystroke poll would
// corrupt with near-zero intervals.
type frameData struct {
	cgroupMeters [3]meter // [meterRAM], [meterCPU], [meterSwap]
	systemMeters [3]meter
	cpuInfoLine  string // CPU model + cur/max clockspeed, unchanged content from before, now its own row

	// cgroupRAMMaxBytes is the cgroup memory limit in bytes — kept outside
	// the meter struct because drawFrame also needs it to size each
	// process row's RSS-fraction bar, not just for display.
	cgroupRAMMaxBytes int64

	procs       []Process
	totalRSSKb  int64
	oomKills    int64
	haveOOMData bool
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

	host, haveHost := readHostMeminfo()

	// When memory.max is "max", readCgroupVal falls through to
	// memory.limit_in_bytes, which reports the kernel's "unlimited"
	// sentinel (~LONG_MAX). That renders as a nonsensical exabyte-scale
	// limit, so treat any limit at or above host RAM as "no cgroup limit"
	// and use the host's total memory as the denominator instead.
	ramNoLimit := false
	if haveHost && (maxBytes <= 0 || maxBytes > host.totalKB*1024) {
		maxBytes = host.totalKB * 1024
		ramNoLimit = true
	}
	ramSuffix := ""
	if ramNoLimit {
		ramSuffix = " (host)"
	}
	cgroupRAM := memMeter("RAM", currBytes, maxBytes, ramSuffix)

	coresLimit, cpuSource, haveCPULimit := readCgroupCPULimit()
	var cpuPct *float64
	if haveCPULimit {
		cpuPct = state.sampleCgroupCPUPct(coresLimit)
	}
	cgroupCPU := cgroupCPUMeter(coresLimit, cpuSource, haveCPULimit, cpuPct)

	swapCurr, haveSwapCurr := readCgroupSwapCurrent()
	var cgroupSwap meter
	switch {
	case !haveSwapCurr:
		cgroupSwap = unavailableMeter("SWAP", "no swap controller found")
	default:
		swapMax, haveSwapMax := readCgroupSwapMax()
		swapSuffix := ""
		if !haveSwapMax {
			swapMax = host.swapTotalKB * 1024
			swapSuffix = " (host)"
		}
		if swapMax <= 0 {
			// Either the cgroup has an unbounded swap limit and the host
			// itself has no swap to fall back to, or the swap limit really
			// is zero — either way there's nothing meaningful to show as a
			// used/total bar.
			cgroupSwap = unavailableMeter("SWAP", "no swap configured")
		} else {
			cgroupSwap = memMeter("SWAP", swapCurr, swapMax, swapSuffix)
		}
	}

	var systemRAM, systemSwap, systemCPU meter
	if haveHost {
		availKB := host.availableKB
		usedKB := host.totalKB - availKB
		systemRAM = memMeter("RAM", usedKB*1024, host.totalKB*1024, "")
		if host.swapTotalKB <= 0 {
			systemSwap = unavailableMeter("SWAP", "no swap configured")
		} else {
			usedSwapKB := host.swapTotalKB - host.swapFreeKB
			systemSwap = memMeter("SWAP", usedSwapKB*1024, host.swapTotalKB*1024, "")
		}
	} else {
		systemRAM = unavailableMeter("RAM", "could not read /proc/meminfo")
		systemSwap = unavailableMeter("SWAP", "could not read /proc/meminfo")
	}
	hostPct, hostOK := state.sampleHostCPUPct()
	systemCPU = systemCPUMeter(hostPct, hostOK, runtimeNumCPU())

	oomKills, haveOOMData := readCgroupOOMKills()

	cpuModel, haveCPUModel := readCPUModel()
	cpuCurMHz, cpuMaxMHz, haveCPUCur, haveCPUMax := readCPUFreqMHz()
	cpuInfoLine := buildCPUInfoLine(cpuModel, haveCPUModel, cpuCurMHz, cpuMaxMHz, haveCPUCur, haveCPUMax)

	procs := buildProcesses(state, coresLimit)
	// A fresh poll yields a brand-new unsorted slice, so the next drawFrame
	// must sort it regardless of whether the sort column changed.
	state.sortDirty = true
	var totalRSSKb int64
	for _, p := range procs {
		totalRSSKb += p.RSSKb
	}

	return frameData{
		cgroupMeters:      [3]meter{meterRAM: cgroupRAM, meterCPU: cgroupCPU, meterSwap: cgroupSwap},
		systemMeters:      [3]meter{meterRAM: systemRAM, meterCPU: systemCPU, meterSwap: systemSwap},
		cpuInfoLine:       cpuInfoLine,
		cgroupRAMMaxBytes: maxBytes,
		procs:             procs,
		totalRSSKb:        totalRSSKb,
		oomKills:          oomKills,
		haveOOMData:       haveOOMData,
	}, nil
}

// buildCPUInfoLine formats the CPU model + cur/max clockspeed line, ported
// unchanged from the text that used to be appended to the CPU Limit line —
// it's host hardware info independent of cgroup vs. system scope, so it now
// gets its own full-width row instead.
func buildCPUInfoLine(model string, haveModel bool, curMHz, maxMHz float64, haveCur, haveMax bool) string {
	var parts []string
	if haveModel {
		parts = append(parts, model)
	}
	switch {
	case haveCur && haveMax:
		parts = append(parts, fmt.Sprintf("%.0f/%.0f MHz (cur/max)", curMHz, maxMHz))
	case haveCur:
		parts = append(parts, fmt.Sprintf("%.0f MHz (cur)", curMHz))
	case haveMax:
		parts = append(parts, fmt.Sprintf("%.0f MHz (max)", maxMHz))
	}
	if len(parts) == 0 {
		return ""
	}
	return "CPU: " + strings.Join(parts, "  |  ")
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

	maxBytes := data.cgroupRAMMaxBytes

	// dividerCol starts the system column at the horizontal midpoint of the
	// terminal so it scales with width, rather than a fixed offset.
	dividerCol := w / 2

	y := 0

	drawText(screen, 0, y, "cgroup", tcell.StyleDefault.Bold(true))
	drawText(screen, dividerCol, y, "system", tcell.StyleDefault.Bold(true))
	y++

	for i := 0; i < 3; i++ {
		// dividerCol-1 leaves a one-column gap so a truncated cgroup detail
		// text never butts directly up against the system column's label.
		drawMeter(screen, 0, y, dividerCol-1, data.cgroupMeters[i])
		drawMeter(screen, dividerCol, y, w-dividerCol, data.systemMeters[i])
		y++
	}

	if data.cpuInfoLine != "" {
		drawText(screen, 0, y, data.cpuInfoLine, tcell.StyleDefault)
	}
	y++

	drawText(screen, 0, y, repeatRune('=', w), tcell.StyleDefault)
	y++

	// --- incremental filter input (only while '/' has been used) ---
	if state.filterMode || state.filterQuery != "" {
		filterLine := " Filter: " + state.filterQuery
		if state.filterMode {
			filterLine += "█"
		}
		drawText(screen, 0, y, truncateVisible(filterLine, w), tcell.StyleDefault.Bold(true))
		y++
	}

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
	// footerY pins the footer to the bottom of the terminal regardless of how
	// many process rows actually got drawn — otherwise a short (or filtered
	// down) list leaves the footer floating right under the last row instead
	// of anchored at the screen edge.
	footerY := tableTop + maxProcRows

	procs := data.procs
	// Sort only when the order is stale (new poll, or sort column/direction
	// changed). A pure scroll redraw reuses the already-sorted cached slice.
	if state.sortDirty {
		state.sortProcesses(procs)
		state.sortDirty = false
	}

	// Filtering runs after sorting (cheap: it's just a subset pass) so the
	// retained processes keep the sorted order, needing no re-sort of its own.
	totalRSSKb := data.totalRSSKb
	if state.filterQuery != "" {
		procs = filterProcesses(procs, state.filterQuery)
		var filteredRSSKb int64
		for _, p := range procs {
			filteredRSSKb += p.RSSKb
		}
		totalRSSKb = filteredRSSKb
	}

	state.currentProcs = procs

	total := len(procs)
	state.clampCursor(total)
	truncated := total > maxProcRows
	// The scroll-position indicator when truncated is folded into the
	// footer rule line below rather than reserving a row for it here, so
	// every one of maxProcRows goes to an actual process row.
	state.syncScrollToCursor(maxProcRows)
	visible := state.visibleProcesses(procs, maxProcRows)
	state.tableTop = tableTop
	state.tableRowCount = len(visible)

	maxKb := float64(maxBytes) / 1024

	for i, p := range visible {
		isCursorRow := state.scrollOffset+i == state.cursor
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
		rssStyle := cursorRowStyle(gradientStyle(rssFrac, processStops), isCursorRow)

		var cpuText string
		var cpuStyle tcell.Style
		if p.CPUPct == nil {
			cpuText = fmt.Sprintf("%*s", cpuWidth, "--")
			cpuStyle = tcell.StyleDefault
		} else {
			var cpuFrac float64
			if data.cgroupMeters[meterCPU].have {
				cpuFrac = *p.CPUPct / 100
			}
			cpuText = fmt.Sprintf("%*.1f%%", cpuWidth-1, *p.CPUPct)
			cpuStyle = gradientStyle(cpuFrac, processCPUStops)
		}
		cpuStyle = cursorRowStyle(cpuStyle, isCursorRow)

		rowStyle := cursorRowStyle(tcell.StyleDefault, isCursorRow)
		x := drawText(screen, 0, y, fmt.Sprintf(" %*d  %-*s  ", pidWidth, p.PID, nameWidth, name), rowStyle)
		x = drawText(screen, x, y, cpuText, cpuStyle)
		x = drawText(screen, x, y, "  ", rowStyle)
		x = drawText(screen, x, y, fmt.Sprintf("%*.1f", rssWidth, float64(p.RSSKb)/1024), rssStyle)
		x = drawText(screen, x, y, "  ", rowStyle)
		drawText(screen, x, y, fmt.Sprintf("%-*s", cmdWidth, cmd), rowStyle)
		y++
	}

	// Jump to the fixed footer position — a short/filtered list stops well
	// short of it, since every row above is now an actual process row (see
	// the maxProcRows comment above — there's no separate reserved line to
	// account for here anymore).
	y = footerY

	// When the list is scrolled, a "[Processes: first-last/total]" indicator
	// is folded into this rule line (less/tmux-style) instead of spending a
	// whole row on it, which is what let the process table above reclaim
	// that row.
	footerRuleText := rule
	if truncated {
		first := state.scrollOffset + 1
		last := state.scrollOffset + len(visible)
		footerRuleText = footerRuleLine(len([]rune(rule)), fmt.Sprintf("[Processes: %d-%d/%d]", first, last, total))
	}
	drawText(screen, 0, y, footerRuleText, tcell.StyleDefault)
	y++
	// Process count isn't repeated here — it's already in the footer rule
	// line above, either as "[Processes: first-last/total]" when scrolled or,
	// when the whole list fits, implied by the row count itself.
	procX := drawText(screen, 0, y, fmt.Sprintf(" Sum of RSS: %.1f MB  (cgroup usage may include cache/shared mem not in RSS)",
		float64(totalRSSKb)/1024), tcell.StyleDefault)
	// OOM-kill counter: only shown when memory.events (or its v1
	// memory.oom_control fallback) is actually readable — e.g. omitted
	// outside a real memory-limited cgroup — since a bare "0" there would
	// read as "no kills happened" rather than the true "not measured." The
	// count only ever climbs, so any value above zero means the kernel has
	// already reaped a process in this cgroup, which the RAM bar alone
	// won't show once the freed memory drops it back under 100%.
	if data.haveOOMData {
		oomStyle := gradientStyle(0, summaryStops)
		if data.oomKills > 0 {
			oomStyle = gradientStyle(1, summaryStops).Bold(true)
		}
		drawText(screen, procX, y, fmt.Sprintf("  OOM Kills: %d", data.oomKills), oomStyle)
	}
	y++
	helpLine := " Help: [h] | Quit: [q] | Sort: [m]em [c]pu [p]id [n]ame [r]everse | Scroll: [↑↓] [PgUp/PgDn] [Home/End]"
	helpStyle := tcell.StyleDefault
	switch {
	case state.killConfirmMode:
		helpLine = fmt.Sprintf(" Kill PID %d (%s)?  [y] SIGTERM   [Y] SIGKILL   [Esc/n] cancel",
			state.killTargetPID, state.killTargetName)
		helpStyle = gradientStyle(1, summaryStops).Bold(true)
	case state.detailMode:
		helpLine = " Process detail — [Enter/Esc/q] close"
	case state.helpMode:
		helpLine = " Keybinding help — [Enter/Esc/q] close"
	case state.filterMode:
		helpLine = " Filter: type to search, Enter to apply, Esc to clear | Ctrl+C exit"
	case state.killStatusMsg != "":
		helpLine = " " + state.killStatusMsg
	default:
		helpLine += " | Filter: [/] | Kill: [x] | Details: [Enter]"
	}
	drawText(screen, 0, y, helpLine, helpStyle)

	if state.detailMode {
		drawDetailPopup(screen, state.detailData, w, h)
	}
	if state.helpMode {
		drawHelpPopup(screen, w, h)
	}
}

// detailLines formats a ProcessDetail into the popup's content lines. Split
// out from drawDetailPopup so the text content is testable without a
// tcell.Screen.
func detailLines(d ProcessDetail) []string {
	lines := []string{
		fmt.Sprintf("PID:     %d", d.PID),
		fmt.Sprintf("Name:    %s", d.Name),
	}
	if d.HaveExtra {
		lines = append(lines,
			fmt.Sprintf("PPID:    %d", d.PPID),
			fmt.Sprintf("State:   %s", d.State),
			fmt.Sprintf("User:    %s", d.User),
			fmt.Sprintf("Threads: %d      Nice: %d", d.Threads, d.Nice),
		)
	} else {
		lines = append(lines, "(further details unavailable — process may have exited)")
	}

	cpuText := "--"
	if d.CPUPct != nil {
		cpuText = fmt.Sprintf("%.1f%%", *d.CPUPct)
	}
	lines = append(lines,
		fmt.Sprintf("CPU%%:    %s", cpuText),
		fmt.Sprintf("RSS:     %.1f MB", float64(d.RSSKb)/1024),
	)
	if d.HaveExtra {
		lines = append(lines,
			fmt.Sprintf("VmSize:  %.1f MB", float64(d.VmSizeKb)/1024),
			fmt.Sprintf("VmSwap:  %.1f MB", float64(d.VmSwapKb)/1024),
		)
	}
	if d.HaveExePath {
		lines = append(lines, fmt.Sprintf("Exe:     %s", d.ExePath))
	}
	lines = append(lines, fmt.Sprintf("Cmd:     %s", d.Cmd))
	return lines
}

// drawDetailPopup draws a bordered, centered box over the already-drawn
// frame showing d's fields — called last in drawFrame so it overlays the
// process table rather than being overwritten by it.
func drawDetailPopup(screen tcell.Screen, d ProcessDetail, w, h int) {
	lines := detailLines(d)

	boxW := min(74, w-2)
	if boxW < 24 {
		boxW = min(24, w)
	}
	boxH := min(len(lines)+4, h-2)
	if boxH < 6 {
		boxH = min(6, h)
	}
	x0 := max(0, (w-boxW)/2)
	y0 := max(0, (h-boxH)/2)

	style := tcell.StyleDefault
	for y := y0; y < y0+boxH && y < h; y++ {
		for x := x0; x < x0+boxW && x < w; x++ {
			ch := ' '
			switch {
			case y == y0 && x == x0:
				ch = '┌'
			case y == y0 && x == x0+boxW-1:
				ch = '┐'
			case y == y0+boxH-1 && x == x0:
				ch = '└'
			case y == y0+boxH-1 && x == x0+boxW-1:
				ch = '┘'
			case y == y0 || y == y0+boxH-1:
				ch = '─'
			case x == x0 || x == x0+boxW-1:
				ch = '│'
			}
			screen.SetContent(x, y, ch, nil, style)
		}
	}

	innerWidth := boxW - 4
	title := fmt.Sprintf(" Process Detail: PID %d ", d.PID)
	drawText(screen, x0+2, y0, truncateVisible(title, innerWidth), style.Bold(true))

	maxContentLines := boxH - 3 // top border, bottom border, footer line
	for i, l := range lines {
		if i >= maxContentLines {
			break
		}
		drawText(screen, x0+2, y0+1+i, truncateVisible(l, innerWidth), style)
	}

	drawText(screen, x0+2, y0+boxH-2, truncateVisible("Enter/Esc/q: close", innerWidth), style.Italic(true))
}

// helpLines is the static content of the keybinding help popup, grouped by
// topic. Split out from drawHelpPopup so the text is testable without a
// tcell.Screen, matching detailLines/drawDetailPopup.
func helpLines() []string {
	return []string{
		"Navigation",
		"  ↑/↓, j/k, PgUp/PgDn, Home/End move the cursor",
		"  mouse wheel scrolls; click a header to sort, a row to select",
		"",
		"Sorting",
		"  m/c/p/n  sort by mem/cpu/pid/name (again = reverse)",
		"  r        reverse the current sort direction",
		"",
		"Filter",
		"  /        start filtering by name/command",
		"  Enter    apply filter        Esc  clear filter",
		"",
		"Process actions",
		"  Enter    show process details",
		"  x        kill selected process (y: SIGTERM, Y: SIGKILL)",
		"",
		"Other",
		"  h, ?       this help screen",
		"  q, Ctrl+C  quit (or close a popup)",
	}
}

// drawHelpPopup draws a bordered, centered box listing all keybindings,
// mirroring drawDetailPopup's layout — called last in drawFrame so it
// overlays the process table.
func drawHelpPopup(screen tcell.Screen, w, h int) {
	lines := helpLines()

	// Size the box to the longest line rather than a fixed width, so a
	// terminal wide enough never clips content — only a narrow terminal
	// forces wrapping-free truncation via the w-2 cap.
	longest := 0
	for _, l := range lines {
		if rl := len([]rune(l)); rl > longest {
			longest = rl
		}
	}
	boxW := min(longest+4, w-2)
	if boxW < 24 {
		boxW = min(24, w)
	}
	boxH := min(len(lines)+3, h-2)
	if boxH < 6 {
		boxH = min(6, h)
	}
	x0 := max(0, (w-boxW)/2)
	y0 := max(0, (h-boxH)/2)

	style := tcell.StyleDefault
	for y := y0; y < y0+boxH && y < h; y++ {
		for x := x0; x < x0+boxW && x < w; x++ {
			ch := ' '
			switch {
			case y == y0 && x == x0:
				ch = '┌'
			case y == y0 && x == x0+boxW-1:
				ch = '┐'
			case y == y0+boxH-1 && x == x0:
				ch = '└'
			case y == y0+boxH-1 && x == x0+boxW-1:
				ch = '┘'
			case y == y0 || y == y0+boxH-1:
				ch = '─'
			case x == x0 || x == x0+boxW-1:
				ch = '│'
			}
			screen.SetContent(x, y, ch, nil, style)
		}
	}

	innerWidth := boxW - 4
	drawText(screen, x0+2, y0, truncateVisible(" Keybindings ", innerWidth), style.Bold(true))

	maxContentLines := boxH - 2 // top border, bottom border
	for i, l := range lines {
		if i >= maxContentLines {
			break
		}
		drawText(screen, x0+2, y0+1+i, truncateVisible(l, innerWidth), style)
	}
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

// footerRuleLine builds the dashed rule above the footer, optionally with a
// label (e.g. a "[Processes: 1-15/390]" scroll-position indicator) embedded
// near its start — a `less`/`tmux`-style status rule, in place of a separate line
// reserved just for that text. Returns a plain width-n dash rule when label
// is empty, so this is a strict superset of repeatRune('-', n) for this
// call site. A label too long for the width degrades to a plain rule rather
// than overflowing or getting cut off mid-word.
func footerRuleLine(width int, label string) string {
	if label == "" {
		return repeatRune('-', width)
	}
	prefix := "-----" + label
	if len([]rune(prefix)) >= width {
		return repeatRune('-', width)
	}
	return prefix + repeatRune('-', width-len([]rune(prefix)))
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
