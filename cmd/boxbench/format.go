package main

import (
	"fmt"
	"io"
	"text/tabwriter"
)

func newTabwriter(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
}

// printResultSummary writes a human-readable rendering of one `boxbench
// run` result: the same content that goes into the JSON file when --out is
// given, formatted for a terminal instead.
func printResultSummary(w io.Writer, r Result) {
	fmt.Fprintf(w, "boxbench result: %s  (%s, %s, %s/%s)\n\n",
		r.Label, r.Timestamp.Format("2006-01-02 15:04:05"), r.GoVersion, r.GOOS, r.GOARCH)

	fmt.Fprintln(w, "Go benchmarks:")
	tw := newTabwriter(w)
	fmt.Fprintln(tw, "  NAME\tNS/OP\tB/OP\tALLOCS/OP")
	for _, b := range r.Benchmarks {
		fmt.Fprintf(tw, "  %s\t%.1f\t%.1f\t%.2f\n", b.Name, b.NsPerOp, b.BytesPerOp, b.AllocsPerOp)
	}
	tw.Flush()

	fmt.Fprintf(w, "\nReal process (%d runs, boxtop -n %g):\n", r.Process.Runs, r.Process.IntervalSeconds)
	tw = newTabwriter(w)
	fmt.Fprintln(tw, "  METRIC\tMEDIAN\tMEAN\tSTDDEV\tMIN\tMAX")
	printProcessRow(tw, "wall clock (ms)", r.Process.WallClockMs)
	printProcessRow(tw, "time to first output (ms)", r.Process.TimeToFirstOutputMs)
	printProcessRow(tw, "peak RSS (KB)", r.Process.PeakRSSKb)
	printProcessRow(tw, "cpu time (ms)", r.Process.CPUTimeMs)
	tw.Flush()
}

func printProcessRow(tw *tabwriter.Writer, label string, s MetricStats) {
	fmt.Fprintf(tw, "  %s\t%.2f\t%.2f\t%.2f\t%.2f\t%.2f\n", label, s.Median, s.Mean, s.StdDev, s.Min, s.Max)
}

// printComparison writes a human-readable diff of two `boxbench run`
// results, headlined by a delta and %change per metric so a regression or
// improvement is visible at a glance without manual arithmetic.
func printComparison(w io.Writer, a, b Result) {
	fmt.Fprintf(w, "Comparing A=%s (%s) vs B=%s (%s)\n\n",
		a.Label, a.Timestamp.Format("2006-01-02 15:04:05"),
		b.Label, b.Timestamp.Format("2006-01-02 15:04:05"))

	fmt.Fprintln(w, "Go benchmarks:")
	tw := newTabwriter(w)
	fmt.Fprintln(tw, "  NAME / METRIC\tA\tB\tDELTA\t%CHANGE")

	aByName := indexBenchmarks(a.Benchmarks)
	bByName := indexBenchmarks(b.Benchmarks)
	for _, name := range unionBenchmarkNames(a.Benchmarks, b.Benchmarks) {
		ab, aok := aByName[name]
		bb, bok := bByName[name]
		switch {
		case !aok:
			fmt.Fprintf(tw, "  %s\t\t\t\t(only in B)\n", name)
		case !bok:
			fmt.Fprintf(tw, "  %s\t\t\t\t(only in A)\n", name)
		default:
			fmt.Fprintf(tw, "  %s\t\t\t\t\n", name)
			printCompareRow(tw, "    ns/op", ab.NsPerOp, bb.NsPerOp)
			printCompareRow(tw, "    B/op", ab.BytesPerOp, bb.BytesPerOp)
			printCompareRow(tw, "    allocs/op", ab.AllocsPerOp, bb.AllocsPerOp)
		}
	}
	tw.Flush()

	fmt.Fprintln(w, "\nReal process (median of each side's runs):")
	tw = newTabwriter(w)
	fmt.Fprintln(tw, "  METRIC\tA\tB\tDELTA\t%CHANGE")
	printCompareRow(tw, "wall clock (ms)", a.Process.WallClockMs.Median, b.Process.WallClockMs.Median)
	printCompareRow(tw, "time to first output (ms)", a.Process.TimeToFirstOutputMs.Median, b.Process.TimeToFirstOutputMs.Median)
	printCompareRow(tw, "peak RSS (KB)", a.Process.PeakRSSKb.Median, b.Process.PeakRSSKb.Median)
	printCompareRow(tw, "cpu time (ms)", a.Process.CPUTimeMs.Median, b.Process.CPUTimeMs.Median)
	tw.Flush()
}

func printCompareRow(tw *tabwriter.Writer, label string, av, bv float64) {
	delta := bv - av
	pctText := "n/a"
	if av != 0 {
		pctText = fmt.Sprintf("%+.1f%%", delta/av*100)
	} else if delta == 0 {
		pctText = "0.0%"
	}
	fmt.Fprintf(tw, "  %s\t%.2f\t%.2f\t%+.2f\t%s\n", label, av, bv, delta, pctText)
}

func indexBenchmarks(stats []BenchmarkStat) map[string]BenchmarkStat {
	m := make(map[string]BenchmarkStat, len(stats))
	for _, s := range stats {
		m[s.Name] = s
	}
	return m
}

// unionBenchmarkNames orders every distinct benchmark name with a's own
// ordering first, then any names only b has appended after.
func unionBenchmarkNames(a, b []BenchmarkStat) []string {
	seen := make(map[string]bool)
	var names []string
	for _, s := range a {
		if !seen[s.Name] {
			seen[s.Name] = true
			names = append(names, s.Name)
		}
	}
	for _, s := range b {
		if !seen[s.Name] {
			seen[s.Name] = true
			names = append(names, s.Name)
		}
	}
	return names
}
