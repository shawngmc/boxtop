package main

import (
	"bufio"
	"regexp"
	"strconv"
	"strings"
)

// benchLine is one parsed result line from `go test -bench=. -benchmem`
// output, e.g.:
//
//	BenchmarkCollectFrame-4    2010    594450 ns/op    151910 B/op    759 allocs/op
//
// name has the trailing "-<GOMAXPROCS>" suffix stripped, so results stay
// comparable across machines with different core counts.
type benchLine struct {
	name        string
	nsPerOp     float64
	bytesPerOp  float64
	allocsPerOp float64
}

// benchLineRE matches a `go test -bench -benchmem` result line. The B/op
// and allocs/op groups are optional so a benchmark run without -benchmem
// still parses (bytesPerOp/allocsPerOp just come back 0). The captured
// name still carries its trailing "-<GOMAXPROCS>" suffix (e.g.
// "BenchmarkCollectFrame-4"); trimGOMAXPROCSSuffix strips that separately
// below rather than fighting an optional group inside the name match.
var benchLineRE = regexp.MustCompile(
	`^(Benchmark\S+)\s+(\d+)\s+([0-9.]+)\s+ns/op(?:\s+([0-9.]+)\s+B/op)?(?:\s+([0-9.]+)\s+allocs/op)?`,
)

// gomaxprocsSuffixRE matches the "-<N>" testing appends to every benchmark
// name for the GOMAXPROCS it ran under.
var gomaxprocsSuffixRE = regexp.MustCompile(`-\d+$`)

func trimGOMAXPROCSSuffix(name string) string {
	return gomaxprocsSuffixRE.ReplaceAllString(name, "")
}

// parseBenchOutput extracts every benchmark result line from raw `go test
// -bench` stdout, ignoring the "goos:"/"pkg:"/"PASS"/etc. framing lines
// around them.
func parseBenchOutput(output string) ([]benchLine, error) {
	var lines []benchLine
	scanner := bufio.NewScanner(strings.NewReader(output))
	// Individual lines are short, but be generous in case a future
	// benchmark reports extra custom metrics via b.ReportMetric.
	scanner.Buffer(make([]byte, 0, 4096), 1<<20)
	for scanner.Scan() {
		m := benchLineRE.FindStringSubmatch(scanner.Text())
		if m == nil {
			continue
		}
		ns, err := strconv.ParseFloat(m[3], 64)
		if err != nil {
			continue
		}
		var bytesPerOp, allocsPerOp float64
		if m[4] != "" {
			bytesPerOp, _ = strconv.ParseFloat(m[4], 64)
		}
		if m[5] != "" {
			allocsPerOp, _ = strconv.ParseFloat(m[5], 64)
		}
		lines = append(lines, benchLine{
			name:        trimGOMAXPROCSSuffix(m[1]),
			nsPerOp:     ns,
			bytesPerOp:  bytesPerOp,
			allocsPerOp: allocsPerOp,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

// aggregateBenchmarks averages every repeat of the same benchmark name
// (one repeat per `go test -count`) into a single BenchmarkStat, in the
// order each name was first seen.
func aggregateBenchmarks(lines []benchLine) []BenchmarkStat {
	var order []string
	sums := make(map[string]*BenchmarkStat)
	counts := make(map[string]int)
	for _, l := range lines {
		s, ok := sums[l.name]
		if !ok {
			s = &BenchmarkStat{Name: l.name}
			sums[l.name] = s
			order = append(order, l.name)
		}
		s.NsPerOp += l.nsPerOp
		s.BytesPerOp += l.bytesPerOp
		s.AllocsPerOp += l.allocsPerOp
		counts[l.name]++
	}

	stats := make([]BenchmarkStat, 0, len(order))
	for _, name := range order {
		s := *sums[name]
		n := float64(counts[name])
		s.NsPerOp /= n
		s.BytesPerOp /= n
		s.AllocsPerOp /= n
		stats = append(stats, s)
	}
	return stats
}
