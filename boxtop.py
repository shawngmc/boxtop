#!/usr/bin/env python3
"""
boxtop.py — cgroup-aware memory monitor + per-process breakdown

Combines a cgroup v1/v2 memory usage bar (limit, current, available)
with a live top/htop-style process table sorted by RSS, using only
the stdlib (/proc parsing) so it runs in minimal containers without
psutil installed.
"""
import time
import os
import shutil
import sys
import select

try:
    import termios
    import tty
    _RAW_MODE_AVAILABLE = True
except ImportError:
    # termios/tty are POSIX-only. Interactive sort keys just won't work
    # on a platform without them (e.g. if this ever ran on Windows); the
    # monitor itself still runs fine on a plain fixed refresh interval.
    _RAW_MODE_AVAILABLE = False

# ---------------------------------------------------------------------------
# cgroup helpers (unchanged logic from the original script)
# ---------------------------------------------------------------------------

def read_cgroup_val(filename):
    paths = [f"/sys/fs/cgroup/{filename}", f"/sys/fs/cgroup/memory/{filename}"]
    for p in paths:
        if os.path.exists(p):
            with open(p, "r") as f:
                val = f.read().strip()
                return int(val) if val.isdigit() else None
    return None


# ---------------------------------------------------------------------------
# cgroup CPU helpers
# ---------------------------------------------------------------------------
# The CPU analog of memory.max is a quota/period pair that works out to a
# fractional core count — e.g. quota=200000, period=100000 means "2.0
# cores' worth of CPU time per period", same idea as a byte limit but for
# CPU time. cgroup v2 keeps both numbers in one file (cpu.max); v1 splits
# them across cpu.cfs_quota_us / cpu.cfs_period_us. A quota of "max" (v2)
# or -1 (v1) means no quota is set, in which case we fall back to the
# cpuset (which cores this cgroup is even allowed to run on) and finally
# to whatever the OS reports, so there's always *something* to show.

def _count_cpu_list(s):
    """Count cores in a cpuset-style list like '0-3,7' -> 5."""
    total = 0
    for part in s.split(","):
        part = part.strip()
        if not part:
            continue
        if "-" in part:
            a, b = part.split("-")
            total += int(b) - int(a) + 1
        else:
            total += 1
    return total


def read_cgroup_cpu_limit():
    """Return (cores, source) where source describes where the number came
    from ('quota', 'cpuset', or 'host') — the distinction matters because a
    quota is an enforced hard cap, while cpuset/host are just "how many
    cores exist to use", which is a softer notion of "limit". Returns
    (None, None) if nothing could be determined at all."""
    # cgroup v2: single file, "$QUOTA $PERIOD" or "max $PERIOD"
    p = "/sys/fs/cgroup/cpu.max"
    if os.path.exists(p):
        with open(p, "r") as f:
            parts = f.read().strip().split()
        if len(parts) == 2 and parts[0] != "max":
            try:
                quota, period = int(parts[0]), int(parts[1])
                if period > 0:
                    return quota / period, "quota"
            except ValueError:
                pass
    else:
        # cgroup v1: two separate files
        quota_p = "/sys/fs/cgroup/cpu/cpu.cfs_quota_us"
        period_p = "/sys/fs/cgroup/cpu/cpu.cfs_period_us"
        if os.path.exists(quota_p) and os.path.exists(period_p):
            try:
                with open(quota_p, "r") as f:
                    quota = int(f.read().strip())
                with open(period_p, "r") as f:
                    period = int(f.read().strip())
                if quota > 0 and period > 0:
                    return quota / period, "quota"
            except ValueError:
                pass

    # No enforced quota — fall back to the cpuset (cores this cgroup is
    # pinned to), which still caps total achievable CPU%.
    for p in ("/sys/fs/cgroup/cpuset.cpus.effective",
              "/sys/fs/cgroup/cpuset.cpus",
              "/sys/fs/cgroup/cpuset/cpuset.cpus"):
        if os.path.exists(p):
            with open(p, "r") as f:
                s = f.read().strip()
            if s:
                n = _count_cpu_list(s)
                if n:
                    return float(n), "cpuset"

    # Last resort: cores actually schedulable by this process, or the
    # host's total core count.
    try:
        n = len(os.sched_getaffinity(0))
        if n:
            return float(n), "host"
    except (AttributeError, OSError):
        pass
    n = os.cpu_count()
    if n:
        return float(n), "host"
    return None, None


def read_cgroup_cpu_usage_usec():
    """Cumulative CPU time consumed by the whole cgroup, in microseconds,
    since the cgroup was created. This is a running counter — CPU % has to
    be derived by sampling it twice and dividing the delta by wall-clock
    time and core count, same as `top` does."""
    p = "/sys/fs/cgroup/cpu.stat"
    if os.path.exists(p):
        with open(p, "r") as f:
            for line in f:
                if line.startswith("usage_usec"):
                    return int(line.split()[1])
    for p in ("/sys/fs/cgroup/cpuacct/cpuacct.usage",
              "/sys/fs/cgroup/cpu,cpuacct/cpuacct.usage"):
        if os.path.exists(p):
            with open(p, "r") as f:
                ns = f.read().strip()
            if ns.isdigit():
                return int(ns) // 1000  # v1 reports nanoseconds
    return None


# Sampling state persists across render() calls (module-level, one monitor
# process) so each frame's CPU% reflects usage since the *previous* frame,
# not since the cgroup was created.
_cpu_prev_usage_usec = None
_cpu_prev_time = None


def sample_cpu_pct(cores_limit):
    """Return CPU% (0-100, where 100% == fully using `cores_limit` cores)
    since the last call, or None on the very first call (no baseline yet
    to diff against) or if usage data isn't available."""
    global _cpu_prev_usage_usec, _cpu_prev_time

    usage_usec = read_cgroup_cpu_usage_usec()
    now = time.monotonic()
    pct = None

    if usage_usec is not None and cores_limit:
        if _cpu_prev_usage_usec is not None:
            dt = now - _cpu_prev_time
            d_usage = usage_usec - _cpu_prev_usage_usec
            if dt > 0:
                pct = (d_usage / 1_000_000) / dt / cores_limit * 100

    if usage_usec is not None:
        _cpu_prev_usage_usec = usage_usec
        _cpu_prev_time = now

    return pct


# ---------------------------------------------------------------------------
# /proc process helpers
# ---------------------------------------------------------------------------

PAGE_KB = os.sysconf("SC_PAGE_SIZE") // 1024 if hasattr(os, "sysconf") else 4


def list_pids():
    return [p for p in os.listdir("/proc") if p.isdigit()]


_CTRL_CHARS = {c: " " for c in range(32) if c not in (0,)}  # NUL handled separately


def sanitize(s):
    """Replace control characters (newlines, tabs, etc.) that could appear
    in a process's argv/comm and would otherwise break the table's
    line-based layout."""
    return s.translate(_CTRL_CHARS)


def read_process(pid):
    """Return dict with pid, rss_kb, name (short exe name), cmd (full
    path/args) — or None if the process vanished before we could read it
    (normal race condition)."""
    base = f"/proc/{pid}"
    try:
        rss_kb = None
        with open(f"{base}/status", "r") as f:
            for line in f:
                if line.startswith("VmRSS:"):
                    # e.g. "VmRSS:\t   12345 kB"
                    rss_kb = int(line.split()[1])
                    break

        # Short kernel-reported executable name (what `top`'s default
        # COMMAND column and `ps -o comm=` show) — max 15 chars by the
        # kernel, no path, no args.
        name = ""
        try:
            with open(f"{base}/comm", "r") as f:
                name = sanitize(f.read().strip())
        except (FileNotFoundError, PermissionError):
            name = "?"

        # Full command line (path + args), NUL-separated
        cmd = ""
        try:
            with open(f"{base}/cmdline", "rb") as f:
                raw = f.read()
            if raw:
                cmd = sanitize(
                    raw.replace(b"\x00", b" ").decode("utf-8", "replace").strip()
                )
        except (FileNotFoundError, PermissionError):
            pass

        # Fallback to the short name (bracketed, kernel-thread style) if
        # cmdline is empty, which happens for kernel threads / some zombies.
        if not cmd:
            cmd = f"[{name}]" if name else "[unknown]"

        if rss_kb is None:
            rss_kb = 0

        return {"pid": int(pid), "rss_kb": rss_kb, "name": name, "cmd": cmd}
    except (FileNotFoundError, ProcessLookupError):
        # Process exited between listdir() and open() — just skip it.
        return None
    except PermissionError:
        return {"pid": int(pid), "rss_kb": 0, "name": "?", "cmd": "[permission denied]"}


def get_processes():
    """Return all readable processes, unsorted — sort order is applied
    later in render() once CPU% has been computed too (sorting here would
    be too early for a CPU-based sort, since that requires a second sample
    that only exists after this list is built)."""
    procs = []
    for pid in list_pids():
        info = read_process(pid)
        if info is not None:
            procs.append(info)
    return procs


# ---------------------------------------------------------------------------
# per-process CPU sampling
# ---------------------------------------------------------------------------
# /proc/<pid>/stat's utime/stime fields are cumulative CPU time (in clock
# ticks) the process has consumed since it started — same idea as the
# cgroup's usage_usec counter, just per-process and in different units. CPU%
# again has to come from sampling twice and diffing, so each process's
# reading reflects usage since the *previous* frame.

CLK_TCK = os.sysconf("SC_CLK_TCK") if hasattr(os, "sysconf") else 100


def read_process_cpu_seconds(pid):
    """Return cumulative user+system CPU seconds for this process (all its
    threads combined), or None if it can't be read."""
    try:
        with open(f"/proc/{pid}/stat", "r") as f:
            raw = f.read()
        # comm (2nd field) is user-controlled and can contain spaces or
        # parens, so split on the LAST ')' to safely isolate the fields
        # that follow it rather than naively splitting on whitespace.
        after_comm = raw.rsplit(")", 1)[1].split()
        # after_comm[0] is field 3 (state); utime is field 14, stime is 15
        utime = int(after_comm[11])
        stime = int(after_comm[12])
        return (utime + stime) / CLK_TCK
    except (FileNotFoundError, ProcessLookupError, IndexError, ValueError):
        return None
    except PermissionError:
        return None


# Sampling state persists across render() calls, one entry per pid, same
# pattern as the cgroup-level sampler above.
_proc_cpu_prev = {}


def sample_process_cpu_pct(procs, cores_limit):
    """Mutate each process dict in `procs`, adding a 'cpu_pct' key — CPU%
    since the last call, normalized against `cores_limit` so it's directly
    comparable to the cgroup summary's CPU%. None on a process's first
    sample (no baseline yet) or if cores_limit/timing isn't available."""
    global _proc_cpu_prev
    now = time.monotonic()
    seen_pids = set()

    for p in procs:
        pid = p["pid"]
        seen_pids.add(pid)
        cpu_secs = read_process_cpu_seconds(pid)

        cpu_pct = None
        if cpu_secs is not None:
            prev = _proc_cpu_prev.get(pid)
            if prev is not None and cores_limit:
                prev_secs, prev_t = prev
                dt = now - prev_t
                if dt > 0:
                    cpu_pct = max(0.0, (cpu_secs - prev_secs) / dt / cores_limit * 100)
            _proc_cpu_prev[pid] = (cpu_secs, now)

        p["cpu_pct"] = cpu_pct

    # Drop bookkeeping for pids that no longer exist, so this doesn't grow
    # without bound over a long-running session.
    for pid in set(_proc_cpu_prev) - seen_pids:
        del _proc_cpu_prev[pid]

    return procs


# ---------------------------------------------------------------------------
# rendering
# ---------------------------------------------------------------------------

# ANSI helpers — used instead of `clear` to avoid the blank-frame flicker
# that a full screen clear causes on slow terminals/links. We move the
# cursor home and overwrite the previous frame in place; \033[K erases any
# leftover characters on each line (e.g. a shorter command string this
# cycle), and \033[J at the very end trims any leftover rows below if the
# process count or terminal size shrank.
CUR_HOME = "\033[H"
CLR_EOL = "\033[K"
CLR_DOWN = "\033[J"
HIDE_CURSOR = "\033[?25l"
SHOW_CURSOR = "\033[?25h"
RESET = "\033[0m"

# Colors are opt-out: disabled if NO_COLOR is set (https://no-color.org) or
# if stdout isn't a real terminal (e.g. piped to a file/log collector).
COLOR_ENABLED = ("NO_COLOR" not in os.environ) and sys.stdout.isatty()


def gradient_color(frac, stops):
    """Map frac in [0, 1] to a 24-bit ANSI color escape by linearly
    interpolating through an arbitrary list of (position, (r, g, b)) stops,
    sorted ascending by position. frac is clamped to the stops' own range —
    values past the last stop just render as that stop's color (e.g. a
    process at 80% still renders as the same bright red as one at 50%,
    rather than going "past red")."""
    frac = max(0.0, min(1.0, frac))

    if frac <= stops[0][0]:
        r, g, b = stops[0][1]
        return f"\033[38;2;{r};{g};{b}m"
    if frac >= stops[-1][0]:
        r, g, b = stops[-1][1]
        return f"\033[38;2;{r};{g};{b}m"

    for (p0, c0), (p1, c1) in zip(stops, stops[1:]):
        if p0 <= frac <= p1:
            t = (frac - p0) / (p1 - p0) if p1 > p0 else 0
            r = int(c0[0] + (c1[0] - c0[0]) * t)
            g = int(c0[1] + (c1[1] - c0[1]) * t)
            b = int(c0[2] + (c1[2] - c0[2]) * t)
            return f"\033[38;2;{r};{g};{b}m"

    # Unreachable given the clamps above, but keep a safe fallback.
    r, g, b = stops[-1][1]
    return f"\033[38;2;{r};{g};{b}m"


GREEN = (0, 255, 0)
YELLOW = (255, 255, 0)
RED = (255, 0, 0)

# Summary bar/percentage: unchanged behavior — plain 0% -> 100%-of-limit
# gradient (green at empty, yellow at half full, red at the limit).
SUMMARY_STOPS = [(0.0, GREEN), (0.5, YELLOW), (1.0, RED)]

# Per-process RSS column: a much steeper curve, since a *single* process
# eating a large share of the whole container's budget is alarming well
# before it hits 50%. Green near 0%, yellow by 10%, bright red by 50% (and
# anything above 50% stays that same red rather than intensifying further).
PROCESS_STOPS = [(0.0, GREEN), (0.10, YELLOW), (0.50, RED)]

# Per-process CPU% column reuses the gentler summary curve rather than
# PROCESS_STOPS: a single process maxing out its cgroup's CPU budget is
# normal and often desirable (just means it's making full use of what it's
# allowed), unlike memory where a single process eating a large share
# raises OOM-kill risk for the whole cgroup. So CPU gets green->0%,
# yellow->50%, red->100%, matching the summary bar directly above it.
PROCESS_CPU_STOPS = SUMMARY_STOPS


def colorize(text, frac, stops):
    if not COLOR_ENABLED:
        return text
    return f"{gradient_color(frac, stops)}{text}{RESET}"


def fmt_mb(kb):
    return f"{kb / 1024:.1f}"


# ---------------------------------------------------------------------------
# top-style interactive sorting
# ---------------------------------------------------------------------------
# One key per column, plus 'r' to flip direction. Pressing the key for the
# column that's already active flips its direction too — same as clicking
# an already-sorted table header again — so 'm' toggles memory high->low
# and low->high rather than doing nothing on the second press.

SORT_COLUMN_KEYS = {"m": "rss", "c": "cpu", "p": "pid", "n": "name"}

# Per-column default direction when switching TO that column fresh: the
# numeric columns default to descending (biggest offender first, which is
# what you want when scanning for a problem process) while name defaults
# ascending (alphabetical reads naturally top-to-bottom).
SORT_DEFAULT_REVERSE = {"rss": True, "cpu": True, "pid": False, "name": False}

# Mutable interactive state — module-level since it needs to persist
# across render() calls (one monitor process, one terminal session).
sort_column = "rss"
sort_reverse = True


def handle_sort_key(ch):
    """Update the module-level sort state in response to a single
    keypress. Unrecognized keys are ignored."""
    global sort_column, sort_reverse

    ch = ch.lower()
    if ch == "r":
        sort_reverse = not sort_reverse
    elif ch in SORT_COLUMN_KEYS:
        col = SORT_COLUMN_KEYS[ch]
        if col == sort_column:
            sort_reverse = not sort_reverse
        else:
            sort_column = col
            sort_reverse = SORT_DEFAULT_REVERSE[col]


def sort_processes(procs):
    """Sort in place per the current module-level sort state and return
    the list (for chaining). CPU% can be None (no baseline sample yet);
    those processes sort as if they were 0 rather than breaking the sort
    or clumping unpredictably."""
    if sort_column == "rss":
        key_func = lambda p: p["rss_kb"]
    elif sort_column == "cpu":
        key_func = lambda p: p["cpu_pct"] if p["cpu_pct"] is not None else 0
    elif sort_column == "pid":
        key_func = lambda p: p["pid"]
    else:  # "name"
        key_func = lambda p: p["name"].lower()

    procs.sort(key=key_func, reverse=sort_reverse)
    return procs


def col_header(label, width, align, active):
    """Render one column's header text, appending a direction arrow (▼
    descending / ▲ ascending) when this is the active sort column, so the
    sorted column is identifiable at a glance the way `top`/spreadsheet
    apps mark a sorted header."""
    text = f"{label}{'▼' if sort_reverse else '▲'}" if active else label
    return f"{text:>{width}}" if align == "right" else f"{text:<{width}}"


def render(max_bytes, curr_bytes):
    max_mb = max_bytes // (1024 * 1024)
    curr_mb = curr_bytes // (1024 * 1024)
    pct = (curr_mb / max_mb) * 100 if max_mb else 0

    bar_length = 30
    filled = int(bar_length * curr_mb // max_mb) if max_mb else 0
    bar = "█" * filled + "░" * (bar_length - filled)
    bar_colored = colorize(bar, pct / 100, SUMMARY_STOPS)
    pct_colored = colorize(f"{pct:.1f}%", pct / 100, SUMMARY_STOPS)

    term_cols, term_rows = shutil.get_terminal_size(fallback=(100, 30))

    lines = []

    # Each header row's caption is built from two independently
    # right-justified pieces — a section title (RAM/CPU, blank on
    # continuation rows) and a field name (Limit/Available/etc.) — so a
    # section's rows all line up under a shared right edge while only the
    # first row of each section carries its title. Widths are derived from
    # the actual caption text rather than hardcoded, so this stays correct
    # if a caption's wording ever changes.
    SECTION_WIDTH = max(len("RAM"), len("CPU")) + 1
    LEFT_FIELD_CAPTIONS = ["Limit", "Current Usage", "Available", "Progress"]
    LEFT_FIELD_WIDTH = max(len(c) for c in LEFT_FIELD_CAPTIONS) + 1
    RIGHT_FIELD_CAPTIONS = ["Limit", "Progress"]
    RIGHT_FIELD_WIDTH = max(len(c) for c in RIGHT_FIELD_CAPTIONS) + 1

    # CPU's section sits far enough right that it clears the memory bar's
    # width comfortably — tuned for terminals a bit wider than the ~80-100
    # column default; on a narrower terminal this row will just run long
    # (same graceful-non-clipping behavior the header block already had).
    DIVIDER_COL = 62

    def caption(section, field, field_width):
        sec = f"{section:>{SECTION_WIDTH}}" if section else " " * SECTION_WIDTH
        return f"{sec}{field:>{field_width}}"

    mem_row1_left = f"{caption('RAM', 'Limit', LEFT_FIELD_WIDTH)} : {max_mb:>6} MB"
    mem_row2_left = f"{caption('', 'Current Usage', LEFT_FIELD_WIDTH)} : {curr_mb:>6} MB"
    mem_row3 = f"{caption('', 'Available', LEFT_FIELD_WIDTH)} : {(max_mb - curr_mb):>6} MB"
    mem_row4 = f"{caption('', 'Progress', LEFT_FIELD_WIDTH)} : [{bar_colored}] {pct_colored}"

    cores_limit, cpu_source = read_cgroup_cpu_limit()
    cpu_row1_right = cpu_row2_right = None
    if cores_limit:
        cpu_pct = sample_cpu_pct(cores_limit)
        source_label = {"quota": "cgroup quota", "cpuset": "cpuset",
                         "host": "host, no limit set"}.get(cpu_source, cpu_source)
        cpu_row1_right = (f"{caption('CPU', 'Limit', RIGHT_FIELD_WIDTH)} : "
                           f"{cores_limit:>6.2f} cores ({source_label})")
        if cpu_pct is None:
            cpu_row2_right = f"{caption('', 'Progress', RIGHT_FIELD_WIDTH)} : measuring..."
        else:
            cpu_frac = max(0.0, min(1.0, cpu_pct / 100))
            cpu_bar_len = 30
            cpu_filled = int(cpu_bar_len * cpu_frac)
            cpu_bar = "█" * cpu_filled + "░" * (cpu_bar_len - cpu_filled)
            cpu_bar_colored = colorize(cpu_bar, cpu_frac, SUMMARY_STOPS)
            cpu_pct_colored = colorize(f"{cpu_pct:5.1f}%", cpu_frac, SUMMARY_STOPS)
            cpu_row2_right = (f"{caption('', 'Progress', RIGHT_FIELD_WIDTH)} : "
                               f"[{cpu_bar_colored}] {cpu_pct_colored}")
    else:
        cpu_row1_right = (f"{caption('CPU', 'Limit', RIGHT_FIELD_WIDTH)} : "
                           f"unavailable (no cgroup cpu controller found)")

    # CPU sits beside memory (in the horizontal space the terminal already
    # has to spare) rather than as its own block underneath — pad the two
    # memory lines that have a CPU counterpart out to a shared column so
    # "CPU Limit"/"CPU Progress" line up under each other on the right.
    lines.append(mem_row1_left.ljust(DIVIDER_COL) + "│" + cpu_row1_right)
    if cpu_row2_right:
        lines.append(mem_row2_left.ljust(DIVIDER_COL) + "│" + cpu_row2_right)
    else:
        lines.append(mem_row2_left)

    lines.append(mem_row3)
    lines.append(mem_row4)
    lines.append("=" * 45)

    # Fixed columns: PID(7) NAME(16) CPU%(7) RSS(9), remaining goes to COMMAND
    name_width = 16
    cpu_width = 7
    fixed_width = 1 + 7 + 2 + name_width + 2 + cpu_width + 2 + 9 + 2  # leading space + cols + gaps
    cmd_width = max(15, term_cols - fixed_width)

    header = (f" {col_header('PID', 7, 'right', sort_column == 'pid')}  "
              f"{col_header('NAME', name_width, 'left', sort_column == 'name')}  "
              f"{col_header('CPU%', cpu_width, 'right', sort_column == 'cpu')}  "
              f"{col_header('RSS (MB)', 9, 'right', sort_column == 'rss')}  "
              f"{'COMMAND':<{cmd_width}}")
    footer_rule = "-" * min(term_cols, fixed_width + cmd_width)

    # Everything above (top summary block) plus everything fixed below the
    # process rows (table header, table header rule, footer rule, summary
    # line, exit prompt) eats into the terminal's row budget. What's left
    # over goes to the process rows themselves — this has to be exact or
    # the frame runs past the bottom of the terminal and scrolls, which is
    # exactly the flicker/spill we're trying to avoid.
    fixed_non_process_rows = len(lines) + 2 + 3  # +2 header/rule, +3 footer/summary/exit
    max_proc_rows = max(3, term_rows - fixed_non_process_rows)

    procs = get_processes()
    sample_process_cpu_pct(procs, cores_limit)
    sort_processes(procs)
    total_rss_kb = sum(p["rss_kb"] for p in procs)

    # If we can't fit every process, reserve one more row for the "N more
    # not shown" notice so the total still lands exactly on budget instead
    # of pushing everything down by one line.
    truncated = len(procs) > max_proc_rows
    shown_rows = max_proc_rows - 1 if truncated else max_proc_rows

    lines.append(header[:term_cols])
    lines.append(footer_rule)

    # Color each process's RSS by its share of the cgroup's memory *limit*
    # (not just what's currently in use) — "50% of the container's total
    # budget" is a fixed, absolute threshold regardless of how much of that
    # budget happens to be in use right now.
    max_kb = max_bytes / 1024 if max_bytes else 0

    for p in procs[:shown_rows]:
        name = p["name"]
        if len(name) > name_width:
            name = name[: name_width - 1] + "…"

        cmd = p["cmd"]
        if len(cmd) > cmd_width:
            cmd = cmd[: cmd_width - 1] + "…"

        frac = (p["rss_kb"] / max_kb) if max_kb else 0
        rss_field = colorize(f"{fmt_mb(p['rss_kb']):>9}", frac, PROCESS_STOPS)

        if p["cpu_pct"] is None:
            cpu_field = f"{'--':>{cpu_width}}"
        else:
            cpu_frac = (p["cpu_pct"] / 100) if cores_limit else 0
            cpu_field = colorize(f"{p['cpu_pct']:>{cpu_width-1}.1f}%", cpu_frac, PROCESS_CPU_STOPS)

        line = (f" {p['pid']:>7}  {name:<{name_width}}  {cpu_field}  "
                f"{rss_field}  {cmd:<{cmd_width}}")
        lines.append(line)

    if truncated:
        lines.append(f" ... {len(procs) - shown_rows} more processes not shown ...")

    lines.append(footer_rule)
    lines.append(f" Processes: {len(procs):<5}  Sum of RSS: {fmt_mb(total_rss_kb)} MB"
                  f"  (cgroup usage may include cache/shared mem not in RSS)")
    lines.append(" Ctrl+C exit | Sort: [m]em [c]pu [p]id [n]ame  [r]everse")

    # Join with CLR_EOL before each newline so leftover characters from a
    # longer previous line (e.g. a longer command string) get wiped even
    # though we're not clearing the whole screen.
    frame = (CLR_EOL + "\n").join(lines)
    return frame + CLR_EOL + CLR_DOWN


def main():
    interval = 1.0
    if len(sys.argv) > 1:
        try:
            interval = float(sys.argv[1])
        except ValueError:
            print(f"Usage: {sys.argv[0]} [refresh_interval_seconds]")
            sys.exit(1)

    # Raw/cbreak mode lets us read single keypresses (m/c/p/n/r) without
    # waiting for Enter, without echoing them to the screen, and without
    # blocking the refresh loop. Only bother if stdin is actually an
    # interactive terminal — piping/redirecting stdin (e.g. cron, a log
    # collector) should just run on the plain timer instead of erroring.
    interactive = _RAW_MODE_AVAILABLE and sys.stdin.isatty()
    old_term_settings = None
    if interactive:
        old_term_settings = termios.tcgetattr(sys.stdin.fileno())
        tty.setcbreak(sys.stdin.fileno())

    sys.stdout.write(HIDE_CURSOR)
    try:
        while True:
            max_bytes = read_cgroup_val("memory.max") or read_cgroup_val("memory.limit_in_bytes")
            curr_bytes = read_cgroup_val("memory.current") or read_cgroup_val("memory.usage_in_bytes")

            if not max_bytes or not curr_bytes:
                print("Error: Could not read cgroup values. Are resource limits set?")
                break

            frame = CUR_HOME + render(max_bytes, curr_bytes)
            sys.stdout.write(frame)
            sys.stdout.flush()

            if interactive:
                # Wait up to `interval` seconds, but wake immediately if a
                # key is pressed so sort changes feel instant rather than
                # waiting out the rest of the refresh interval.
                ready, _, _ = select.select([sys.stdin], [], [], interval)
                if ready:
                    ch = sys.stdin.read(1)
                    if ch == "":
                        # stdin closed out from under us — fall back to a
                        # plain timer instead of busy-looping on selects
                        # that immediately return empty reads forever.
                        interactive = False
                        time.sleep(interval)
                    elif ch.lower() == "q":
                        break
                    else:
                        handle_sort_key(ch)
            else:
                time.sleep(interval)
    except KeyboardInterrupt:
        pass
    finally:
        if interactive:
            termios.tcsetattr(sys.stdin.fileno(), termios.TCSADRAIN, old_term_settings)
        sys.stdout.write(SHOW_CURSOR)
        sys.stdout.flush()
        print("\nExiting.")


if __name__ == "__main__":
    main()
