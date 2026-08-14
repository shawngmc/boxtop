package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

// newTestScreen builds an in-process tcell SimulationScreen — this is what
// lets drawFrame's actual rendering (cursor highlight, footer text, styles)
// be asserted on in `go test`, in place of driving a real terminal by hand.
func newTestScreen(t *testing.T, w, h int) tcell.SimulationScreen {
	t.Helper()
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen.Init() = %v", err)
	}
	screen.SetSize(w, h)
	t.Cleanup(screen.Fini)
	return screen
}

// testFrameData builds a minimal frameData around a fixed process fixture —
// memory limit fields just need to be non-zero so drawFrame's percentage
// math doesn't divide by zero; CPU/OOM/model fields stay at their "not
// available" zero values so those optional sections are skipped.
func testFrameData(procs []Process) frameData {
	return frameData{
		maxBytes:  100 * 1024 * 1024,
		currBytes: 50 * 1024 * 1024,
		procs:     procs,
	}
}

// rowText reconstructs the visible text of screen row y by reading back
// every cell drawFrame wrote via SetContent (GetContent reads the pending
// "back" buffer, so no Show()/Sync() is needed first).
func rowText(screen tcell.Screen, w, y int) string {
	var b strings.Builder
	for x := 0; x < w; x++ {
		r, _, _, _ := screen.GetContent(x, y)
		if r == 0 {
			r = ' '
		}
		b.WriteRune(r)
	}
	return b.String()
}

// findRow scans every row for one containing needle, returning it along
// with whether it was found at all — "not found" is itself meaningful here
// (e.g. a row scrolled out of view).
func findRow(screen tcell.Screen, w, h int, needle string) (y int, ok bool) {
	for y = 0; y < h; y++ {
		if strings.Contains(rowText(screen, w, y), needle) {
			return y, true
		}
	}
	return 0, false
}

// rowReversed reports whether the cell at column 1 of row y (inside the
// fixed-width PID field every process row starts with) has the reverse-video
// attribute drawFrame applies to the cursor row.
func rowReversed(screen tcell.Screen, y int) bool {
	_, _, style, _ := screen.GetContent(1, y)
	_, _, attr := style.Decompose()
	return attr&tcell.AttrReverse != 0
}

func TestDrawFrameHighlightsCursorRow(t *testing.T) {
	screen := newTestScreen(t, 80, 20)
	state := newMonitorState()
	state.sortCol = sortPID
	state.sortReverse = false
	state.sortDirty = true
	state.cursor = 1 // second row once sorted by PID ascending: PID 2 ("bash")

	data := testFrameData([]Process{
		{PID: 1, Name: "init", NameLower: "init", Cmd: "/sbin/init"},
		{PID: 2, Name: "bash", NameLower: "bash", Cmd: "/bin/bash"},
		{PID: 3, Name: "sleep", NameLower: "sleep", Cmd: "sleep 300"},
	})

	drawFrame(screen, state, data)

	w, h := screen.Size()
	tests := []struct {
		name string
		want bool
	}{
		{"init", false},
		{"bash", true},
		{"sleep", false},
	}
	for _, tc := range tests {
		y, ok := findRow(screen, w, h, tc.name)
		if !ok {
			t.Fatalf("row for %q not found on screen", tc.name)
		}
		if got := rowReversed(screen, y); got != tc.want {
			t.Errorf("row %q reversed = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestDrawFrameAutoScrollsCursorIntoView(t *testing.T) {
	const n = 30
	procs := make([]Process, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("proc%02d", i)
		procs[i] = Process{PID: i + 1, Name: name, NameLower: name, Cmd: name}
	}

	screen := newTestScreen(t, 80, 12) // short terminal: only a few process rows fit
	state := newMonitorState()
	state.sortCol = sortPID
	state.sortReverse = false
	state.sortDirty = true
	state.cursor = n - 1 // last process — far below what scrollOffset 0 would show

	drawFrame(screen, state, testFrameData(procs))

	w, h := screen.Size()
	lastName := procs[n-1].Name
	y, ok := findRow(screen, w, h, lastName)
	if !ok {
		t.Fatalf("cursor row %q not visible after drawFrame — auto-scroll did not bring it into view", lastName)
	}
	if !rowReversed(screen, y) {
		t.Errorf("row %q is visible but not highlighted as the cursor row", lastName)
	}

	if _, ok := findRow(screen, w, h, procs[0].Name); ok {
		t.Errorf("first process %q is still visible; expected the viewport to have scrolled away from it", procs[0].Name)
	}
}

func TestDrawFrameFooterKillConfirmAndStatus(t *testing.T) {
	screen := newTestScreen(t, 100, 20)
	state := newMonitorState()
	data := testFrameData(nil)

	state.killConfirmMode = true
	state.killTargetPID = 4242
	state.killTargetName = "sleep"
	drawFrame(screen, state, data)

	w, h := screen.Size()
	want := "Kill PID 4242 (sleep)?  [y] SIGTERM   [Y] SIGKILL   [Esc/n] cancel"
	y, ok := findRow(screen, w, h, want)
	if !ok {
		t.Fatalf("kill-confirm footer text not found on screen")
	}
	_, _, style, _ := screen.GetContent(1, y)
	fg, _, attr := style.Decompose()
	if attr&tcell.AttrBold == 0 {
		t.Error("kill-confirm footer is not bold")
	}
	if fg == tcell.ColorDefault {
		t.Error("kill-confirm footer has no distinct (danger) foreground color")
	}

	state.killConfirmMode = false
	state.killStatusMsg = "Sent SIGTERM to PID 4242 (sleep)"
	drawFrame(screen, state, data)
	if _, ok := findRow(screen, w, h, state.killStatusMsg); !ok {
		t.Error("kill status message not found on screen")
	}
}

func TestDrawFrameDetailPopup(t *testing.T) {
	screen := newTestScreen(t, 100, 24)
	state := newMonitorState()
	data := testFrameData([]Process{
		{PID: 1, Name: "init", NameLower: "init", Cmd: "/sbin/init"},
	})

	cpu := 3.5
	state.detailMode = true
	state.detailData = ProcessDetail{
		PID: 4242, Name: "sleep", Cmd: "sleep 300", RSSKb: 2048, CPUPct: &cpu,
		HaveExtra: true, State: "Sleeping", PPID: 1, Threads: 2, User: "root (uid 0)",
	}
	drawFrame(screen, state, data)

	w, h := screen.Size()
	for _, want := range []string{
		"Process Detail: PID 4242",
		"Name:    sleep",
		"PPID:    1",
		"State:   Sleeping",
		"User:    root (uid 0)",
		"CPU%:    3.5%",
		"RSS:     2.0 MB",
		"Cmd:     sleep 300",
		"Enter/Esc/q: close",
	} {
		if _, ok := findRow(screen, w, h, want); !ok {
			t.Errorf("detail popup text %q not found on screen", want)
		}
	}

	if _, ok := findRow(screen, w, h, "Process detail — [Enter/Esc/q] close"); !ok {
		t.Error("main footer did not show the detail-mode hint")
	}
}
