package main

import (
	"strconv"
	"strings"
)

// parseCPUModel extracts the "model name" field of /proc/cpuinfo — the
// CPU's make/model string. Every core reports the same value on
// virtually every real system, so only the first match is needed.
func parseCPUModel(cpuinfo string) (string, bool) {
	line, ok := firstLineWithPrefix(cpuinfo, "model name")
	if !ok {
		return "", false
	}
	i := strings.IndexByte(line, ':')
	if i < 0 {
		return "", false
	}
	model := strings.TrimSpace(line[i+1:])
	if model == "" {
		return "", false
	}
	return model, true
}

// cachedCPUModel memoizes readCPUModel's result. The make/model string is
// fixed hardware identity for the process's lifetime — unlike cpu MHz or the
// cpufreq files, it never changes tick to tick — so re-reading and
// re-parsing all of /proc/cpuinfo (which grows with core count) every poll
// was pure waste. Only a successful read is cached; a transient failure (or
// a genuinely model-less /proc/cpuinfo) leaves it empty so the next tick
// tries again instead of freezing in the "unavailable" state forever.
var cachedCPUModel string

// readCPUModel reads and parses /proc/cpuinfo for the make/model string,
// caching the result after the first successful read.
func readCPUModel() (string, bool) {
	if cachedCPUModel != "" {
		return cachedCPUModel, true
	}
	data, ok := readSmallFile("/proc/cpuinfo")
	if !ok {
		return "", false
	}
	model, ok := parseCPUModel(data)
	if !ok {
		return "", false
	}
	cachedCPUModel = model
	return model, true
}

// parseCPUInfoMHz extracts the "cpu MHz" field of /proc/cpuinfo, used as a
// fallback current-clockspeed source on systems without a cpufreq sysfs
// interface (e.g. some VMs/containers).
func parseCPUInfoMHz(cpuinfo string) (float64, bool) {
	line, ok := firstLineWithPrefix(cpuinfo, "cpu MHz")
	if !ok {
		return 0, false
	}
	i := strings.IndexByte(line, ':')
	if i < 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(line[i+1:]), 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// readSysFreqKHz reads a cpufreq sysfs file (a single integer in kHz) such
// as scaling_cur_freq or cpuinfo_max_freq.
func readSysFreqKHz(path string) (float64, bool) {
	data, ok := readSmallFile(path)
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(data), 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// readCPUFreqMHz reports cpu0's current and maximum clock speed in MHz.
// The kernel's cpufreq subsystem (scaling_cur_freq/cpuinfo_max_freq, both
// in kHz) is the primary source, matching what lscpu/htop report; cpu0 is
// used as representative of the whole package rather than tracking every
// core individually. On kernels/VMs without cpufreq, current speed falls
// back to /proc/cpuinfo's "cpu MHz" field — there is no equivalent fallback
// for the max, so haveMax is false in that case.
func readCPUFreqMHz() (curMHz, maxMHz float64, haveCur, haveMax bool) {
	if v, ok := readSysFreqKHz("/sys/devices/system/cpu/cpu0/cpufreq/scaling_cur_freq"); ok {
		curMHz, haveCur = v/1000, true
	} else if v, ok := readCPUInfoMHz(); ok {
		curMHz, haveCur = v, true
	}

	if v, ok := readSysFreqKHz("/sys/devices/system/cpu/cpu0/cpufreq/cpuinfo_max_freq"); ok {
		maxMHz, haveMax = v/1000, true
	} else if v, ok := readSysFreqKHz("/sys/devices/system/cpu/cpu0/cpufreq/scaling_max_freq"); ok {
		maxMHz, haveMax = v/1000, true
	}
	return
}

// readCPUInfoMHz reads and parses /proc/cpuinfo for the "cpu MHz" fallback.
func readCPUInfoMHz() (float64, bool) {
	data, ok := readSmallFile("/proc/cpuinfo")
	if !ok {
		return 0, false
	}
	return parseCPUInfoMHz(data)
}
