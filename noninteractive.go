// noninteractive.go is the fallback path for when boxtop isn't attached to
// a real terminal on both ends (or --non-interactive forces it anyway):
// tcell's raw-mode event loop needs a real tty to read keys and draw into,
// so instead this collects one baseline sample, waits one tick, collects
// again (now with real CPU-usage deltas), and prints a single plain-text
// snapshot to stdout — no raw mode, no key handling, no repeating loop that
// would hang a script or flood a log.
package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
)

// isTerminal is the isatty-style check used to decide whether the
// interactive tcell loop is even usable.
func isTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

// nonInteractiveWidth picks a line width for the plain-text renderer, since
// there's no tcell screen to ask. Stdout may still be a real terminal even
// when non-interactive mode was forced by flag or triggered by a
// non-terminal stdin (e.g. `boxtop --filter foo < recording`), so a real
// size is preferred; $COLUMNS and a fixed fallback cover the fully
// redirected case.
func nonInteractiveWidth() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		return w
	}
	if cols := os.Getenv("COLUMNS"); cols != "" {
		if w, err := strconv.Atoi(cols); err == nil && w > 0 {
			return w
		}
	}
	return 120
}

// runNonInteractive replaces run()'s tcell event loop: no screen, no raw
// mode, no key handling, and — unlike the interactive TUI — no repeating
// loop, so a script that shells out to boxtop gets one snapshot back and
// doesn't hang. Both cgroup-wide and per-process CPU% are computed from a
// time delta between two samples (see sampleCgroupCPUPct and buildProcesses'
// use of procCPUPrev), so the first collectFrame call only plants that
// baseline; after waiting one tick, the second call has a real delta to
// report and its result is what gets printed.
func runNonInteractive(interval time.Duration, initialFilter string) error {
	state := newMonitorState()
	state.filterQuery = initialFilter

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	if _, err := collectFrame(state); err != nil {
		return err
	}

	select {
	case <-sigCh:
		return nil
	case <-time.After(interval):
	}

	data, err := collectFrame(state)
	if err != nil {
		return err
	}
	state.sortProcesses(data.procs)
	writeNonInteractiveFrame(os.Stdout, state, data, nonInteractiveWidth())
	return nil
}

// plainBar is drawBar's plain-text equivalent: no color, just the block
// characters, since a redirected/piped stdout has no styling to render.
func plainBar(length int, frac float64) string {
	if length < 0 {
		length = 0
	}
	filled := int(float64(length) * frac)
	if filled > length {
		filled = length
	}
	if filled < 0 {
		filled = 0
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", length-filled)
}

// writeMeter is drawMeter's plain-text equivalent. Unlike the interactive
// top bar, which packs cgroup and system side by side to fit a terminal
// width, plain-text output has no such column constraint — so cgroup and
// system meters are printed as separate, fully-detailed stacked lines
// instead of trying to replicate the side-by-side layout in text form.
func writeMeter(w io.Writer, scope string, m meter) {
	label := fmt.Sprintf("%s %s", scope, m.label)
	switch {
	case !m.have:
		fmt.Fprintf(w, "%-11s: unavailable (%s)\n", label, m.unavailableText)
	case m.measuring:
		fmt.Fprintf(w, "%-11s: measuring...  %s\n", label, m.detail)
	default:
		fmt.Fprintf(w, "%-11s: [%s] %s  %s\n", label, plainBar(30, m.frac), m.pctText, m.detail)
	}
}

// writeNonInteractiveFrame formats one snapshot as plain text: the same RAM/
// CPU summary and column header content as drawFrame, but the full process
// list unconditionally (there's no viewport to scroll) and a simplified
// footer with no key-help line, since there are no keys to press.
func writeNonInteractiveFrame(w io.Writer, state *monitorState, data frameData, width int) {
	fmt.Fprintf(w, "----- %s -----\n", time.Now().Format("2006-01-02 15:04:05"))

	if !data.cgroupHidden {
		cgroupScope := "cgroup"
		if data.cgroupName != "" {
			cgroupScope = fmt.Sprintf("cgroup (%s)", data.cgroupName)
		}
		for i := 0; i < 3; i++ {
			writeMeter(w, cgroupScope, data.cgroupMeters[i])
		}
	}
	for i := 0; i < 3; i++ {
		writeMeter(w, "system", data.systemMeters[i])
	}
	if data.cpuInfoLine != "" {
		fmt.Fprintln(w, data.cpuInfoLine)
	}

	fmt.Fprintln(w, strings.Repeat("=", width))

	if state.filterQuery != "" {
		fmt.Fprintf(w, " Filter: %s\n", state.filterQuery)
	}

	fixedWidth := 1 + pidWidth + 2 + nameWidth + 2 + cpuWidth + 2 + rssWidth + 2
	cmdWidth := max(15, width-fixedWidth)

	header := fmt.Sprintf(" %*s  %-*s  %*s  %*s  %-*s",
		pidWidth, state.columnLabel("PID", sortPID),
		nameWidth, state.columnLabel("NAME", sortName),
		cpuWidth, state.columnLabel("CPU%", sortCPU),
		rssWidth, state.columnLabel("RSS (MB)", sortRSS),
		cmdWidth, "COMMAND")
	fmt.Fprintln(w, truncateVisible(header, width))
	rule := strings.Repeat("-", min(width, fixedWidth+cmdWidth))
	fmt.Fprintln(w, rule)

	procs := data.procs
	totalRSSKb := data.totalRSSKb
	if state.filterQuery != "" {
		procs = filterProcesses(procs, state.filterQuery)
		var filteredRSSKb int64
		for _, p := range procs {
			filteredRSSKb += p.RSSKb
		}
		totalRSSKb = filteredRSSKb
	}

	for _, p := range procs {
		name := p.Name
		if len([]rune(name)) > nameWidth {
			name = string([]rune(name)[:nameWidth-1]) + "…"
		}
		cmd := p.Cmd
		if len([]rune(cmd)) > cmdWidth {
			cmd = string([]rune(cmd)[:cmdWidth-1]) + "…"
		}

		var cpuText string
		if p.CPUPct == nil {
			cpuText = fmt.Sprintf("%*s", cpuWidth, "--")
		} else {
			cpuText = fmt.Sprintf("%*.1f%%", cpuWidth-1, *p.CPUPct)
		}

		fmt.Fprintf(w, " %*d  %-*s  %s  %*.1f  %-*s\n",
			pidWidth, p.PID, nameWidth, name, cpuText, rssWidth, float64(p.RSSKb)/1024, cmdWidth, cmd)
	}

	fmt.Fprintln(w, rule)
	fmt.Fprintf(w, " Processes: %-5d  Sum of RSS: %.1f MB  (cgroup usage may include cache/shared mem not in RSS)",
		len(procs), float64(totalRSSKb)/1024)
	if data.haveOOMData {
		fmt.Fprintf(w, "  OOM Kills: %d", data.oomKills)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w)
}
