# boxtop 

A tool to measure cgroup-based workspaces (docker, k8s, etc.).

Features:
- Side-by-side cgroup and system-wide RAM/CPU/Swap meters in the top bar,
  so a confined workload's own usage and the host it's sharing are both
  visible at a glance
- Swap accounting: cgroup swap (`memory.swap.current`/`memory.swap.max` on
  v2, derived from `memory.memsw.*` on v1) and system-wide swap
  (`/proc/meminfo`'s `SwapTotal`/`SwapFree`), each with a graceful
  "unavailable"/"no swap configured" state when swap accounting or swap
  itself isn't present
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
- `--cgroup <name-or-path>` points boxtop at any cgroup on the host instead
  of its own — e.g. `--cgroup docker/1a2b3c4d5e6f` or a full
  `/sys/fs/cgroup/...` path — so it can run on the host and watch a
  container's RAM/CPU/Swap meters without `docker exec`-ing into it (the
  process list is still the host's own `/proc`, unscoped by `--cgroup`);
  `--list-cgroups` prints every cgroup name found on the host to help pick
  one



## Building

See [CONTRIBUTING.md](CONTRIBUTING.md) for build, test, and release
instructions.

## TODOs
- **No cache-vs-anon breakdown.** `memory.stat`'s `anon`/`file` split would
  let users tell reclaimable page cache apart from a real leak, instead of
  just disclaiming the ambiguity in the footer line.
- **No trend/history view.** A small sparkline of RAM%/CPU% over the last
  N samples would show a slow climb toward the limit, not just the
  instantaneous value.
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
