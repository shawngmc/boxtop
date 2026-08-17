package main

import (
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
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
//
// Called once per process per tick (name) and once per new pid (cmdline),
// but real process names/cmdlines almost never contain control characters,
// so it first scans read-only for one; only when it actually finds a
// replacement to make does it pay for the Builder allocation and copy. This
// keeps the overwhelmingly common case allocation-free instead of always
// building a fresh string.
func sanitize(s string) string {
	needsFix := false
	for _, r := range s {
		if r < 32 && r != 0 {
			needsFix = true
			break
		}
	}
	if !needsFix {
		return s
	}

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

// direntReclenOffset/direntNameOffset are the byte offsets of the Reclen and
// Name fields within the kernel's getdents64 dirent record, per
// syscall.Dirent's layout — used by listPIDs to parse raw directory entries
// without going through os.ReadDir. Computed once rather than re-measured
// per entry.
var (
	direntReclenOffset = int(unsafe.Offsetof(syscall.Dirent{}.Reclen))
	direntNameOffset   = int(unsafe.Offsetof(syscall.Dirent{}.Name))
)

// direntReclen reads a raw getdents64 record's Reclen field (how many bytes
// of buf this one record occupies, header included) via plain byte shifts
// rather than an unsafe.Pointer cast, so it works regardless of the buffer's
// alignment — same approach the standard library's own (unexported)
// dirent-parsing helpers use internally. Little-endian only, matching every
// realistic boxtop target (amd64/arm64).
func direntReclen(buf []byte) (int, bool) {
	if len(buf) < direntReclenOffset+2 {
		return 0, false
	}
	return int(buf[direntReclenOffset]) | int(buf[direntReclenOffset+1])<<8, true
}

// direntPID extracts rec's NUL-terminated Name field as a pid, entirely
// without allocating a string: it walks the raw name bytes directly,
// accumulating a decimal value digit by digit, and rejects (ok=false) the
// instant it sees a non-digit byte or the NUL terminator with zero digits
// consumed — which is what makes this fast for /proc's ~60 non-pid entries
// (self, cpuinfo, meminfo, ..., plus "." and "..") as well as the numeric
// ones: every one of them is rejected (or accepted) in a single pass with
// no heap traffic at all.
func direntPID(rec []byte) (int, bool) {
	if len(rec) <= direntNameOffset {
		return 0, false
	}
	pid := 0
	digits := 0
	for _, c := range rec[direntNameOffset:] {
		if c == 0 {
			break
		}
		if c < '0' || c > '9' {
			return 0, false
		}
		pid = pid*10 + int(c-'0')
		digits++
	}
	return pid, digits > 0
}

// listPIDs ports list_pids(): every numeric entry under /proc. buf is a
// reusable scratch buffer (state.dirBuf), grown if too small for a single
// getdents64 call's worth of raw entries — mirrors readProcFile's
// buf/newBuf convention.
//
// Reads the directory via raw getdents64 syscalls instead of os.ReadDir:
// profiling showed os.ReadDir's sorted []DirEntry (one os.newUnixDirent
// allocation plus a cloned name string per entry, for every one of /proc's
// ~360+ entries including its ~60 non-pid ones) was a large share of every
// tick's allocations — for a sort order and DirEntry API this function
// immediately throws away in favor of a bare []int anyway. This parses
// straight out of the raw kernel buffer via direntPID, which only ever
// allocates for pids themselves (appending an int, not a string, to the
// result slice) — the non-pid entries that make up roughly a sixth of
// /proc's contents cost a handful of byte comparisons each, nothing more.
func listPIDs(buf []byte) (pids []int, newBuf []byte) {
	fd, err := syscall.Open("/proc", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, buf
	}
	defer syscall.Close(fd)

	pids = make([]int, 0, 512)
	for {
		n, err := syscall.Getdents(fd, buf)
		if err != nil || n <= 0 {
			break
		}
		data := buf[:n]
		for len(data) > 0 {
			reclen, ok := direntReclen(data)
			if !ok || reclen <= 0 || reclen > len(data) {
				break
			}
			rec := data[:reclen]
			data = data[reclen:]
			if pid, ok := direntPID(rec); ok {
				pids = append(pids, pid)
			}
		}
	}
	return pids, buf
}

// procStatPath builds the /proc/<pid>/stat path for a pid.
func procStatPath(pid int) string {
	return "/proc/" + strconv.Itoa(pid) + "/stat"
}

// readProcFile reads path via raw open/read/close syscalls into buf, growing
// buf if the file doesn't fit, and returns the filled data plus the
// (possibly reallocated) buffer to store back for reuse next call.
//
// This deliberately bypasses os.ReadFile. Profiling the poll loop showed
// ~95% of its CPU sits in the file syscalls, and os.ReadFile pays a stack of
// per-open overhead that is pure waste for these tiny, always-ready /proc
// files: an fstat to size a growable buffer (procfs reports size 0 anyway), a
// nonblocking fcntl, registration with the runtime network poller, and a GC
// finalizer. open/read/close straight to a reused buffer skips all of it.
func readProcFile(path string, buf []byte) (data, newBuf []byte, ok bool) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, buf, false
	}
	total := 0
	for {
		if total == len(buf) {
			buf = append(buf, make([]byte, len(buf))...) // grow (rare: /proc files are small)
		}
		n, err := syscall.Read(fd, buf[total:])
		if n > 0 {
			total += n
		}
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			syscall.Close(fd)
			return nil, buf, false
		}
		if n == 0 {
			break
		}
	}
	syscall.Close(fd)
	return buf[:total], buf, true
}

// bytesToStr aliases a byte slice as a string with no copy. Safe here because
// every caller only reads the string during parsing and never retains it past
// the next reuse of the underlying buffer.
func bytesToStr(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return unsafe.String(&b[0], len(b))
}

// readStatNameCPU reads /proc/<pid>/stat ONCE and returns the short kernel
// name (comm), cumulative user+system CPU seconds, and resident set size in
// kB. This is the convenience wrapper (own buffer); the poll loop reads via
// state.readBuf + parseStatNameCPU directly to reuse one buffer across pids.
func readStatNameCPU(pid int) (name string, cpuSecs float64, rssKb int64, ok bool) {
	data, _, ok := readProcFile("/proc/"+strconv.Itoa(pid)+"/stat", make([]byte, 4096))
	if !ok {
		return "", 0, 0, false
	}
	return parseStatNameCPU(bytesToStr(data))
}

// parseStatNameCPU is the pure parser behind readStatNameCPU, split out so
// the tricky comm-with-parens/spaces handling can be unit-tested against
// fixture lines without touching /proc. comm (the 2nd field) can contain
// spaces and parentheses, so — same trick as the Python version — we isolate
// the numeric tail by splitting after the LAST ')'.
//
// RSS now comes from stat field 24 (rss, in pages), which is identical to the
// resident field of /proc/<pid>/statm the loop used to read separately — same
// page-count semantics, so pulling it from this one read drops the per-pid
// file count from two to one with no change in the reported number. (Like
// statm's resident, this is the page-count RSS, which can differ slightly
// from status's VmRSS anon/file/shmem breakdown; immaterial for a ranking.)
func parseStatNameCPU(raw string) (name string, cpuSecs float64, rssKb int64, ok bool) {
	lp := strings.IndexByte(raw, '(')
	rp := strings.LastIndexByte(raw, ')')
	if lp == -1 || rp == -1 || rp < lp || rp+1 >= len(raw) {
		return "", 0, 0, false
	}
	// comm is between the first '(' and last ')'; the kernel caps it at 15
	// chars (no path/args), identical to what /proc/<pid>/comm reported.
	//
	// raw aliases the caller's reusable read buffer (bytesToStr is a
	// zero-copy cast), which gets overwritten by the next pid's /stat (and
	// /cmdline) read in the hot loop — so the substring is cloned into
	// independent memory *before* sanitize, not after. sanitize's fast path
	// returns its input unchanged when nothing needs fixing; if that input
	// were still the raw substring, the returned name would alias the
	// buffer too, and silently go stale (wrong text / garbage bytes) the
	// moment the loop reused it for the next pid. Cloning first means the
	// fast path's "return unchanged" is always already-independent memory.
	name = sanitize(strings.Clone(raw[lp+1 : rp]))
	if name == "" {
		name = "?"
	}

	// fields[0] is field 3 (state); utime is field 14, stime 15, rss 24 —
	// i.e. indices 11, 12, and 21 once state (field 3) is index 0.
	f11, f12, f21, haveCPU, haveRSS := statTailFields(raw[rp+1:])
	if !haveCPU {
		return name, 0, 0, false
	}
	utime, e1 := strconv.ParseInt(f11, 10, 64)
	stime, e2 := strconv.ParseInt(f12, 10, 64)
	if e1 != nil || e2 != nil {
		return name, 0, 0, false
	}
	// rss (field 24) is present in every real stat line; guard the index and
	// tolerate a parse miss so a short/odd line still yields CPU + name.
	if haveRSS {
		if pages, err := strconv.ParseInt(f21, 10, 64); err == nil {
			rssKb = pages * int64(pageSizeKB)
		}
	}
	return name, float64(utime+stime) / clkTck, rssKb, true
}

// statTailFields extracts fields 11, 12, and 21 (0-based, utime/stime/rss)
// of the /stat tail without allocating the ~49-element slice
// strings.Fields would to reach them — this runs once per process per tick,
// and only 3 of the ~49 fields it produced were ever used. Splits on plain
// ' ' rather than generic whitespace: /proc/<pid>/stat's numeric tail is
// always space-separated, so this is equivalent to strings.Fields here, just
// without materializing the intermediate slice.
//
// haveCPU mirrors the old `len(fields) < 13` guard (true once field 12 is
// reached, i.e. at least 13 fields exist); haveRSS mirrors `len(fields) >
// 21` (true once field 21 is reached).
func statTailFields(s string) (f11, f12, f21 string, haveCPU, haveRSS bool) {
	idx := 0
	i := 0
	for i < len(s) {
		for i < len(s) && s[i] == ' ' {
			i++
		}
		if i >= len(s) {
			break
		}
		start := i
		for i < len(s) && s[i] != ' ' {
			i++
		}
		field := s[start:i]
		switch idx {
		case 11:
			f11 = field
		case 12:
			f12 = field
			haveCPU = true
		case 21:
			f21 = field
			haveRSS = true
			return
		}
		idx++
	}
	return
}

// cmdFor returns the full COMMAND string for pid, reading and caching
// /proc/<pid>/cmdline on first sight and reusing the cached value on every
// later tick. A process's cmdline is effectively immutable for its lifetime,
// so this turns a per-tick read into a once-per-process read (dead pids are
// evicted from the cache in buildProcesses).
//
// buf is the same reusable read buffer buildProcesses' /stat read uses (it's
// safe to share: by the time cmdFor is called for a pid, that pid's /stat
// data has already been fully parsed into name/cpuSecs/rssKb, so nothing
// still references the old contents). newBuf is buf, possibly grown, for the
// caller to store back — same convention as readProcFile itself.
//
// Empty cmdline (kernel threads, some zombies) falls back to the bracketed
// short name, same as the Python version. A transient read *error* is not
// cached, so a busy process that briefly refuses the read self-corrects next
// tick instead of freezing on a fallback string.
func cmdFor(pid int, name string, cache map[int]string, buf []byte) (cmd string, newBuf []byte) {
	if c, ok := cache[pid]; ok {
		return c, buf
	}

	data, buf, ok := readProcFile("/proc/"+strconv.Itoa(pid)+"/cmdline", buf)
	if !ok {
		return bracketName(name), buf // don't cache — retry next tick
	}

	cmd = parseCmdline(data, name)
	cache[pid] = cmd
	return cmd, buf
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
// per pid (status, comm, cmdline, stat); this reads /stat alone (name + CPU +
// RSS) per tick, plus /cmdline only for pids not already cached. RSS used to
// need a second /statm read, but stat field 24 carries the same page-count
// value, so the steady-state per-pid file count is now one — the poll loop is
// dominated by these opens, so halving them roughly halves its CPU.
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

	pids, dirBuf := listPIDs(state.dirBuf)
	state.dirBuf = dirBuf
	procs := make([]Process, 0, len(pids))

	for _, pid := range pids {
		// One read per pid: /proc/<pid>/stat now yields name, CPU, and RSS.
		// The buffer is reused across pids (and grown back onto state) so the
		// hot loop allocates nothing for the read itself.
		data, buf, ok := readProcFile(procStatPath(pid), state.readBuf)
		state.readBuf = buf
		if !ok {
			// Process vanished between listing and reading (normal race).
			continue
		}
		name, cpuSecs, rssKb, statOK := parseStatNameCPU(bytesToStr(data))
		if !statOK {
			// Unparseable stat line — skip it.
			continue
		}
		seen[pid] = true

		cmd, buf := cmdFor(pid, name, cmdCache, state.readBuf)
		state.readBuf = buf
		p := Process{
			PID:       pid,
			RSSKb:     rssKb,
			Name:      name,
			NameLower: strings.ToLower(name),
			Cmd:       cmd,
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
