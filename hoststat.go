package main

import (
	"os"
	"strconv"
	"strings"
)

// hostCPUSample holds the two aggregate figures needed to compute
// system-wide CPU utilization between two /proc/stat samples: idle
// (idle + iowait, matching the standard "how busy is the box" definition)
// and total (the sum of every field on the "cpu " line).
type hostCPUSample struct {
	idle  uint64
	total uint64
}

// readHostCPUSample parses the aggregate "cpu  user nice system idle iowait
// irq softirq steal guest guest_nice" line at the top of /proc/stat — the
// system-wide counterpart to readCgroupCPUUsageUsec. Unlike that cumulative
// microsecond counter, these are jiffy counts since boot with no separate
// clock-rate conversion needed: the caller only ever uses the ratio of two
// deltas, so the units cancel out.
func readHostCPUSample() (hostCPUSample, bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return hostCPUSample{}, false
	}
	line, ok := firstLineWithPrefix(string(data), "cpu ")
	if !ok {
		return hostCPUSample{}, false
	}
	fields := strings.Fields(line)[1:] // drop the "cpu" label
	if len(fields) < 4 {
		return hostCPUSample{}, false
	}

	var sample hostCPUSample
	for i, f := range fields {
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			return hostCPUSample{}, false
		}
		sample.total += v
		if i == 3 || i == 4 { // idle, iowait
			sample.idle += v
		}
	}
	return sample, true
}
