package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"syscall"
	"time"
)

// runGoBenchmarks runs the root package's `go test -bench` suite (see
// bench_test.go) count times per benchmark and averages each name's
// repeats into one BenchmarkStat.
func runGoBenchmarks(dir string, count int) ([]BenchmarkStat, error) {
	cmd := exec.Command("go", "test", "-run=^$", "-bench=.", "-benchmem", "-count="+strconv.Itoa(count), ".")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("go test -bench failed: %w\n%s", err, out)
	}
	lines, err := parseBenchOutput(string(out))
	if err != nil {
		return nil, fmt.Errorf("parsing go test -bench output: %w", err)
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("no benchmark results found in go test -bench output:\n%s", out)
	}
	return aggregateBenchmarks(lines), nil
}

// buildBoxtop builds the boxtop binary at dir into outPath, matching the
// plain dev build CONTRIBUTING.md documents (kept symbols/debug info —
// this is a local measurement tool, not a release artifact).
func buildBoxtop(dir, outPath string) error {
	cmd := exec.Command("go", "build", "-o", outPath, ".")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go build failed: %w\n%s", err, out)
	}
	return nil
}

// processSample is one real boxtop process's measurements from a single
// `boxtop -n <interval>` run.
type processSample struct {
	wallClockMs         float64
	timeToFirstOutputMs float64
	peakRSSKb           float64
	cpuTimeMs           float64
}

// runProcessOnce runs the built binary once in non-interactive mode
// (auto-selected anyway since neither of the harness's pipes is a tty) and
// measures it black-box: wall clock, time to the first stdout byte, and
// peak RSS/CPU time via getrusage (the same data `/usr/bin/time -v`
// reports), so no /proc polling is needed.
func runProcessOnce(binPath string, intervalSeconds float64) (processSample, error) {
	cmd := exec.Command(binPath, "-n", strconv.FormatFloat(intervalSeconds, 'f', -1, 64))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return processSample{}, err
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return processSample{}, err
	}

	var firstByteAt time.Time
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buf := make([]byte, 4096)
		for {
			n, err := stdout.Read(buf)
			if n > 0 && firstByteAt.IsZero() {
				firstByteAt = time.Now()
			}
			if err != nil {
				return
			}
		}
	}()
	// exec.Cmd requires every read from a StdoutPipe to finish before
	// Wait is called, or Wait can close the pipe out from under the
	// reader — see the StdoutPipe doc comment.
	<-readDone

	waitErr := cmd.Wait()
	wallClock := time.Since(start)
	if waitErr != nil {
		return processSample{}, fmt.Errorf("boxtop run failed: %w\nstderr: %s", waitErr, stderrBuf.String())
	}

	ru, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage)
	if !ok {
		return processSample{}, fmt.Errorf("rusage not available on %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	cpuTimeMs := timevalMs(ru.Utime) + timevalMs(ru.Stime)

	var ttfoMs float64
	if !firstByteAt.IsZero() {
		ttfoMs = float64(firstByteAt.Sub(start)) / float64(time.Millisecond)
	}

	return processSample{
		wallClockMs:         float64(wallClock) / float64(time.Millisecond),
		timeToFirstOutputMs: ttfoMs,
		peakRSSKb:           float64(ru.Maxrss), // already kilobytes on Linux
		cpuTimeMs:           cpuTimeMs,
	}, nil
}

func timevalMs(tv syscall.Timeval) float64 {
	return float64(tv.Sec)*1000 + float64(tv.Usec)/1000
}

// runProcessSamples runs the built binary `runs` times and summarizes each
// metric across the repeats.
func runProcessSamples(binPath string, runs int, intervalSeconds float64) (ProcessStats, error) {
	wall := make([]float64, 0, runs)
	ttfo := make([]float64, 0, runs)
	rss := make([]float64, 0, runs)
	cpu := make([]float64, 0, runs)

	for i := 0; i < runs; i++ {
		sample, err := runProcessOnce(binPath, intervalSeconds)
		if err != nil {
			return ProcessStats{}, fmt.Errorf("process run %d/%d: %w", i+1, runs, err)
		}
		wall = append(wall, sample.wallClockMs)
		ttfo = append(ttfo, sample.timeToFirstOutputMs)
		rss = append(rss, sample.peakRSSKb)
		cpu = append(cpu, sample.cpuTimeMs)
	}

	return ProcessStats{
		Runs:                runs,
		IntervalSeconds:     intervalSeconds,
		WallClockMs:         computeStats(wall),
		TimeToFirstOutputMs: computeStats(ttfo),
		PeakRSSKb:           computeStats(rss),
		CPUTimeMs:           computeStats(cpu),
	}, nil
}

// gitLabel derives a default --label from the repo at dir: the short
// commit hash, with a "-dirty" suffix if the working tree has uncommitted
// changes. Falls back to "unlabeled" if git isn't available or dir isn't a
// repo, so boxbench still works outside a git checkout.
func gitLabel(dir string) string {
	hash, err := runGit(dir, "rev-parse", "--short", "HEAD")
	if err != nil {
		return "unlabeled"
	}
	status, err := runGit(dir, "status", "--porcelain")
	if err == nil && status != "" {
		hash += "-dirty"
	}
	return hash
}

func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return trimTrailingNewline(string(out)), nil
}

func trimTrailingNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// tempBoxtopBinary builds boxtop into a fresh temp directory and returns
// its path plus a cleanup func.
func tempBoxtopBinary(dir string) (path string, cleanup func(), err error) {
	tmpDir, err := os.MkdirTemp("", "boxbench-*")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { os.RemoveAll(tmpDir) }

	binPath := tmpDir + "/boxtop"
	if err := buildBoxtop(dir, binPath); err != nil {
		cleanup()
		return "", nil, err
	}
	return binPath, cleanup, nil
}
