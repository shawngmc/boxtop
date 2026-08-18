package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "run":
		err = runCmd(os.Args[2:])
	case "compare":
		err = compareCmd(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "boxbench: unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `boxbench is a black-box benchmarking harness for boxtop.

Usage:
  boxbench run [flags]              measure the current working tree
  boxbench compare FILE1 FILE2      diff two 'run' results

Comparing branches/versions is save-then-compare: run 'boxbench run --out'
once per branch/commit you want to compare (boxbench itself never touches
git state), then hand both result files to 'boxbench compare'.

Run 'boxbench run -h' for flag details.
`)
}

func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	label := fs.String("label", "", "label for this result (default: git short hash, plus \"-dirty\" if the tree has uncommitted changes)")
	out := fs.String("out", "", "write the full result as JSON to this path (optional; a summary always prints to stdout)")
	runs := fs.Int("runs", 7, "number of real boxtop process runs to sample")
	benchCount := fs.Int("bench-count", 3, "go test -bench -count value")
	interval := fs.Float64("interval", 0.05, "refresh interval in seconds passed to boxtop -n for each process run")
	fs.Parse(args)

	const dir = "."

	fmt.Fprintln(os.Stderr, "boxbench: running go test -bench=. ...")
	benchStats, err := runGoBenchmarks(dir, *benchCount)
	if err != nil {
		return err
	}

	fmt.Fprintln(os.Stderr, "boxbench: building boxtop...")
	binPath, cleanup, err := tempBoxtopBinary(dir)
	if err != nil {
		return err
	}
	defer cleanup()

	fmt.Fprintf(os.Stderr, "boxbench: running boxtop %d times...\n", *runs)
	procStats, err := runProcessSamples(binPath, *runs, *interval)
	if err != nil {
		return err
	}

	lbl := *label
	if lbl == "" {
		lbl = gitLabel(dir)
	}

	result := Result{
		Label:      lbl,
		Timestamp:  time.Now(),
		GoVersion:  runtime.Version(),
		GOOS:       runtime.GOOS,
		GOARCH:     runtime.GOARCH,
		Benchmarks: benchStats,
		Process:    procStats,
	}

	fmt.Fprintln(os.Stderr)
	printResultSummary(os.Stdout, result)

	if *out != "" {
		if err := saveResult(*out, result); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "\nboxbench: wrote %s\n", *out)
	}
	return nil
}

func compareCmd(args []string) error {
	fs := flag.NewFlagSet("compare", flag.ExitOnError)
	fs.Parse(args)
	if fs.NArg() != 2 {
		return fmt.Errorf("usage: boxbench compare FILE1.json FILE2.json")
	}

	a, err := loadResult(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("loading %s: %w", fs.Arg(0), err)
	}
	b, err := loadResult(fs.Arg(1))
	if err != nil {
		return fmt.Errorf("loading %s: %w", fs.Arg(1), err)
	}

	printComparison(os.Stdout, a, b)
	return nil
}
