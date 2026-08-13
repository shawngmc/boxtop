package main

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Process ports the dict shape returned by read_process()/get_processes().
// CPUPct is a pointer so nil can represent Python's None (no baseline
// sample yet), rather than overloading a sentinel float value.
type Process struct {
	PID    int
	RSSKb  int64
	Name   string
	Cmd    string
	CPUPct *float64
}

// sanitize ports sanitize(): replaces control characters (newlines, tabs)
// that could appear in argv/comm and would break the table's line-based
// layout. NUL bytes are handled separately in cmdline parsing.
func sanitize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 32 && r != 0 {
			b.WriteRune(' ')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// listPIDs ports list_pids(): every numeric entry under /proc.
func listPIDs() []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	pids := make([]int, 0, len(entries))
	for _, e := range entries {
		if pid, err := strconv.Atoi(e.Name()); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

// readProcess ports read_process(): returns (Process, ok) — ok is false
// if the process vanished between listing and reading (normal race).
func readProcess(pid int) (Process, bool) {
	base := "/proc/" + strconv.Itoa(pid)

	var rssKb int64
	if data, err := os.ReadFile(base + "/status"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "VmRSS:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					rssKb, _ = strconv.ParseInt(fields[1], 10, 64)
				}
				break
			}
		}
	} else if os.IsNotExist(err) {
		return Process{}, false
	}

	// Short kernel-reported executable name (max 15 chars, no path/args) —
	// same as Python's /proc/<pid>/comm read.
	name := "?"
	if data, err := os.ReadFile(base + "/comm"); err == nil {
		name = sanitize(strings.TrimSpace(string(data)))
	}

	// Full command line (path + args), NUL-separated.
	cmd := ""
	if data, err := os.ReadFile(base + "/cmdline"); err == nil && len(data) > 0 {
		replaced := strings.ReplaceAll(string(data), "\x00", " ")
		cmd = sanitize(strings.TrimSpace(replaced))
	}

	// Kernel threads / some zombies have empty cmdline — fall back to the
	// bracketed short name, same as the Python version.
	if cmd == "" {
		if name != "" && name != "?" {
			cmd = "[" + name + "]"
		} else {
			cmd = "[unknown]"
		}
	}

	return Process{PID: pid, RSSKb: rssKb, Name: name, Cmd: cmd}, true
}

// getProcesses ports get_processes(): all readable processes, unsorted.
func getProcesses() []Process {
	pids := listPIDs()
	procs := make([]Process, 0, len(pids))
	for _, pid := range pids {
		if p, ok := readProcess(pid); ok {
			procs = append(procs, p)
		}
	}
	return procs
}

// readProcessCPUSeconds ports read_process_cpu_seconds(): cumulative
// user+system CPU seconds for a process, from /proc/<pid>/stat. comm (the
// 2nd field) can contain spaces/parens, so — same trick as the Python
// version — we split on the LAST ')' to safely isolate the fields after
// it rather than naively splitting the whole line on whitespace.
func readProcessCPUSeconds(pid int) (float64, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	raw := string(data)
	idx := strings.LastIndex(raw, ")")
	if idx == -1 || idx+1 >= len(raw) {
		return 0, false
	}
	afterComm := strings.Fields(raw[idx+1:])
	// afterComm[0] is field 3 (state); utime is field 14, stime is 15 —
	// i.e. afterComm[11] and afterComm[12] once state (field 3) is index 0.
	if len(afterComm) < 13 {
		return 0, false
	}
	utime, e1 := strconv.ParseInt(afterComm[11], 10, 64)
	stime, e2 := strconv.ParseInt(afterComm[12], 10, 64)
	if e1 != nil || e2 != nil {
		return 0, false
	}
	return float64(utime+stime) / clkTck, true
}

// cpuSample is one entry of the per-pid CPU sampling baseline that
// procCPUPrev in monitorState carries across frames.
type cpuSample struct {
	cpuSecs float64
	t       time.Time
}

// sampleProcessCPU ports sample_process_cpu_pct(): mutates each Process's
// CPUPct in place, using per-pid state carried in prev (this replaces the
// module-level _proc_cpu_prev dict — see monitorState.procCPUPrev). It
// also prunes prev entries for pids that no longer exist, same as the
// Python version's cleanup loop, so long sessions don't leak memory.
func sampleProcessCPU(procs []Process, coresLimit float64, prev map[int]cpuSample) {
	now := time.Now()
	seen := make(map[int]bool, len(procs))

	for i := range procs {
		pid := procs[i].PID
		seen[pid] = true

		cpuSecs, ok := readProcessCPUSeconds(pid)
		if !ok {
			continue
		}

		if old, hadPrev := prev[pid]; hadPrev && coresLimit > 0 {
			dt := now.Sub(old.t).Seconds()
			if dt > 0 {
				pct := (cpuSecs - old.cpuSecs) / dt / coresLimit * 100
				if pct < 0 {
					pct = 0
				}
				procs[i].CPUPct = &pct
			}
		}
		prev[pid] = cpuSample{cpuSecs: cpuSecs, t: now}
	}

	for pid := range prev {
		if !seen[pid] {
			delete(prev, pid)
		}
	}
}
