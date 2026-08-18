package main

import (
	"fmt"
	"runtime"
	"syscall"

	"golang.org/x/sys/unix"
)

// runtimeNumCPU mirrors Python's final os.sched_getaffinity(0) fallback:
// the schedulable core count for this thread, which can be a subset of
// the host total (e.g. under a container runtime that pins via affinity
// mask rather than a cgroup cpuset). Falls back to runtime.NumCPU() (host
// total) if the syscall fails, which shouldn't happen on Linux.
func runtimeNumCPU() int {
	var set unix.CPUSet
	if err := unix.SchedGetaffinity(0, &set); err == nil {
		if n := set.Count(); n > 0 {
			return n
		}
	}
	return runtime.NumCPU()
}

// clkTck mirrors CLK_TCK in the Python version (os.sysconf(SC_CLK_TCK)).
// 100 is the near-universal value on Linux. Exact parity would need cgo's
// C.sysconf(_SC_CLK_TCK) — golang.org/x/sys/unix has no pure-Go path to
// it on Linux (no SysconfClktck, no AT_CLKTCK auxv support) — and cgo
// would break the CGO_ENABLED=0 static builds scripts/create-test-container.sh
// relies on to run boxtop inside a musl/Alpine container, so this stays a
// constant.
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

// readSmallFile reads a small (single-page-ish) /proc or /sys file through
// readProcFile's raw open/read/close path and hands back its contents as a
// string. Every per-tick stat file outside the process loop (cgroup limits,
// /proc/meminfo, /proc/stat, /proc/cpuinfo, ...) used to go through
// os.ReadFile, which — per the readProcFile doc comment — pays an fstat,
// nonblocking fcntl, poller registration, and GC finalizer that these
// always-ready virtual files never need. Each call gets its own small
// buffer since these are one-shot reads (not a per-pid loop with a buffer to
// reuse), so the win is purely the skipped os.ReadFile overhead, not fewer
// allocations.
//
// The returned string aliases the buffer via bytesToStr with no copy; that's
// safe here because the buffer is freshly allocated per call and never
// reused, so nothing can mutate it out from under a caller that holds onto
// the string (e.g. readCgroupName's display label).
func readSmallFile(path string) (string, bool) {
	data, _, ok := readProcFile(path, make([]byte, 4096))
	if !ok {
		return "", false
	}
	return bytesToStr(data), true
}

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
