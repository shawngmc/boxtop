package main

import (
	"fmt"
	"runtime"
	"syscall"
)

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
// the page-count RSS from /proc/<pid>/stat field 24 into kB (see
// parseStatNameCPU).
// The base page size is architecture- and (on arm64/ppc64/mips) kernel-build-
// dependent, so we read it at runtime rather than assuming 4 KB: it's 16 KB on
// Apple Silicon (Asahi) and loongarch64, commonly 64 KB on enterprise POWER,
// 8 KB on sparc64/alpha, and configurable to 16/64 KB on arm64. syscall.
// Getpagesize() returns the base page size in bytes (cached from AT_PAGESZ,
// not a real syscall per call), which is the unit /proc/<pid>/stat field 24
// counts in.
var pageSizeKB = syscall.Getpagesize() / 1024

// formatBytesCompact renders a byte count as a short human string — "512M"
// below 1 GiB, "1.2G" at or above it — for the side-by-side top-bar meters,
// which don't have room for "1234/4096 MB".
func formatBytesCompact(b int64) string {
	const gib = 1024 * 1024 * 1024
	if b < 0 {
		b = 0
	}
	if b >= gib {
		return fmt.Sprintf("%.1fG", float64(b)/gib)
	}
	return fmt.Sprintf("%dM", b/(1024*1024))
}
