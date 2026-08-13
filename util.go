package main

import "runtime"

// runtimeNumCPU wraps runtime.NumCPU() so cgroup.go's fallback reads the
// same way the Python version's final os.cpu_count() fallback does. Swap
// this for golang.org/x/sys/unix.SchedGetaffinity if you want exact
// parity with Python's os.sched_getaffinity(0) (schedulable cores, which
// can be a subset of the host total on some container setups).
func runtimeNumCPU() int {
	return runtime.NumCPU()
}

// clkTck mirrors CLK_TCK in the Python version (os.sysconf(SC_CLK_TCK)).
// 100 is the near-universal value on Linux; use
// golang.org/x/sys/unix.SysconfClktck() (via cgo) if you need to be
// rigorous about non-standard kernels.
const clkTck = 100.0

// pageSizeKB mirrors PAGE_KB. Not currently used since VmRSS in
// /proc/<pid>/status is already reported in kB directly, but kept here
// in case you add a code path that reads RSS in pages instead.
const pageSizeKB = 4
