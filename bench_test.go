package main

import (
	"io"
	"testing"

	"github.com/gdamore/tcell/v2"
)

// BenchmarkNewMonitorState measures the allocation cost of standing up a
// fresh monitorState (the map/slice initialization in newMonitorState),
// isolated from any per-tick /proc or /sys work — this is what run() and
// runNonInteractive() both pay exactly once at startup.
func BenchmarkNewMonitorState(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stateSink = newMonitorState()
	}
}

// stateSink pins newMonitorState's result to the heap for the benchmark
// above — assigning to a package-level var (instead of _) keeps escape
// analysis from proving the result unused and stack-allocating it, which
// would otherwise silently zero out the reported allocation numbers.
var stateSink *monitorState

// BenchmarkTimeToFirstPaint measures the same sequence run()'s interactive
// path pays before its very first screen.Show() (see main.go's run()): a
// fresh monitorState, one collectFrame poll, drawFrame, and Show against a
// simulated screen. This is the closest in-process proxy for interactive
// cold-start latency available without a real terminal — collectFrame and
// drawFrame are the same code the real TUI runs, just against
// tcell.NewSimulationScreen instead of a live tty.
func BenchmarkTimeToFirstPaint(b *testing.B) {
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		b.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(120, 40)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		state := newMonitorState()
		data, err := collectFrame(state)
		if err != nil {
			b.Fatal(err)
		}
		drawFrame(screen, state, data)
		screen.Show()
	}
}

// BenchmarkNonInteractiveSnapshot measures writeNonInteractiveFrame's
// plain-text formatting cost — what runNonInteractive pays every time it
// prints its one snapshot, and the non-interactive equivalent of
// BenchmarkDrawFrame's tcell repaint.
func BenchmarkNonInteractiveSnapshot(b *testing.B) {
	state := newMonitorState()
	data, err := collectFrame(state)
	if err != nil {
		b.Fatal(err)
	}
	state.sortProcesses(data.procs)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		writeNonInteractiveFrame(io.Discard, state, data, 120)
	}
}

// BenchmarkBuildProcesses measures the per-tick hot path against the live
// /proc of the machine running the benchmark.
func BenchmarkBuildProcesses(b *testing.B) {
	state := newMonitorState()
	buildProcesses(state, 1.0) // warm caches (cmdline, cpu baseline)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buildProcesses(state, 1.0)
	}
}

// BenchmarkCollectFrame measures the whole per-tick poll — every cgroup/host
// stat read plus buildProcesses — against the live /proc and /sys of the
// machine running the benchmark. This is what the ticker in run() actually
// pays every interval, so it's the number that matters for steady-state CPU
// use, not just BenchmarkBuildProcesses in isolation.
func BenchmarkCollectFrame(b *testing.B) {
	state := newMonitorState()
	_, _ = collectFrame(state) // warm caches (cmdline, cpu baselines)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = collectFrame(state)
	}
}

// BenchmarkDrawFrame measures the redraw hot path against a real process
// snapshot: every tick pays this in addition to collectFrame, and so does
// every scroll/sort/filter keystroke, which redraws the cached snapshot
// without repolling.
func BenchmarkDrawFrame(b *testing.B) {
	state := newMonitorState()
	data, err := collectFrame(state)
	if err != nil {
		b.Fatal(err)
	}

	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		b.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(120, 40)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		drawFrame(screen, state, data)
	}
}
