package main

import (
	"os"
	"strconv"
	"strings"
)

// firstLineWithPrefix returns the first line of s beginning with prefix,
// scanning in place via IndexByte rather than materializing the whole
// []string that strings.Split allocates just to read a single line out of a
// multi-line /proc or /sys file.
func firstLineWithPrefix(s, prefix string) (string, bool) {
	for len(s) > 0 {
		line := s
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			line, s = s[:i], s[i+1:]
		} else {
			s = ""
		}
		if strings.HasPrefix(line, prefix) {
			return line, true
		}
	}
	return "", false
}

// cgroupFSMounted reports whether any cgroup v1 or v2 filesystem is
// mounted under /sys/fs/cgroup, by walking /proc/self/mountinfo. This is
// what lets collectFrame tell "no cgroup visible at all" (cgroupfs
// unmounted or not delegated here — common in restricted or nested
// containers) apart from "cgroup visible but genuinely unconstrained" (an
// ordinary host, or any container that never passed --memory/--cpus):
// both leave memory.max unreadable, but only the first one means there's
// truly nothing to measure.
//
// A single statfs on /sys/fs/cgroup itself isn't enough: on a cgroup v1
// host that path is often just a plain tmpfs used to hang the
// per-controller directories off of (as on this repo's own dev/CI boxes),
// with the actual "cgroup"-typed mounts nested one level down at paths
// like /sys/fs/cgroup/memory — so the check has to walk the mount table,
// not just stat the root.
func cgroupFSMounted() bool {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		// Can't tell either way; assume mounted so an unreadable mount
		// table never changes behavior on its own.
		return true
	}
	return hasCgroupMount(string(data))
}

// hasCgroupMount is cgroupFSMounted's pure parsing half. mountinfo lines
// look like:
//
//	29 22 0:25 / /sys/fs/cgroup ro,nosuid,nodev,noexec shared:4 - tmpfs tmpfs ro,mode=755
//	30 29 0:26 / /sys/fs/cgroup/systemd rw,nosuid ... shared:5 - cgroup cgroup rw,...
//
// everything left of the literal " - " separator is positional fields
// (mount point is the 5th); everything right of it starts with the
// filesystem type.
func hasCgroupMount(mountinfo string) bool {
	for _, line := range strings.Split(mountinfo, "\n") {
		sep := strings.Index(line, " - ")
		if sep < 0 {
			continue
		}
		pre := strings.Fields(line[:sep])
		post := strings.Fields(line[sep+3:])
		if len(pre) < 5 || len(post) < 1 {
			continue
		}
		mountPoint, fsType := pre[4], post[0]
		if (fsType == "cgroup" || fsType == "cgroup2") &&
			(mountPoint == "/sys/fs/cgroup" || strings.HasPrefix(mountPoint, "/sys/fs/cgroup/")) {
			return true
		}
	}
	return false
}

// readCgroupVal ports read_cgroup_val(): tries the v2 path first, then
// falls back to the v1 memory-controller path. Returns (value, true) on
// success, (0, false) if the file doesn't exist or isn't a plain integer.
func readCgroupVal(filename string) (int64, bool) {
	paths := []string{
		"/sys/fs/cgroup/" + filename,
		"/sys/fs/cgroup/memory/" + filename,
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		s := strings.TrimSpace(string(data))
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			continue
		}
		return n, true
	}
	return 0, false
}

// hostMeminfo holds the /proc/meminfo fields needed for the system-wide RAM
// and Swap meters, plus the RAM noLimit fallback denominator.
type hostMeminfo struct {
	totalKB       int64
	availableKB   int64
	swapTotalKB   int64
	swapFreeKB    int64
	haveAvailable bool // false on kernels old enough to lack MemAvailable
}

// meminfoInt parses the kB value out of a /proc/meminfo line like
// "MemTotal:       16384000 kB".
func meminfoInt(data, prefix string) (int64, bool) {
	line, ok := firstLineWithPrefix(data, prefix)
	if !ok {
		return 0, false
	}
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0, false
	}
	v, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// readHostMeminfo parses /proc/meminfo once for every field the top bar's
// system-wide meters need: total/available RAM (available falls back to
// MemFree on pre-3.14 kernels that lack MemAvailable) and total/free swap.
// Returns (zero value, false) only if the file itself can't be read; a
// single missing field just leaves its zero value in place (MemTotal is
// required for a useful result, so its absence is treated as failure too).
func readHostMeminfo() (hostMeminfo, bool) {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return hostMeminfo{}, false
	}
	data := string(raw)

	totalKB, ok := meminfoInt(data, "MemTotal:")
	if !ok {
		return hostMeminfo{}, false
	}

	var m hostMeminfo
	m.totalKB = totalKB
	if avail, ok := meminfoInt(data, "MemAvailable:"); ok {
		m.availableKB = avail
		m.haveAvailable = true
	} else if free, ok := meminfoInt(data, "MemFree:"); ok {
		m.availableKB = free
	}
	m.swapTotalKB, _ = meminfoInt(data, "SwapTotal:")
	m.swapFreeKB, _ = meminfoInt(data, "SwapFree:")
	return m, true
}

// readCgroupSwapCurrent returns the cgroup's current swap usage in bytes.
// cgroup v2 exposes this directly; v1 has no standalone swap counter, only
// memory+swap combined (memory.memsw.*), so it's derived by subtracting the
// plain memory usage from the combined figure.
func readCgroupSwapCurrent() (int64, bool) {
	if v, ok := readCgroupVal("memory.swap.current"); ok {
		return v, true
	}
	memsw, ok1 := readCgroupVal("memory.memsw.usage_in_bytes")
	mem, ok2 := readCgroupVal("memory.usage_in_bytes")
	if ok1 && ok2 {
		v := memsw - mem
		if v < 0 {
			v = 0
		}
		return v, true
	}
	return 0, false // no swap controller available (e.g. v1 w/o swapaccount=1)
}

// readCgroupSwapMax returns the cgroup's swap limit in bytes. Returns
// (0, false) both when there's genuinely no swap controller and when the
// limit is unbounded ("max" on v2, or no v1 memsw limit set) — callers fall
// back to the host's total swap as the denominator in that case, mirroring
// readCgroupVal's memory.max "max"-sentinel handling in collectFrame.
func readCgroupSwapMax() (int64, bool) {
	if v, ok := readCgroupVal("memory.swap.max"); ok {
		return v, true
	}
	memswMax, ok1 := readCgroupVal("memory.memsw.limit_in_bytes")
	memMax, ok2 := readCgroupVal("memory.limit_in_bytes")
	if ok1 && ok2 && memswMax > memMax {
		return memswMax - memMax, true
	}
	return 0, false // covers "max"/unbounded (v2) and no-limit (v1)
}

// cpuLimitSource mirrors the Python source strings ("quota"/"cpuset"/"host")
// used to label where the CPU core count came from.
type cpuLimitSource string

const (
	cpuSourceQuota  cpuLimitSource = "quota"
	cpuSourceCPUSet cpuLimitSource = "cpuset"
	cpuSourceHost   cpuLimitSource = "host"
)

// countCPUList ports _count_cpu_list(): counts cores in a cpuset-style
// list like "0-3,7" -> 5.
func countCPUList(s string) int {
	total := 0
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			bounds := strings.SplitN(part, "-", 2)
			a, errA := strconv.Atoi(bounds[0])
			b, errB := strconv.Atoi(bounds[1])
			if errA == nil && errB == nil {
				total += b - a + 1
			}
			continue
		}
		total++
	}
	return total
}

// readCgroupCPULimit ports read_cgroup_cpu_limit(): enforced quota first
// (v2 single-file, then v1 split files), falling back to cpuset core
// count, then to the process's schedulable affinity, then to the host's
// total CPU count. Returns (0, "", false) if nothing could be determined.
func readCgroupCPULimit() (float64, cpuLimitSource, bool) {
	// cgroup v2: single file, "$QUOTA $PERIOD" or "max $PERIOD"
	if data, err := os.ReadFile("/sys/fs/cgroup/cpu.max"); err == nil {
		parts := strings.Fields(strings.TrimSpace(string(data)))
		if len(parts) == 2 && parts[0] != "max" {
			quota, errQ := strconv.ParseInt(parts[0], 10, 64)
			period, errP := strconv.ParseInt(parts[1], 10, 64)
			if errQ == nil && errP == nil && period > 0 {
				return float64(quota) / float64(period), cpuSourceQuota, true
			}
		}
	} else {
		// cgroup v1: two separate files
		quotaData, errQ := os.ReadFile("/sys/fs/cgroup/cpu/cpu.cfs_quota_us")
		periodData, errP := os.ReadFile("/sys/fs/cgroup/cpu/cpu.cfs_period_us")
		if errQ == nil && errP == nil {
			quota, e1 := strconv.ParseInt(strings.TrimSpace(string(quotaData)), 10, 64)
			period, e2 := strconv.ParseInt(strings.TrimSpace(string(periodData)), 10, 64)
			if e1 == nil && e2 == nil && quota > 0 && period > 0 {
				return float64(quota) / float64(period), cpuSourceQuota, true
			}
		}
	}

	// No enforced quota — fall back to the cpuset this cgroup is pinned to.
	cpusetPaths := []string{
		"/sys/fs/cgroup/cpuset.cpus.effective",
		"/sys/fs/cgroup/cpuset.cpus",
		"/sys/fs/cgroup/cpuset/cpuset.cpus",
	}
	for _, p := range cpusetPaths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		s := strings.TrimSpace(string(data))
		if s == "" {
			continue
		}
		if n := countCPUList(s); n > 0 {
			return float64(n), cpuSourceCPUSet, true
		}
	}

	// Last resort: host's total core count. (Go's stdlib doesn't expose
	// sched_getaffinity directly the way Python's os.sched_getaffinity
	// does; golang.org/x/sys/unix.SchedGetaffinity could be used here for
	// exact parity if that distinction matters for your containers.)
	if n := runtimeNumCPU(); n > 0 {
		return float64(n), cpuSourceHost, true
	}
	return 0, "", false
}

// readCgroupName returns a short identifier for the current process's own
// cgroup — e.g. "docker/1a2b3c4d5e6f" or "system.slice/foo.service" — for
// display next to the top bar's "cgroup" header, parsed from
// /proc/self/cgroup. Returns ("", false) when that file can't be read, or
// when every controller line resolves to the host root ("/"): the latter
// is also true inside a container using a private cgroup namespace (the
// default for modern Docker/Podman), where the container's own cgroup
// appears as its own root and so has nothing more specific to name — the
// RAM/CPU meters above still report that container's real limits
// correctly, only this cosmetic label has nothing to show.
func readCgroupName() (string, bool) {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", false
	}
	return parseCgroupName(string(data))
}

// parseCgroupName is readCgroupName's pure parsing half, split out for
// testability the same way parseStatNameCPU/parseCPUModel are — it takes
// /proc/self/cgroup's raw content ("<hierarchy-id>:<controllers>:<path>"
// lines) and picks the most useful non-root path: cgroup v2's single
// "0::<path>" line is preferred when present; otherwise the v1
// memory-controller line, and failing that, any other controller line with
// a non-root path — better than nothing when memory happens to be
// co-mounted oddly.
func parseCgroupName(data string) (string, bool) {
	var v2Path, memPath, anyPath string
	for _, line := range strings.Split(strings.TrimSpace(data), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		hier, controllers, path := parts[0], parts[1], parts[2]
		if path == "" || path == "/" {
			continue
		}
		switch {
		case hier == "0" && controllers == "":
			v2Path = path
		case strings.Contains(controllers, "memory"):
			memPath = path
		case anyPath == "":
			anyPath = path
		}
	}

	path := v2Path
	if path == "" {
		path = memPath
	}
	if path == "" {
		path = anyPath
	}
	if path == "" {
		return "", false
	}
	return strings.TrimPrefix(path, "/"), true
}

// readCgroupOOMKills returns the cumulative number of times the kernel has
// OOM-killed a process in this cgroup: the oom_kill counter in
// memory.events (cgroup v2), or the oom_kill field of memory.oom_control
// (cgroup v1, on kernels new enough to report it there). This only ever
// climbs, so a nonzero value means the kernel has already started reaping
// processes — worth surfacing even while the RAM bar still reads under
// 100%, since a burst of allocation can trigger a kill and free the memory
// before the next poll samples memory.current.
//
// Returns (0, false) if neither file exposes the counter, which is the
// expected case when boxtop isn't confined by a real memory-limited
// cgroup (bare host, most WSL setups) — the same condition collectFrame
// already detects via readHostMeminfo's noLimit fallback. Callers should
// omit the OOM display entirely in that case rather than showing a
// misleading "0", since 0 there doesn't mean "safe", it means "not
// measured."
func readCgroupOOMKills() (int64, bool) {
	paths := []string{
		"/sys/fs/cgroup/memory.events",
		"/sys/fs/cgroup/memory/memory.oom_control",
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if line, ok := firstLineWithPrefix(string(data), "oom_kill "); ok {
			fields := strings.Fields(line)
			if len(fields) == 2 {
				if v, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
					return v, true
				}
			}
		}
	}
	return 0, false
}

// readCgroupCPUUsageUsec ports read_cgroup_cpu_usage_usec(): cumulative
// CPU time consumed by the whole cgroup, in microseconds, since creation.
func readCgroupCPUUsageUsec() (int64, bool) {
	if data, err := os.ReadFile("/sys/fs/cgroup/cpu.stat"); err == nil {
		if line, ok := firstLineWithPrefix(string(data), "usage_usec"); ok {
			fields := strings.Fields(line)
			if len(fields) == 2 {
				if v, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
					return v, true
				}
			}
		}
	}

	v1Paths := []string{
		"/sys/fs/cgroup/cpuacct/cpuacct.usage",
		"/sys/fs/cgroup/cpu,cpuacct/cpuacct.usage",
	}
	for _, p := range v1Paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		s := strings.TrimSpace(string(data))
		if ns, err := strconv.ParseInt(s, 10, 64); err == nil {
			return ns / 1000, true // v1 reports nanoseconds
		}
	}
	return 0, false
}
