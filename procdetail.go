package main

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
	"strings"
)

// ProcessDetail is a point-in-time snapshot of extra per-process information
// beyond what's already in Process (the per-tick table row), fetched only
// when the details popup opens rather than on every poll — PPID, thread
// count, VmSize, and the exe path rarely change and aren't worth reading for
// every process on every tick just in case the user opens the popup.
type ProcessDetail struct {
	PID    int
	Name   string
	Cmd    string
	RSSKb  int64
	CPUPct *float64

	// HaveExtra is false if /proc/<pid>/status couldn't be read — the
	// process exited between being selected and the popup opening, or a
	// permission issue — in which case the fields below are zero-valued.
	HaveExtra bool
	State     string
	PPID      int
	Threads   int
	User      string
	VmSizeKb  int64
	VmSwapKb  int64
	Nice      int

	ExePath     string
	HaveExePath bool
}

// processStateNames maps /proc/<pid>/status's single-letter State code to a
// human-readable word, per proc(5).
var processStateNames = map[string]string{
	"R": "Running",
	"S": "Sleeping",
	"D": "Disk sleep",
	"Z": "Zombie",
	"T": "Stopped",
	"t": "Tracing stop",
	"X": "Dead",
	"I": "Idle",
}

// buildProcessDetail assembles a ProcessDetail for the process behind row p.
// It always returns a usable value — even if the process has since exited,
// the fields already known from the table row (name/cmd/RSS/CPU) still
// display, just without the /status-derived extras.
func buildProcessDetail(p Process) ProcessDetail {
	d := ProcessDetail{
		PID:    p.PID,
		Name:   p.Name,
		Cmd:    p.Cmd,
		RSSKb:  p.RSSKb,
		CPUPct: p.CPUPct,
	}

	if status, ok := parseProcStatus(p.PID); ok {
		d.HaveExtra = true
		if sf := strings.Fields(status["State"]); len(sf) > 0 {
			if name, known := processStateNames[sf[0]]; known {
				d.State = name
			} else {
				d.State = sf[0]
			}
		}
		d.PPID, _ = strconv.Atoi(status["PPid"])
		d.Threads, _ = strconv.Atoi(status["Threads"])
		d.VmSizeKb = parseVmKb(status["VmSize"])
		d.VmSwapKb = parseVmKb(status["VmSwap"])
		d.User = resolveUser(status["Uid"])
	}

	if nice, ok := readProcNice(p.PID); ok {
		d.Nice = nice
	}

	if exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", p.PID)); err == nil {
		d.ExePath = exe
		d.HaveExePath = true
	}

	return d
}

// statusWantedFields is the set of /proc/<pid>/status keys parseProcStatus
// keeps; every other line is skipped.
var statusWantedFields = map[string]bool{
	"State":   true,
	"PPid":    true,
	"Threads": true,
	"VmSize":  true,
	"VmSwap":  true,
	"Uid":     true,
}

// parseProcStatus reads /proc/<pid>/status and returns the wanted fields as
// a key -> trimmed-value map. Returns ok=false only if the file itself
// couldn't be read (process gone or no permission); a field simply absent
// from the file is just missing from the map.
func parseProcStatus(pid int) (map[string]string, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return nil, false
	}
	out := make(map[string]string, len(statusWantedFields))
	for _, line := range strings.Split(string(data), "\n") {
		i := strings.IndexByte(line, ':')
		if i < 0 {
			continue
		}
		key := line[:i]
		if !statusWantedFields[key] {
			continue
		}
		out[key] = strings.TrimSpace(line[i+1:])
	}
	return out, true
}

// parseVmKb parses a status value like "12345 kB" into its numeric kB
// amount, returning 0 for an empty or malformed value.
func parseVmKb(s string) int64 {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return 0
	}
	v, _ := strconv.ParseInt(fields[0], 10, 64)
	return v
}

// resolveUser turns a status "Uid" value (real, effective, saved, fs —
// space separated) into a display string, preferring the resolved username
// but falling back to the bare uid when the lookup fails (e.g. a uid with
// no /etc/passwd entry, common in minimal containers).
func resolveUser(uidField string) string {
	fields := strings.Fields(uidField)
	if len(fields) == 0 {
		return ""
	}
	uid := fields[0]
	if u, err := user.LookupId(uid); err == nil {
		return fmt.Sprintf("%s (uid %s)", u.Username, uid)
	}
	return "uid " + uid
}

// readProcNice reads the nice value (stat field 19) out of
// /proc/<pid>/stat, applying the same "split after the last ')'" trick as
// parseStatNameCPU since comm can contain spaces/parens.
func readProcNice(pid int) (int, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, false
	}
	return parseStatNice(string(data))
}

// parseStatNice is the pure parser behind readProcNice, split out for unit
// testing against fixture lines without touching /proc. Field 19 (nice) is
// index 16 once field 3 (state) is index 0 — same indexing scheme as
// parseStatNameCPU's utime/stime/rss.
func parseStatNice(raw string) (int, bool) {
	rp := strings.LastIndexByte(raw, ')')
	if rp == -1 || rp+1 >= len(raw) {
		return 0, false
	}
	fields := strings.Fields(raw[rp+1:])
	if len(fields) <= 16 {
		return 0, false
	}
	n, err := strconv.ParseInt(fields[16], 10, 64)
	if err != nil {
		return 0, false
	}
	return int(n), true
}
