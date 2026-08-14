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

// readHostMemTotal returns the machine's total physical RAM in bytes, read
// from MemTotal in /proc/meminfo (which is reported in kB). Used as the
// fallback denominator when the cgroup imposes no real memory limit.
func readHostMemTotal() (int64, bool) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	line, ok := firstLineWithPrefix(string(data), "MemTotal:")
	if !ok {
		return 0, false
	}
	fields := strings.Fields(line)
	if len(fields) >= 2 {
		if kb, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
			return kb * 1024, true
		}
	}
	return 0, false
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
// already detects via readHostMemTotal's noLimit fallback. Callers should
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
