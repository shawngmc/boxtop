# boxtop 

A tool to measure cgroup-based workspaces (docker, k8s, etc.).

Features:
- Mouse support for column sorting and scrolling via wheel
- Falls back to host constraints if not constrained/in cgroup
- Colorblind mode via `--colorblind`
- Surfaces the cgroup's OOM-kill count, when available, so a kernel-reaped
  process shows up even after the RAM bar drops back under 100%
- Incremental name/command filter: press `/` to type, `Enter` to apply,
  `Esc` to clear — or start pre-filtered with `--filter`/`-f`
- Process details popup: press `Enter` on a selected row for PPID, state,
  owning user, thread count, nice value, VmSize/VmSwap, and the resolved
  exe path; `Enter`/`Esc`/`q` closes it
- Keybinding help screen: press `h` or `?` for an overlay listing every key
  and mouse action; `Enter`/`Esc`/`q` closes it
- Non-interactive mode: prints one snapshot (header, full process list,
  simplified footer) and exits instead of running the TUI — auto-enabled
  when stdin or stdout isn't a terminal (piped/redirected), or forced with
  `--non-interactive`/`-n`



## Building

See [CONTRIBUTING.md](CONTRIBUTING.md) for build, test, and release
instructions.

## TODOs
- **`golang.org/x/sys/unix` niceties omitted.** `runtimeNumCPU()` uses
  `runtime.NumCPU()` (host total) rather than
  `unix.SchedGetaffinity` (schedulable cores), and `CLK_TCK`/page size
  are hardcoded constants rather than read via `sysconf`. Noted inline
  in `util.go` — swap in `x/sys/unix` if exact parity matters for your
  containers.
- **No swap accounting.** `memory.swap.current`/`memory.swap.max` are
  tracked separately from `memory.max` in cgroup v2. The RAM bar can look
  fine while swap is thrashing.
- **No cache-vs-anon breakdown.** `memory.stat`'s `anon`/`file` split would
  let users tell reclaimable page cache apart from a real leak, instead of
  just disclaiming the ambiguity in the footer line.
- **No trend/history view.** A small sparkline of RAM%/CPU% over the last
  N samples would show a slow climb toward the limit, not just the
  instantaneous value.
- **Truncation is rune-count, not display-width.** The name/cmd truncation
  in `render.go` counts runes via `len([]rune(...))`, not terminal
  columns, so wide (e.g. CJK) characters desync column alignment.
  `go-runewidth` is already a transitive dependency via tcell — use
  `runewidth.Truncate`/`runewidth.Width` instead.
- **Hardcoded to the running process's own cgroup.** `cgroup.go` always
  reads `/sys/fs/cgroup/...` for wherever boxtop itself is running. A
  `--cgroup <name-or-path>` flag would let boxtop run on the host and
  watch a specific container without `docker exec`-ing into it.
- **Kernel thread cmdline reads are wasted.** `cmdFor` in `proc.go` does a
  full `open`/`read`/`close` of `/proc/<pid>/cmdline` on every cache miss,
  which always comes back empty for kernel threads. The `flags` field
  already present in the `/proc/<pid>/stat` line `parseStatNameCPU` reads
  (`PF_KTHREAD`, `0x00200000`) could detect kernel threads for free and
  skip straight to the bracketed name, saving a syscall per newly-seen
  kthread. RSS needs no such fix — it's already 0 for kernel threads and
  parsed from that same single `/stat` read at no extra cost.


## AI Disclosure
Claude Code was used to help write this, including the original python
prototype. This is being used as an entry point into Go, a language
I haven't used much, so feedback is appreciated.
