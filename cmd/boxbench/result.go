// Command boxbench is a black-box benchmarking harness for boxtop: it
// builds and runs the real compiled binary (plus the in-process
// benchmarks in the root package's bench_test.go) and reports human
// -readable memory/CPU/timing numbers. It never imports boxtop's
// internals — those are unexported in package main at the repo root
// anyway — so it only ever shells out via os/exec, keeping it fully
// decoupled from boxtop's internal structure.
//
// Comparing two versions is "save-then-compare": `boxbench run --out`
// writes a labeled JSON snapshot for whatever's currently built; run it
// once per branch/commit you want to compare, then hand both files to
// `boxbench compare`. boxbench itself never touches git state.
package main

import (
	"encoding/json"
	"os"
	"time"
)

// BenchmarkStat is one aggregated `go test -bench` result: the mean of
// NsPerOp/BytesPerOp/AllocsPerOp across every -count repeat of that
// benchmark name.
type BenchmarkStat struct {
	Name        string  `json:"name"`
	NsPerOp     float64 `json:"ns_per_op"`
	BytesPerOp  float64 `json:"bytes_per_op"`
	AllocsPerOp float64 `json:"allocs_per_op"`
}

// MetricStats summarizes repeated samples of one real-process metric.
// Median is the headline number in the comparison table — it shrinks the
// effect of the odd scheduler-noise outlier that mean wouldn't.
type MetricStats struct {
	Mean   float64 `json:"mean"`
	Median float64 `json:"median"`
	StdDev float64 `json:"stddev"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
}

// ProcessStats is the black-box measurement of the real boxtop binary run
// `Runs` times in non-interactive mode (`boxtop -n <interval>`).
type ProcessStats struct {
	Runs                int         `json:"runs"`
	IntervalSeconds     float64     `json:"interval_seconds"`
	WallClockMs         MetricStats `json:"wall_clock_ms"`
	TimeToFirstOutputMs MetricStats `json:"time_to_first_output_ms"`
	PeakRSSKb           MetricStats `json:"peak_rss_kb"`
	CPUTimeMs           MetricStats `json:"cpu_time_ms"`
}

// Result is one full `boxbench run` snapshot: everything needed to print
// its own summary and to diff against another Result later.
type Result struct {
	Label      string          `json:"label"`
	Timestamp  time.Time       `json:"timestamp"`
	GoVersion  string          `json:"go_version"`
	GOOS       string          `json:"goos"`
	GOARCH     string          `json:"goarch"`
	Benchmarks []BenchmarkStat `json:"benchmarks"`
	Process    ProcessStats    `json:"process"`
}

func saveResult(path string, r Result) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func loadResult(path string) (Result, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{}, err
	}
	var r Result
	if err := json.Unmarshal(data, &r); err != nil {
		return Result{}, err
	}
	return r, nil
}
