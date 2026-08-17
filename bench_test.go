package main

import (
	"testing"

	"github.com/gdamore/tcell/v2"
)

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
