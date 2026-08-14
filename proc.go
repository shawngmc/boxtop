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
	PID   int
	RSSKb int64
	Name  string
	// NameLower is Name pre-lowercased once at build time so the name-sort
	// comparator doesn't call strings.ToLower (an allocation) O(n log n)
	// times per sort — and per redraw, since sorting runs every frame.
	NameLower string
	Cmd       string
	CPUPct    *float64
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

// readStatNameCPU reads /proc/<pid>/stat ONCE and returns both the short
// kernel name (comm) and cumulative user+system CPU seconds. comm (the 2nd
// field) can contain spaces and parentheses, so — same trick as the Python
// version — we isolate the numeric tail by splitting after the LAST ')'.
//
// This single read now supplies the name that used to come from a separate
// /proc/<pid>/comm read, so the per-pid read count drops.
func readStatNameCPU(pid int) (name string, cpuSecs float64, ok bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", 0, false
	}
	return parseStatNameCPU(string(data))
}

// parseStatNameCPU is the pure parser behind readStatNameCPU, split out so
// the tricky comm-with-parens/spaces handling can be unit-tested against
// fixture lines without touching /proc.
func parseStatNameCPU(raw string) (name string, cpuSecs float64, ok bool) {
	lp := strings.IndexByte(raw, '(')
	rp := strings.LastIndexByte(raw, ')')
	if lp == -1 || rp == -1 || rp < lp || rp+1 >= len(raw) {
		return "", 0, false
	}
	// comm is between the first '(' and last ')'; the kernel caps it at 15
	// chars (no path/args), identical to what /proc/<pid>/comm reported.
	name = sanitize(raw[lp+1 : rp])
	if name == "" {
		name = "?"
	}

	fields := strings.Fields(raw[rp+1:])
	// fields[0] is field 3 (state); utime is field 14, stime is 15 —
	// i.e. fields[11] and fields[12] once state (field 3) is index 0.
	if len(fields) < 13 {
		return name, 0, false
	}
	utime, e1 := strconv.ParseInt(fields[11], 10, 64)
	stime, e2 := strconv.ParseInt(fields[12], 10, 64)
	if e1 != nil || e2 != nil {
		return name, 0, false
	}
	return name, float64(utime+stime) / clkTck, true
}

// readStatmRSS reads /proc/<pid>/statm and returns the resident set size in
// kB. statm is a single short space-separated line whose 2nd field is the
// RSS in pages — far cheaper to read and parse each tick than scanning the
// ~50-line /proc/<pid>/status for its VmRSS line.
//
// Fidelity note: this is the page-count RSS (pages * PAGE_KB), which can
// differ slightly from status's VmRSS anon/file/shmem breakdown. For a
// per-process memory ranking the difference is immaterial; switch back to
// parsing VmRSS out of /status if exact parity matters.
func readStatmRSS(pid int) (int64, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/statm")
	if err != nil {
		return 0, false
	}
	return parseStatmRSS(string(data))
}

// parseStatmRSS is the pure parser behind readStatmRSS.
func parseStatmRSS(raw string) (int64, bool) {
	fields := strings.Fields(raw)
	if len(fields) < 2 {
		return 0, false
	}
	pages, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return pages * pageSizeKB, true
}

// cmdFor returns the full COMMAND string for pid, reading and caching
// /proc/<pid>/cmdline on first sight and reusing the cached value on every
// later tick. A process's cmdline is effectively immutable for its lifetime,
// so this turns a per-tick read into a once-per-process read (dead pids are
// evicted from the cache in buildProcesses).
//
// Empty cmdline (kernel threads, some zombies) falls back to the bracketed
// short name, same as the Python version. A transient read *error* is not
// cached, so a busy process that briefly refuses the read self-corrects next
// tick instead of freezing on a fallback string.
func cmdFor(pid int, name string, cache map[int]string) string {
	if c, ok := cache[pid]; ok {
		return c
	}

	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return bracketName(name) // don't cache — retry next tick
	}

	cmd := parseCmdline(data, name)
	cache[pid] = cmd
	return cmd
}

// parseCmdline turns the NUL-separated raw /cmdline bytes into a display
// string, falling back to the bracketed short name when it is empty (kernel
// threads, some zombies). Split out from cmdFor so it can be unit-tested.
func parseCmdline(data []byte, name string) string {
	cmd := ""
	if len(data) > 0 {
		replaced := strings.ReplaceAll(string(data), "\x00", " ")
		cmd = sanitize(strings.TrimSpace(replaced))
	}
	if cmd == "" {
		cmd = bracketName(name)
	}
	return cmd
}

// bracketName is the empty-cmdline fallback: the short name in brackets, or
// [unknown] when even the name is missing.
func bracketName(name string) string {
	if name != "" && name != "?" {
		return "[" + name + "]"
	}
	return "[unknown]"
}

// cpuSample is one entry of the per-pid CPU sampling baseline that
// procCPUPrev in monitorState carries across frames.
type cpuSample struct {
	cpuSecs float64
	t       time.Time
}

// buildProcesses reads every process once per tick and returns the full,
// unsorted table with CPU% already sampled. It replaces the old
// getProcesses()+sampleProcessCPU() two-pass design, which read four files
// per pid (status, comm, cmdline, stat); this reads /stat (name + CPU) and
// /statm (RSS) per tick, plus /cmdline only for pids not already cached —
// roughly a 4x reduction in per-process I/O for a steady process set.
//
// The per-pid CPU baseline (procCPUPrev), COMMAND cache (cmdCache), and the
// scratch "seen this tick" set (procSeen) all live on state and are reused
// across frames: procCPUPrev and cmdCache carry data forward (pruned of pids
// that no longer exist so long sessions don't leak), while procSeen is just
// cleared and refilled each tick instead of allocating a fresh map.
func buildProcesses(state *monitorState, coresLimit float64) []Process {
	now := time.Now()
	prev := state.procCPUPrev
	cmdCache := state.cmdCache
	seen := state.procSeen
	clear(seen)

	pids := listPIDs()
	procs := make([]Process, 0, len(pids))

	for _, pid := range pids {
		name, cpuSecs, statOK := readStatNameCPU(pid)
		if !statOK {
			// Process vanished between listing and reading, or its stat was
			// unparseable — skip it (normal race).
			continue
		}
		rssKb, rssOK := readStatmRSS(pid)
		if !rssOK {
			// Raced away between the two reads.
			continue
		}
		seen[pid] = true

		p := Process{
			PID:       pid,
			RSSKb:     rssKb,
			Name:      name,
			NameLower: strings.ToLower(name),
			Cmd:       cmdFor(pid, name, cmdCache),
		}

		// CPU% from the delta against the previous sample for this pid.
		if old, hadPrev := prev[pid]; hadPrev && coresLimit > 0 {
			dt := now.Sub(old.t).Seconds()
			if dt > 0 {
				pct := (cpuSecs - old.cpuSecs) / dt / coresLimit * 100
				if pct < 0 {
					pct = 0
				}
				p.CPUPct = &pct
			}
		}
		prev[pid] = cpuSample{cpuSecs: cpuSecs, t: now}

		procs = append(procs, p)
	}

	// Evict dead pids from the carried-over caches.
	for pid := range prev {
		if !seen[pid] {
			delete(prev, pid)
		}
	}
	for pid := range cmdCache {
		if !seen[pid] {
			delete(cmdCache, pid)
		}
	}

	return procs
}
