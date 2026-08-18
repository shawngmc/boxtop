package main

import "testing"

func TestParseBenchOutput(t *testing.T) {
	output := `goos: linux
goarch: amd64
pkg: boxtop
cpu: QEMU Virtual CPU version 2.5+
BenchmarkBuildProcesses-4        2522        464409 ns/op       25112 B/op        616 allocs/op
BenchmarkBuildProcesses-4        2400        470000 ns/op       25200 B/op        620 allocs/op
BenchmarkCollectFrame-4          2010        594450 ns/op      151910 B/op        759 allocs/op
BenchmarkNewMonitorState-4     377199          2843 ns/op       37776 B/op          6 allocs/op
PASS
ok  	boxtop	7.820s
`
	lines, err := parseBenchOutput(output)
	if err != nil {
		t.Fatalf("parseBenchOutput: %v", err)
	}
	if len(lines) != 4 {
		t.Fatalf("got %d parsed lines, want 4: %+v", len(lines), lines)
	}

	want := benchLine{name: "BenchmarkCollectFrame", nsPerOp: 594450, bytesPerOp: 151910, allocsPerOp: 759}
	if lines[2] != want {
		t.Errorf("lines[2] = %+v, want %+v", lines[2], want)
	}

	for _, l := range lines {
		if l.name == "" {
			t.Errorf("empty name in parsed line %+v", l)
		}
		for _, r := range l.name {
			if r == '-' {
				t.Errorf("name %q still carries a GOMAXPROCS suffix", l.name)
			}
		}
	}
}

func TestParseBenchOutputIgnoresNonBenchLines(t *testing.T) {
	lines, err := parseBenchOutput("PASS\nok  \tboxtop\t0.5s\n")
	if err != nil {
		t.Fatalf("parseBenchOutput: %v", err)
	}
	if len(lines) != 0 {
		t.Errorf("got %d lines, want 0: %+v", len(lines), lines)
	}
}

func TestAggregateBenchmarksAveragesRepeats(t *testing.T) {
	lines := []benchLine{
		{name: "BenchmarkBuildProcesses", nsPerOp: 100, bytesPerOp: 10, allocsPerOp: 1},
		{name: "BenchmarkCollectFrame", nsPerOp: 500, bytesPerOp: 50, allocsPerOp: 5},
		{name: "BenchmarkBuildProcesses", nsPerOp: 200, bytesPerOp: 20, allocsPerOp: 3},
	}
	stats := aggregateBenchmarks(lines)
	if len(stats) != 2 {
		t.Fatalf("got %d stats, want 2: %+v", len(stats), stats)
	}

	// Order should follow first-seen order, not input grouping.
	if stats[0].Name != "BenchmarkBuildProcesses" || stats[1].Name != "BenchmarkCollectFrame" {
		t.Fatalf("unexpected order: %+v", stats)
	}

	got := stats[0]
	want := BenchmarkStat{Name: "BenchmarkBuildProcesses", NsPerOp: 150, BytesPerOp: 15, AllocsPerOp: 2}
	if got != want {
		t.Errorf("averaged stat = %+v, want %+v", got, want)
	}
}

func TestTrimGOMAXPROCSSuffix(t *testing.T) {
	tests := []struct{ in, want string }{
		{"BenchmarkCollectFrame-4", "BenchmarkCollectFrame"},
		{"BenchmarkCollectFrame-16", "BenchmarkCollectFrame"},
		{"BenchmarkCollectFrame", "BenchmarkCollectFrame"},
	}
	for _, tc := range tests {
		if got := trimGOMAXPROCSSuffix(tc.in); got != tc.want {
			t.Errorf("trimGOMAXPROCSSuffix(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
