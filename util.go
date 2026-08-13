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

// pageSizeKB mirrors PAGE_KB: the kernel page size in kB, used to convert
// the page-count RSS from /proc/<pid>/statm into kB (see readStatmRSS).
// 4 KB is the near-universal Linux page size; read it via
// golang.org/x/sys/unix or syscall.Getpagesize() if you need to support
// kernels/arches with a different page size.
const pageSizeKB = 4
