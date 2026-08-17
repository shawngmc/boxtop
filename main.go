// Command boxtop is a cgroup-aware memory monitor with a per-process
// breakdown, similar to top/htop but reporting against a container's
// cgroup memory limit rather than the host's total RAM. This is the
// tcell-based port of boxtop.py — see the package comment in each file
// for what maps to which piece of the original script.
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"syscall"
	"time"

	"github.com/gdamore/tcell/v2"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	var colorblind, showVersion, nonInteractive, listCgroupsFlag, narrow bool
	var filter, cgroupOverride string
	flag.BoolVar(&colorblind, "colorblind", false, "use the colorblind-friendly palette")
	flag.BoolVar(&colorblind, "c", false, "shorthand for --colorblind")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.BoolVar(&narrow, "narrow", false, "force the stacked cgroup/system top-bar layout (system block below cgroup) instead of side-by-side — automatic below 120 columns wide regardless of this flag")
	flag.StringVar(&filter, "filter", "", "start with an incremental process filter already applied (same as pressing '/', typing, then Enter)")
	flag.StringVar(&filter, "f", "", "shorthand for --filter")
	flag.BoolVar(&nonInteractive, "non-interactive", false, "skip the interactive TUI: wait one refresh_interval_seconds tick, then print one snapshot (header, full process list, simplified footer) and exit. Auto-enabled when stdin or stdout isn't a terminal (piped/redirected).")
	flag.BoolVar(&nonInteractive, "n", false, "shorthand for --non-interactive")
	flag.StringVar(&cgroupOverride, "cgroup", "", "monitor a specific cgroup's RAM/CPU/Swap limits instead of boxtop's own, by name or path (e.g. docker/1a2b3c4d5e6f, system.slice/foo.service, or a full /sys/fs/cgroup/... path) — see --list-cgroups for valid names. The process list is still the host's own /proc, unscoped by this flag.")
	flag.BoolVar(&listCgroupsFlag, "list-cgroups", false, "print every cgroup found on the host along with its current RAM/CPU status, then exit (use with --cgroup)")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [refresh_interval_seconds] [flags]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "refresh_interval_seconds defaults to 1 if omitted.\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if showVersion {
		fmt.Println("boxtop", version)
		return
	}

	if listCgroupsFlag {
		hostInfo, statuses, err := listCgroupsStatus()
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			os.Exit(1)
		}
		printCgroupList(os.Stdout, hostInfo, statuses)
		return
	}

	cgroupSuffix = normalizeCgroupOverride(cgroupOverride)

	interval := time.Second
	if args := flag.Args(); len(args) > 0 {
		secs, err := strconv.ParseFloat(args[0], 64)
		if err != nil {
			flag.Usage()
			os.Exit(2)
		}
		interval = time.Duration(secs * float64(time.Second))
	}
	if interval <= 0 {
		interval = time.Second
	}

	useColorblindPalette(colorblind)

	// tcell's raw-mode event loop needs a real terminal on both ends: one to
	// read keys from, one to draw into. Fall back to the plain-timer
	// non-interactive path if either isn't a tty, even without --non-interactive.
	if !nonInteractive {
		nonInteractive = !isTerminal(os.Stdin) || !isTerminal(os.Stdout)
	}

	runFn := run
	if nonInteractive {
		runFn = runNonInteractive
	}
	if err := runFn(interval, filter, narrow); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func run(interval time.Duration, initialFilter string, forceNarrow bool) error {
	screen, err := tcell.NewScreen()
	if err != nil {
		return err
	}
	if err := screen.Init(); err != nil {
		return err
	}
	defer screen.Fini()

	// tcell owns raw-mode setup/teardown internally — no separate
	// golang.org/x/term calls needed, unlike the Option A sketch.
	//
	// Request button events only (clicks + wheel), NOT motion: passing no
	// flags would also enable motion tracking, flooding the loop with an
	// EventMouse on every mouse-over and spiking CPU. MouseButtonEvents gives
	// us wheel-scroll and click-to-sort without that.
	screen.EnableMouse(tcell.MouseButtonEvents)

	state := newMonitorState()
	state.filterQuery = initialFilter
	state.forceNarrow = forceNarrow

	events := make(chan tcell.Event, 8)
	quit := make(chan struct{})
	go screen.ChannelEvents(events, quit)
	defer close(quit)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// data is the last polled snapshot. We poll (read /proc and /sys, and run
	// the time-delta CPU sampling) only on the ticker; key events reorder or
	// scroll the cached snapshot and redraw cheaply, so a held-down scroll
	// key doesn't hammer the filesystem or corrupt CPU% with tiny intervals.
	data, err := collectFrame(state)
	if err != nil {
		return err
	}
	drawFrame(screen, state, data)
	screen.Show()

	for {
		select {
		case ev := <-events:
			redraw, quitNow := handleEvent(screen, state, ev)
			if quitNow {
				return nil
			}
			// Coalesce a key-repeat backlog: drain everything already queued,
			// applying each so a held scroll key still moves, but collapse it
			// into a single redraw instead of one per event.
			for drained := true; drained; {
				select {
				case ev := <-events:
					r, q := handleEvent(screen, state, ev)
					if q {
						return nil
					}
					redraw = redraw || r
				default:
					drained = false
				}
			}
			// A cgroup picker selection ('g', Enter) redirects every reader
			// in cgroup.go via cgroupSuffix, but the meters in `data` were
			// captured against the old target — re-poll now instead of
			// leaving stale numbers up for however long is left on the
			// ticker.
			if state.cgroupChangePending {
				state.cgroupChangePending = false
				data, err = collectFrame(state)
				if err != nil {
					return err
				}
				redraw = true
			}
			if redraw {
				drawFrame(screen, state, data)
				screen.Show()
			}
		case <-ticker.C:
			data, err = collectFrame(state)
			if err != nil {
				return err
			}
			drawFrame(screen, state, data)
			screen.Show()
		}
	}
}

// handleEvent applies a single tcell event to state and reports whether the
// screen needs a redraw and whether the app should quit. It never polls —
// scroll/sort just mutate cached view state.
func handleEvent(screen tcell.Screen, state *monitorState, ev tcell.Event) (redraw, quit bool) {
	switch e := ev.(type) {
	case *tcell.EventKey:
		if state.killConfirmMode {
			return handleKillConfirmKey(state, e)
		}
		if state.cgroupSelectMode {
			return handleCgroupSelectKey(state, e)
		}
		if state.detailMode {
			return handleDetailKey(state, e)
		}
		if state.helpMode {
			return handleHelpKey(state, e)
		}
		// statusMsg clears on the very next keypress, whatever it is —
		// including a key that would otherwise be a no-op — so the OR-in of
		// clearedStatus below still forces a redraw to drop the stale text.
		clearedStatus := state.statusMsg != ""
		if clearedStatus {
			state.statusMsg = ""
		}
		var redraw, quit bool
		if state.filterMode {
			redraw, quit = handleFilterKey(state, e)
		} else {
			redraw, quit = handleNormalKey(state, e)
		}
		return redraw || clearedStatus, quit
	case *tcell.EventMouse:
		// A modal popup (kill-confirm, cgroup picker, process detail, help)
		// has exclusive keyboard focus above — see the EventKey branch's
		// mode checks — so mouse input over the table it's drawn on top of
		// must be blocked the same way, or a click "through" the popup can
		// move the cursor/sort column on rows the user can't even see.
		if state.killConfirmMode || state.cgroupSelectMode || state.detailMode || state.helpMode {
			return false, false
		}
		btns := e.Buttons()
		switch {
		case btns&tcell.WheelUp != 0:
			state.scrollBy(-mouseWheelStep)
			return true, false
		case btns&tcell.WheelDown != 0:
			state.scrollBy(mouseWheelStep)
			return true, false
		case btns&tcell.Button1 != 0:
			// Act on the press; the matching release arrives as a separate
			// ButtonNone event, which falls through to no-op.
			mx, my := e.Position()
			if col, ok := state.sortColumnAt(mx, my); ok {
				state.setSortColumn(col)
				return true, false
			}
			if row, ok := state.rowAt(my); ok {
				state.cursor = row
				return true, false
			}
		}
		return false, false
	case *tcell.EventResize:
		// tcell gives resize as a real event; the Python version just
		// re-reads terminal size every frame via shutil.get_terminal_size()
		// instead. Sync() forces a full repaint against the new size.
		screen.Sync()
		return true, false
	}
	return false, false
}

// handleNormalKey applies a single keypress when neither the filter input
// nor the kill-confirmation prompt has focus: cursor movement, quit, and
// the mode-opening keys ('/' for filter, 'x' for kill).
func handleNormalKey(state *monitorState, e *tcell.EventKey) (redraw, quit bool) {
	switch e.Key() {
	case tcell.KeyCtrlC, tcell.KeyEscape:
		return false, true
	case tcell.KeyUp:
		state.cursorUp()
	case tcell.KeyDown:
		state.cursorDown()
	case tcell.KeyPgUp:
		state.cursorPageUp()
	case tcell.KeyPgDn:
		state.cursorPageDown()
	case tcell.KeyHome:
		state.cursorHome()
	case tcell.KeyEnd:
		state.cursorEnd()
	case tcell.KeyEnter:
		if !state.startDetailView() {
			return false, false
		}
	case tcell.KeyRune:
		r := e.Rune()
		if r == 'q' || r == 'Q' {
			return false, true
		}
		if r == '/' {
			state.filterMode = true
			return true, false
		}
		if r == 'x' {
			if state.startKillConfirm() {
				return true, false
			}
			return false, false
		}
		if r == 'h' || r == 'H' || r == '?' {
			state.openHelpView()
			return true, false
		}
		if r == 'g' || r == 'G' {
			state.openCgroupSelect()
			return true, false
		}
		state.handleRuneKey(r)
	default:
		return false, false
	}
	return true, false
}

// handleDetailKey applies a single keypress while the process-details popup
// (opened via Enter) has focus. Enter/Esc/q/Q all close it — Enter doubles
// as both "open" (in handleNormalKey) and "close" here, matching how a
// header click both selects and re-flips a sort column. Ctrl+C still
// force-quits, matching handleFilterKey/handleKillConfirmKey's escape hatch.
func handleDetailKey(state *monitorState, e *tcell.EventKey) (redraw, quit bool) {
	switch e.Key() {
	case tcell.KeyCtrlC:
		return false, true
	case tcell.KeyEscape, tcell.KeyEnter:
		state.closeDetailView()
		return true, false
	case tcell.KeyRune:
		switch e.Rune() {
		case 'q', 'Q':
			state.closeDetailView()
			return true, false
		}
	}
	return false, false
}

// handleHelpKey applies a single keypress while the keybinding help popup
// (opened via 'h'/'?') has focus. Enter/Esc/q/Q all close it, same as
// handleDetailKey. Ctrl+C still force-quits.
func handleHelpKey(state *monitorState, e *tcell.EventKey) (redraw, quit bool) {
	switch e.Key() {
	case tcell.KeyCtrlC:
		return false, true
	case tcell.KeyEscape, tcell.KeyEnter:
		state.closeHelpView()
		return true, false
	case tcell.KeyRune:
		switch e.Rune() {
		case 'q', 'Q':
			state.closeHelpView()
			return true, false
		}
	}
	return false, false
}

// handleCgroupSelectKey applies a single keypress while the cgroup picker
// (opened via 'g'/'G') has focus. Unlike handleDetailKey/handleHelpKey,
// this popup accepts text input to narrow the list — so, like
// handleFilterKey, typed runes (including 'q') feed the filter rather than
// closing the popup; only Esc/Ctrl+C cancel and Enter commits.
func handleCgroupSelectKey(state *monitorState, e *tcell.EventKey) (redraw, quit bool) {
	switch e.Key() {
	case tcell.KeyCtrlC:
		return false, true
	case tcell.KeyEscape:
		state.closeCgroupSelect()
		return true, false
	case tcell.KeyEnter:
		state.applyCgroupSelection()
		return true, false
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		state.cgroupSelectBackspace()
		return true, false
	case tcell.KeyUp:
		state.cgroupSelectMove(-1)
		return true, false
	case tcell.KeyDown:
		state.cgroupSelectMove(1)
		return true, false
	case tcell.KeyPgUp:
		state.cgroupSelectMove(-cgroupSelectPageSize)
		return true, false
	case tcell.KeyPgDn:
		state.cgroupSelectMove(cgroupSelectPageSize)
		return true, false
	case tcell.KeyHome:
		state.cgroupSelectHome()
		return true, false
	case tcell.KeyEnd:
		state.cgroupSelectEnd()
		return true, false
	case tcell.KeyRune:
		state.cgroupSelectAppendRune(e.Rune())
		return true, false
	}
	return false, false
}

// mouseWheelStep is how many rows one wheel notch scrolls.
const mouseWheelStep = 3

// handleFilterKey applies a single keypress while the incremental filter
// input (opened via '/') has focus. Typed runes narrow the process list
// live; Backspace edits the query; Enter leaves the input focused elsewhere
// while keeping the filter applied; Esc clears the filter and closes the
// input — the same split htop uses for its F4 filter bar. Ctrl+C still
// force-quits so a stuck filter session isn't a trap.
func handleFilterKey(state *monitorState, e *tcell.EventKey) (redraw, quit bool) {
	switch e.Key() {
	case tcell.KeyCtrlC:
		return false, true
	case tcell.KeyEnter:
		state.filterMode = false
		return true, false
	case tcell.KeyEscape:
		state.filterMode = false
		state.clearFilter()
		return true, false
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		state.filterBackspace()
		return true, false
	case tcell.KeyRune:
		state.appendFilterRune(e.Rune())
		return true, false
	}
	return false, false
}

// handleKillConfirmKey applies a single keypress while the kill-
// confirmation prompt (opened via 'x') has focus. y/Y are checked on the
// raw rune, like q/Q in handleNormalKey, to distinguish SIGTERM from
// SIGKILL — handleRuneKey's lowercasing dispatch is not used here. Any
// other rune is ignored rather than treated as quit or cancel, so a stray
// keypress can't slip past the confirm step. Ctrl+C still force-quits,
// matching handleFilterKey's escape hatch.
func handleKillConfirmKey(state *monitorState, e *tcell.EventKey) (redraw, quit bool) {
	switch e.Key() {
	case tcell.KeyCtrlC:
		return false, true
	case tcell.KeyEscape:
		state.cancelKillConfirm()
		return true, false
	case tcell.KeyRune:
		switch e.Rune() {
		case 'y':
			state.sendKillSignal(syscall.SIGTERM)
			return true, false
		case 'Y':
			state.sendKillSignal(syscall.SIGKILL)
			return true, false
		case 'n', 'N':
			state.cancelKillConfirm()
			return true, false
		}
	}
	return false, false
}
