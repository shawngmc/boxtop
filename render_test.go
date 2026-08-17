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
// the RAM meter just needs a non-zero total so drawFrame's percentage math
// doesn't divide by zero; CPU/Swap/OOM/model fields stay "unavailable" so
// those optional sections render their placeholder text instead of a bar.
func testFrameData(procs []Process) frameData {
	return frameData{
		cgroupMeters: [3]meter{
			meterRAM:  memMeter("RAM", 50*1024*1024, 100*1024*1024, ""),
			meterCPU:  unavailableMeter("CPU", "no cgroup cpu controller found"),
			meterSwap: unavailableMeter("SWAP", "no swap controller found"),
		},
		systemMeters: [3]meter{
			meterRAM:  unavailableMeter("RAM", "not available in tests"),
			meterCPU:  unavailableMeter("CPU", "not available in tests"),
			meterSwap: unavailableMeter("SWAP", "not available in tests"),
		},
		cgroupRAMMaxBytes: 100 * 1024 * 1024,
		procs:             procs,
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

	// Width stays >= narrowWidthThreshold so the top bar keeps its compact
	// side-by-side layout — this test is about vertical auto-scroll in a
	// short terminal, not the narrow stacked layout (covered separately).
	screen := newTestScreen(t, 120, 12) // short terminal: only a few process rows fit
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

// TestDrawFrameTruncatedListFoldsScrollIndicatorIntoFooterRule checks the
// compact "-----[Processes: first-last/total]-----" scroll-position
// indicator: when the process list doesn't fit, that indicator is folded
// into the footer's dashed rule line rather than spending a whole extra row
// on it, so every row above the footer is an actual process row.
func TestDrawFrameTruncatedListFoldsScrollIndicatorIntoFooterRule(t *testing.T) {
	const n = 60
	procs := make([]Process, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("proc%02d", i)
		procs[i] = Process{PID: i + 1, Name: name, NameLower: name, Cmd: name}
	}

	screen := newTestScreen(t, 80, 16)
	state := newMonitorState()
	state.sortCol = sortPID
	state.sortReverse = false
	state.sortDirty = true

	drawFrame(screen, state, testFrameData(procs))
	w, h := screen.Size()

	y, ok := findRow(screen, w, h, "[Processes: 1-")
	if !ok {
		t.Fatal("footer rule missing the \"[Processes: first-last/total]\" scroll indicator")
	}
	row := rowText(screen, w, y)
	if !strings.HasPrefix(strings.TrimRight(row, " "), "-----[Processes: 1-") {
		t.Errorf("scroll-indicator row = %q, want it to start with a dashed rule like \"-----[Processes: 1-N/%d]\"", row, n)
	}
	if !strings.Contains(row, fmt.Sprintf("/%d]", n)) {
		t.Errorf("scroll-indicator row = %q, want it to end the bracket with the total count /%d]", row, n)
	}

	if _, ok := findRow(screen, w, h, "scroll for more"); ok {
		t.Error("the old \"... showing N-M of T — scroll for more ...\" line should no longer be drawn")
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
	state.statusMsg = "Sent SIGTERM to PID 4242 (sleep)"
	drawFrame(screen, state, data)
	if _, ok := findRow(screen, w, h, state.statusMsg); !ok {
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

func TestDrawFrameHelpPopup(t *testing.T) {
	screen := newTestScreen(t, 100, 24)
	state := newMonitorState()
	data := testFrameData([]Process{
		{PID: 1, Name: "init", NameLower: "init", Cmd: "/sbin/init"},
	})

	state.helpMode = true
	drawFrame(screen, state, data)

	w, h := screen.Size()
	for _, want := range []string{
		"Keybindings",
		"Navigation",
		"move the cursor",
		"Sorting",
		"Filter",
		"Process actions",
		"kill selected process",
		"this help screen",
		"quit (or close a popup)",
	} {
		if _, ok := findRow(screen, w, h, want); !ok {
			t.Errorf("help popup text %q not found on screen", want)
		}
	}

	if _, ok := findRow(screen, w, h, "Keybinding help — [Enter/Esc/q] close"); !ok {
		t.Error("main footer did not show the help-mode hint")
	}
}

func TestDrawFrameFooterMentionsHelpKey(t *testing.T) {
	screen := newTestScreen(t, 100, 24)
	state := newMonitorState()
	data := testFrameData([]Process{
		{PID: 1, Name: "init", NameLower: "init", Cmd: "/sbin/init"},
	})
	drawFrame(screen, state, data)

	w, h := screen.Size()
	if _, ok := findRow(screen, w, h, "Help: [h]"); !ok {
		t.Error("default footer did not advertise the help key")
	}
}

func TestDrawFrameFooterMentionsCgroupKey(t *testing.T) {
	screen := newTestScreen(t, 200, 24)
	state := newMonitorState()
	data := testFrameData([]Process{
		{PID: 1, Name: "init", NameLower: "init", Cmd: "/sbin/init"},
	})
	drawFrame(screen, state, data)

	w, h := screen.Size()
	if _, ok := findRow(screen, w, h, "Cgroup: [g]"); !ok {
		t.Error("default footer did not advertise the cgroup-picker key")
	}
}

func TestDrawFrameCgroupSelectPopup(t *testing.T) {
	screen := newTestScreen(t, 100, 24)
	state := newMonitorState()
	data := testFrameData([]Process{
		{PID: 1, Name: "init", NameLower: "init", Cmd: "/sbin/init"},
	})

	state.cgroupSelectMode = true
	state.cgroupSelectAll = []string{"docker/1a2b3c4d5e6f", "system.slice/foo.service"}
	drawFrame(screen, state, data)

	w, h := screen.Size()
	for _, want := range []string{
		"Select cgroup",
		cgroupSelectDefaultLabel,
		"docker/1a2b3c4d5e6f",
		"system.slice/foo.service",
	} {
		if _, ok := findRow(screen, w, h, want); !ok {
			t.Errorf("cgroup select popup text %q not found on screen", want)
		}
	}
	if _, ok := findRow(screen, w, h, "Select cgroup: type to search"); !ok {
		t.Error("main footer did not show the cgroup-select-mode hint")
	}

	// Typing narrows the list and jumps the cursor back to the top.
	state.cgroupSelectCursor = 1
	state.cgroupSelectAppendRune('f')
	state.cgroupSelectAppendRune('o')
	state.cgroupSelectAppendRune('o')
	drawFrame(screen, state, data)
	if _, ok := findRow(screen, w, h, "docker/1a2b3c4d5e6f"); ok {
		t.Error("cgroup select popup still shows a name that doesn't match the filter")
	}
	if _, ok := findRow(screen, w, h, "system.slice/foo.service"); !ok {
		t.Error("cgroup select popup dropped a name that matches the filter")
	}
}

func TestMemMeter(t *testing.T) {
	m := memMeter("RAM", 512*1024*1024, 1024*1024*1024, "")
	if !m.have || m.frac != 0.5 {
		t.Errorf("memMeter: have=%v frac=%v, want have=true frac=0.5", m.have, m.frac)
	}
	if m.detail != "512M/1.0G" {
		t.Errorf("memMeter detail = %q, want %q", m.detail, "512M/1.0G")
	}
	if m.pctText != " 50.0%" {
		t.Errorf("memMeter pctText = %q, want %q", m.pctText, " 50.0%")
	}

	suffixed := memMeter("RAM", 100, 200, " (host)")
	if !strings.HasSuffix(suffixed.detail, " (host)") {
		t.Errorf("memMeter with suffix = %q, want it to end with \" (host)\"", suffixed.detail)
	}

	zero := memMeter("SWAP", 0, 0, "")
	if zero.frac != 0 {
		t.Errorf("memMeter with zero total: frac = %v, want 0 (no divide-by-zero)", zero.frac)
	}
}

func TestFracGlyph(t *testing.T) {
	tests := []struct {
		frac float64
		want rune
	}{
		{-0.1, ' '},
		{0, ' '},
		{0.05, ' '},
		{0.1, '▏'},
		{0.2, '▎'},
		{0.5, '▌'},
		{0.9, '▉'},
		{0.99, '█'},
		{1, '█'},
		{1.5, '█'},
	}
	for _, tc := range tests {
		if got := fracGlyph(tc.frac); got != tc.want {
			t.Errorf("fracGlyph(%v) = %q, want %q", tc.frac, got, tc.want)
		}
	}
}

func TestSparkGlyph(t *testing.T) {
	tests := []struct {
		frac float64
		want rune
	}{
		{-0.1, '▁'},
		{0, '▁'},
		{0.05, '▁'},
		{0.13, '▂'},
		{0.5, '▅'},
		{0.99, '█'},
		{1, '█'},
		{1.5, '█'},
	}
	for _, tc := range tests {
		if got := sparkGlyph(tc.frac); got != tc.want {
			t.Errorf("sparkGlyph(%v) = %q, want %q", tc.frac, got, tc.want)
		}
	}
}

func TestDrawBarPartialCell(t *testing.T) {
	screen := newTestScreen(t, 20, 1)
	drawBar(screen, 0, 0, 10, 0.55, summaryStops)
	got := rowText(screen, 10, 0)
	want := "█████▌    "
	if got != want {
		t.Errorf("drawBar partial-cell row = %q, want %q", got, want)
	}

	_, _, filledStyle, _ := screen.GetContent(0, 0)
	if fg, _, _ := filledStyle.Decompose(); fg == tcell.ColorDefault {
		t.Error("filled bar cell has no distinct gradient foreground color")
	}
	_, _, boundaryStyle, _ := screen.GetContent(5, 0)
	if fg, _, _ := boundaryStyle.Decompose(); fg == tcell.ColorDefault {
		t.Error("boundary (partial) bar cell has no distinct gradient foreground color")
	}
	_, _, emptyStyle, _ := screen.GetContent(6, 0)
	if emptyStyle != tcell.StyleDefault {
		t.Errorf("empty bar cell style = %v, want tcell.StyleDefault (no explicit color)", emptyStyle)
	}
}

func TestDrawMeterBracketsUncolored(t *testing.T) {
	screen := newTestScreen(t, 40, 1)
	m := memMeter("RAM", 512*1024*1024, 1024*1024*1024, "")
	drawMeter(screen, 0, 0, 40, m, nil)

	row := []rune(rowText(screen, 40, 0))
	openIdx, closeIdx := -1, -1
	for i, r := range row {
		switch r {
		case '[':
			openIdx = i
		case ']':
			closeIdx = i
		}
	}
	if openIdx < 0 || closeIdx < 0 {
		t.Fatalf("meter row %q missing bar brackets", string(row))
	}

	_, _, openStyle, _ := screen.GetContent(openIdx, 0)
	if openStyle != tcell.StyleDefault {
		t.Errorf("opening bracket style = %v, want tcell.StyleDefault (no explicit color)", openStyle)
	}
	_, _, closeStyle, _ := screen.GetContent(closeIdx, 0)
	if closeStyle != tcell.StyleDefault {
		t.Errorf("closing bracket style = %v, want tcell.StyleDefault (no explicit color)", closeStyle)
	}

	pctIdx := closeIdx + 2 // "] " then the percentage text
	_, _, pctStyle, _ := screen.GetContent(pctIdx, 0)
	if fg, _, _ := pctStyle.Decompose(); fg == tcell.ColorDefault {
		t.Error("percentage text has no distinct gradient foreground color")
	}
}

func TestDrawSparklineColorsPerSample(t *testing.T) {
	screen := newTestScreen(t, 6, 1)
	samples := []float64{0, 1}
	drawSparkline(screen, 0, 0, 5, samples, summaryStops)

	// Only 2 of the 5 requested cells have samples, so the sparkline
	// right-aligns them: 3 blank padding cells, then the two samples.
	for x := 0; x < 3; x++ {
		r, _, style, _ := screen.GetContent(x, 0)
		if r != ' ' || style != emptyBarStyle {
			t.Errorf("padding cell %d = %q/%v, want blank/emptyBarStyle", x, r, style)
		}
	}

	r, _, style, _ := screen.GetContent(3, 0)
	if r != '▁' {
		t.Errorf("first sample glyph = %q, want '▁' (frac 0)", r)
	}
	if fg, _, _ := style.Decompose(); fg == tcell.ColorDefault {
		t.Error("first sample cell has no distinct gradient foreground color")
	}

	r, _, style, _ = screen.GetContent(4, 0)
	if r != '█' {
		t.Errorf("second sample glyph = %q, want '█' (frac 1)", r)
	}
	if fg, _, _ := style.Decompose(); fg == tcell.ColorDefault {
		t.Error("second sample cell has no distinct gradient foreground color")
	}
}

func TestDrawSparklineMostRecentSampleIsRightmost(t *testing.T) {
	screen := newTestScreen(t, 3, 1)
	// More samples than the available width: only the most recent 3
	// should be drawn, oldest-first left to right, newest at the right.
	samples := []float64{0, 0.5, 0.99, 1}
	drawSparkline(screen, 0, 0, 3, samples, summaryStops)

	got := rowText(screen, 3, 0)
	want := string([]rune{sparkGlyph(0.5), sparkGlyph(0.99), sparkGlyph(1)})
	if got != want {
		t.Errorf("drawSparkline row = %q, want %q (dropped the oldest sample, kept newest rightmost)", got, want)
	}
}

// TestDrawMeterSparklineBesideBar covers the sparkline's placement when a
// meter is drawn with history: bracketed, meterBarWidth wide, immediately
// before the live bar's own brackets, at the same width — "trend, then
// current value," both readable at a glance without a separate row.
func TestDrawMeterSparklineBesideBar(t *testing.T) {
	screen := newTestScreen(t, 60, 1)
	m := memMeter("RAM", 512*1024*1024, 1024*1024*1024, "")
	hist := []float64{0.2, 0.4, 0.6}
	drawMeter(screen, 0, 0, 60, m, hist)

	row := []rune(rowText(screen, 60, 0))
	var brackets []int
	for i, r := range row {
		if r == '[' || r == ']' {
			brackets = append(brackets, i)
		}
	}
	if len(brackets) != 4 {
		t.Fatalf("meter row %q has %d bracket chars, want 4 (sparkline + bar)", string(row), len(brackets))
	}
	sparkOpen, sparkClose, barOpen, barClose := brackets[0], brackets[1], brackets[2], brackets[3]
	if sparkClose-sparkOpen-1 != meterBarWidth {
		t.Errorf("sparkline bracket span = %d, want meterBarWidth (%d)", sparkClose-sparkOpen-1, meterBarWidth)
	}
	if barClose-barOpen-1 != meterBarWidth {
		t.Errorf("bar bracket span = %d, want meterBarWidth (%d)", barClose-barOpen-1, meterBarWidth)
	}
	if barOpen <= sparkClose {
		t.Errorf("live bar brackets (open at %d) should come after the sparkline brackets (close at %d)", barOpen, sparkClose)
	}
}

// TestDrawMeterOmitsSparklineWhenHistoryEmpty covers the "fully hidden if
// unavailable" requirement: with no history samples, no sparkline brackets
// are drawn at all (not even an empty "[]" stub), so the row is exactly what
// it was before the sparkline feature existed.
func TestDrawMeterOmitsSparklineWhenHistoryEmpty(t *testing.T) {
	screen := newTestScreen(t, 60, 1)
	m := memMeter("RAM", 512*1024*1024, 1024*1024*1024, "")
	drawMeter(screen, 0, 0, 60, m, nil)

	row := []rune(rowText(screen, 60, 0))
	brackets := 0
	for _, r := range row {
		if r == '[' || r == ']' {
			brackets++
		}
	}
	if brackets != 2 {
		t.Errorf("meter row %q has %d bracket chars, want 2 (bar only, no sparkline)", string(row), brackets)
	}
}

// TestDrawMeterHidesSparklineWhenColumnTooNarrow covers the regression this
// guards against: on a plain 100-col terminal the cgroup/system split gives
// each column only ~49 cells, too narrow for a 20-wide sparkline plus a
// 20-wide bar plus the percentage text. Without this guard, the percentage
// would be pushed past the column's right edge and silently overwritten by
// the next column's own drawing — so instead the sparkline is dropped and
// the percentage always stays visible.
func TestDrawMeterHidesSparklineWhenColumnTooNarrow(t *testing.T) {
	screen := newTestScreen(t, 49, 1)
	m := memMeter("RAM", 512*1024*1024, 1024*1024*1024, "")
	hist := []float64{0.2, 0.4, 0.6}
	drawMeter(screen, 0, 0, 49, m, hist)

	row := rowText(screen, 49, 0)
	if strings.ContainsRune(row, '▂') || strings.ContainsRune(row, '▃') ||
		strings.ContainsRune(row, '▄') || strings.ContainsRune(row, '▅') {
		t.Errorf("meter row %q drew a sparkline despite too-narrow column", row)
	}
	if !strings.Contains(row, "50.0%") {
		t.Errorf("meter row %q missing the percentage text, want it always visible even when narrow", row)
	}
}

func TestUnavailableMeter(t *testing.T) {
	m := unavailableMeter("SWAP", "no swap controller found")
	if m.have {
		t.Error("unavailableMeter: have = true, want false")
	}
	if m.unavailableText != "no swap controller found" {
		t.Errorf("unavailableMeter reason = %q, want %q", m.unavailableText, "no swap controller found")
	}
}

func TestCgroupCPUMeter(t *testing.T) {
	if m := cgroupCPUMeter(0, "", false, nil); m.have {
		t.Error("cgroupCPUMeter with haveLimit=false: have = true, want false")
	}

	measuring := cgroupCPUMeter(2, cpuSourceQuota, true, nil)
	if !measuring.have || !measuring.measuring {
		t.Errorf("cgroupCPUMeter with nil pct: have=%v measuring=%v, want have=true measuring=true", measuring.have, measuring.measuring)
	}
	if measuring.detail != "2.00 cores (quota)" {
		t.Errorf("cgroupCPUMeter detail = %q, want %q", measuring.detail, "2.00 cores (quota)")
	}

	pct := 42.5
	m := cgroupCPUMeter(2, cpuSourceCPUSet, true, &pct)
	if m.measuring || m.frac != 0.425 {
		t.Errorf("cgroupCPUMeter: measuring=%v frac=%v, want measuring=false frac=0.425", m.measuring, m.frac)
	}
	if m.detail != "2.00 cores (cpuset)" {
		t.Errorf("cgroupCPUMeter detail = %q, want %q", m.detail, "2.00 cores (cpuset)")
	}
}

func TestSystemCPUMeter(t *testing.T) {
	if m := systemCPUMeter(nil, false, 4); m.have {
		t.Error("systemCPUMeter with ok=false: have = true, want false")
	}

	measuring := systemCPUMeter(nil, true, 4)
	if !measuring.have || !measuring.measuring || measuring.detail != "4 cores" {
		t.Errorf("systemCPUMeter measuring case = %+v, want have=true measuring=true detail=\"4 cores\"", measuring)
	}

	pct := 10.0
	m := systemCPUMeter(&pct, true, 4)
	if m.measuring || m.frac != 0.1 {
		t.Errorf("systemCPUMeter: measuring=%v frac=%v, want measuring=false frac=0.1", m.measuring, m.frac)
	}
}

func TestBuildCPUInfoLine(t *testing.T) {
	if got := buildCPUInfoLine("", false, 0, 0, false, false); got != "" {
		t.Errorf("buildCPUInfoLine with nothing available = %q, want empty string", got)
	}
	if got := buildCPUInfoLine("Generic CPU", true, 0, 0, false, false); got != "CPU: Generic CPU" {
		t.Errorf("buildCPUInfoLine model-only = %q", got)
	}
	got := buildCPUInfoLine("Generic CPU", true, 2400, 3700, true, true)
	want := "CPU: Generic CPU  |  2400/3700 MHz (cur/max)"
	if got != want {
		t.Errorf("buildCPUInfoLine = %q, want %q", got, want)
	}
}

// TestDrawFrameShowsCgroupAndSystemMeters checks the restructured top bar
// renders both scopes' RAM meters (the "cgroup"/"system" column split is
// the core of this feature — see the CPU/SWAP meter unit tests above for
// the formatting details each row shares).
func TestDrawFrameShowsCgroupAndSystemMeters(t *testing.T) {
	screen := newTestScreen(t, 100, 24)
	state := newMonitorState()
	data := testFrameData(nil)
	drawFrame(screen, state, data)

	w, h := screen.Size()
	if _, ok := findRow(screen, w, h, "cgroup"); !ok {
		t.Error("top bar missing the \"cgroup\" column header")
	}
	if _, ok := findRow(screen, w, h, "system"); !ok {
		t.Error("top bar missing the \"system\" column header")
	}
	if y, ok := findRow(screen, w, h, "RAM"); ok {
		if !strings.Contains(rowText(screen, w, y), "50.0%") {
			t.Errorf("RAM meter row %q missing expected 50.0%% (testFrameData is 50/100 MB)", rowText(screen, w, y))
		}
	} else {
		t.Error("top bar missing the RAM meter row")
	}
	if _, ok := findRow(screen, w, h, "unavailable (not available in tests)"); !ok {
		t.Error("top bar missing an \"unavailable\" system meter row")
	}
}

// TestDrawFrameSideBySideAboveWidthThreshold covers the default wide-
// terminal layout: cgroup and system headers land on the same row, split
// left/right at the midpoint.
func TestDrawFrameSideBySideAboveWidthThreshold(t *testing.T) {
	screen := newTestScreen(t, 140, 24)
	state := newMonitorState()
	drawFrame(screen, state, testFrameData(nil))

	w, h := screen.Size()
	cgroupY, ok := findRow(screen, w, h, "cgroup")
	if !ok {
		t.Fatal("top bar missing the \"cgroup\" column header")
	}
	systemY, ok := findRow(screen, w, h, "system")
	if !ok {
		t.Fatal("top bar missing the \"system\" column header")
	}
	if cgroupY != systemY {
		t.Errorf("cgroup header row %d, system header row %d, want the same row (side by side) at width %d", cgroupY, systemY, w)
	}
}

// TestDrawFrameStacksBelowWidthThreshold covers the auto-narrow trigger:
// under narrowWidthThreshold columns, the system block moves below the
// cgroup block (both full width) instead of splitting the row in half.
func TestDrawFrameStacksBelowWidthThreshold(t *testing.T) {
	screen := newTestScreen(t, narrowWidthThreshold-1, 24)
	state := newMonitorState()
	drawFrame(screen, state, testFrameData(nil))

	w, h := screen.Size()
	cgroupY, ok := findRow(screen, w, h, "cgroup")
	if !ok {
		t.Fatal("top bar missing the \"cgroup\" column header")
	}
	systemY, ok := findRow(screen, w, h, "system")
	if !ok {
		t.Fatal("top bar missing the \"system\" column header")
	}
	// cgroup's header + 3 meter rows (RAM/CPU/SWAP) come first, so system's
	// header lands exactly 4 rows below cgroup's.
	if want := cgroupY + 4; systemY != want {
		t.Errorf("system header row = %d, want %d (stacked 4 rows below cgroup's at width %d)", systemY, want, w)
	}
}

// TestDrawFrameForceNarrowStacksEvenWhenWide covers --narrow: it stacks the
// layout even on a terminal wide enough for the side-by-side split.
func TestDrawFrameForceNarrowStacksEvenWhenWide(t *testing.T) {
	screen := newTestScreen(t, 200, 24)
	state := newMonitorState()
	state.forceNarrow = true
	drawFrame(screen, state, testFrameData(nil))

	w, h := screen.Size()
	cgroupY, ok := findRow(screen, w, h, "cgroup")
	if !ok {
		t.Fatal("top bar missing the \"cgroup\" column header")
	}
	systemY, ok := findRow(screen, w, h, "system")
	if !ok {
		t.Fatal("top bar missing the \"system\" column header")
	}
	if want := cgroupY + 4; systemY != want {
		t.Errorf("system header row = %d, want %d (--narrow should stack even at width %d)", systemY, want, w)
	}
}

// TestDrawFrameShowsSparklinesBesideMeters covers the sparkline restructure:
// RAM/CPU history is drawn inline on each meter's own row (bracketed, beside
// the live bar) rather than on a separate row below the three meters. Uses a
// wide screen so each split column comfortably fits both bracketed bars —
// see TestDrawMeterHidesSparklineWhenColumnTooNarrow for the narrower case.
func TestDrawFrameShowsSparklinesBesideMeters(t *testing.T) {
	screen := newTestScreen(t, 160, 24)
	state := newMonitorState()
	data := testFrameData(nil)
	data.cgroupRAMHistory = []float64{0.2, 0.4, 0.6}
	data.cgroupCPUHistory = []float64{0.2, 0.4, 0.6}
	data.systemRAMHistory = []float64{0.2, 0.4, 0.6}
	data.systemCPUHistory = []float64{0.2, 0.4, 0.6}
	drawFrame(screen, state, data)

	w, h := screen.Size()
	y, ok := findRow(screen, w, h, "RAM")
	if !ok {
		t.Fatal("top bar missing the RAM meter row")
	}
	row := rowText(screen, w, y)
	if !strings.ContainsRune(row, '▂') && !strings.ContainsRune(row, '▃') &&
		!strings.ContainsRune(row, '▄') && !strings.ContainsRune(row, '▅') {
		t.Errorf("RAM meter row %q has no sparkline glyph for the populated samples", row)
	}
	// header(y-1), RAM(y), CPU(y+1), SWAP(y+2), cpuInfo(y+3), "=" rule
	// (y+4) — confirming the rule lands exactly there (not one row lower)
	// shows no leftover history row was inserted between the meters and it.
	if got := rowText(screen, w, y+4); !strings.HasPrefix(got, "=") {
		t.Errorf("\"=\" separator rule row = %q, want it to start with \"=\" at row %d (no stray row between meters and it)", got, y+4)
	}
}

// TestDrawFrameHidesCgroupHistoryWhenUnconfined mirrors
// TestDrawFrameHidesCgroupSectionWhenUnconfined for the sparklines: when
// there's no meaningful cgroup confinement, only the full-width system RAM
// row should carry a sparkline, not a cgroup-side one too.
func TestDrawFrameHidesCgroupHistoryWhenUnconfined(t *testing.T) {
	screen := newTestScreen(t, 100, 24)
	state := newMonitorState()
	data := testFrameData(nil)
	data.cgroupHidden = true
	data.systemMeters[meterRAM] = memMeter("RAM", 50*1024*1024, 100*1024*1024, "")
	data.systemRAMHistory = []float64{0.2, 0.4, 0.6}
	data.systemCPUHistory = []float64{0.2, 0.4, 0.6}
	drawFrame(screen, state, data)

	w, h := screen.Size()
	y, ok := findRow(screen, w, h, "RAM")
	if !ok {
		t.Fatal("top bar missing the RAM meter row")
	}
	row := rowText(screen, w, y)
	if !strings.ContainsRune(row, '▂') && !strings.ContainsRune(row, '▃') &&
		!strings.ContainsRune(row, '▄') && !strings.ContainsRune(row, '▅') {
		t.Errorf("RAM meter row %q has no sparkline glyph for the populated samples", row)
	}
	// Only the single (system) RAM row should exist — cgroupHidden means
	// there's no cgroup column at all to carry a second sparkline.
	for y2 := y + 1; y2 < h; y2++ {
		if strings.Contains(rowText(screen, w, y2), "RAM") {
			t.Error("found an unexpected second RAM meter row when cgroupHidden is set")
			break
		}
	}
}

// TestDrawFrameHidesCgroupSectionWhenUnconfined covers the fix for the case
// where boxtop isn't running under any real cgroup confinement (bare host,
// no container, no systemd resource limits): rather than show a "cgroup"
// column whose numbers come from a different accounting method than
// "system" (cgroup memory.current counts reclaimable page cache as used;
// /proc/meminfo's MemAvailable-derived figure doesn't) and so never quite
// agrees with it, the whole column is omitted and system gets full width.
func TestDrawFrameHidesCgroupSectionWhenUnconfined(t *testing.T) {
	screen := newTestScreen(t, 100, 24)
	state := newMonitorState()
	data := testFrameData(nil)
	data.cgroupHidden = true
	drawFrame(screen, state, data)

	w, h := screen.Size()
	// rowText(0) is the top bar's header row — checked directly (rather
	// than via findRow, whose substring search would also match the
	// unrelated "(cgroup usage may include cache/shared mem not in RSS)"
	// footer note) to confirm the "cgroup" column header itself is gone.
	if strings.Contains(rowText(screen, w, 0), "cgroup") {
		t.Errorf("top bar header row should not show a \"cgroup\" column when cgroupHidden is set, got: %q", rowText(screen, w, 0))
	}
	if _, ok := findRow(screen, w, h, "system"); !ok {
		t.Error("top bar missing the \"system\" column header")
	}
}

// TestDrawFrameShowsCgroupName covers the header label added for a process
// actually confined to a named cgroup (e.g. inside a container).
func TestDrawFrameShowsCgroupName(t *testing.T) {
	screen := newTestScreen(t, 100, 24)
	state := newMonitorState()
	data := testFrameData(nil)
	data.cgroupName = "docker/1a2b3c4d5e6f"
	drawFrame(screen, state, data)

	w, h := screen.Size()
	if _, ok := findRow(screen, w, h, "cgroup (docker/1a2b3c4d5e6f)"); !ok {
		t.Error("top bar missing the cgroup name label")
	}
}

// TestCollectFrameAppendsSystemRAMHistory documents that collectFrame feeds
// the sparkline history buffers, once per call, from the same values it
// computes for the bar meters — /proc/meminfo is always readable on a
// Linux test host, so systemRAMHistory should grow by exactly one sample
// per call with no gating.
func TestCollectFrameAppendsSystemRAMHistory(t *testing.T) {
	state := newMonitorState()

	if _, err := collectFrame(state); err != nil {
		t.Fatalf("collectFrame: %v", err)
	}
	if got := len(state.systemRAMHistory.recent(sparkHistoryLen)); got != 1 {
		t.Fatalf("systemRAMHistory length after 1st collectFrame = %d, want 1", got)
	}

	if _, err := collectFrame(state); err != nil {
		t.Fatalf("collectFrame: %v", err)
	}
	if got := len(state.systemRAMHistory.recent(sparkHistoryLen)); got != 2 {
		t.Errorf("systemRAMHistory length after 2nd collectFrame = %d, want 2", got)
	}
}

// TestCollectFrameGatesCPUHistoryOnBaseline documents the deliberate gap in
// systemCPUHistory: sampleHostCPUPct returns no percentage on its first
// call (no baseline yet, see TestSampleHostCPUPctBaseline in
// state_test.go), so collectFrame must skip that tick's push rather than
// record a synthetic 0 — a fake dip that isn't a real sample.
func TestCollectFrameGatesCPUHistoryOnBaseline(t *testing.T) {
	state := newMonitorState()

	if _, err := collectFrame(state); err != nil {
		t.Fatalf("collectFrame: %v", err)
	}
	if got := len(state.systemCPUHistory.recent(sparkHistoryLen)); got != 0 {
		t.Errorf("systemCPUHistory length after 1st collectFrame = %d, want 0 (no baseline yet)", got)
	}

	if _, err := collectFrame(state); err != nil {
		t.Fatalf("collectFrame: %v", err)
	}
	if got := len(state.systemCPUHistory.recent(sparkHistoryLen)); got != 1 {
		t.Errorf("systemCPUHistory length after 2nd collectFrame = %d, want 1", got)
	}
}
