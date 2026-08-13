// Command boxtop is a cgroup-aware memory monitor with a per-process
// breakdown, similar to top/htop but reporting against a container's
// cgroup memory limit rather than the host's total RAM. This is the
// tcell-based port of boxtop.py — see the package comment in each file
// for what maps to which piece of the original script.
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/gdamore/tcell/v2"
)

func main() {
	interval := time.Second
	if len(os.Args) > 1 {
		secs, err := strconv.ParseFloat(os.Args[1], 64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Usage: %s [refresh_interval_seconds]\n", os.Args[0])
			os.Exit(1)
		}
		interval = time.Duration(secs * float64(time.Second))
	}
	if interval <= 0 {
		interval = time.Second
	}

	if err := run(interval); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func run(interval time.Duration) error {
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
		switch e.Key() {
		case tcell.KeyCtrlC, tcell.KeyEscape:
			return false, true
		case tcell.KeyUp:
			state.scrollUp()
		case tcell.KeyDown:
			state.scrollDown()
		case tcell.KeyPgUp:
			state.pageUp()
		case tcell.KeyPgDn:
			state.pageDown()
		case tcell.KeyHome:
			state.scrollHome()
		case tcell.KeyEnd:
			state.scrollEnd()
		case tcell.KeyRune:
			r := e.Rune()
			if r == 'q' || r == 'Q' {
				return false, true
			}
			state.handleRuneKey(r)
		default:
			return false, false
		}
		return true, false
	case *tcell.EventMouse:
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

// mouseWheelStep is how many rows one wheel notch scrolls.
const mouseWheelStep = 3
