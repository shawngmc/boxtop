package main

import "testing"

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
