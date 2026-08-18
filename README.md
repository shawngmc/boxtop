# boxtop 

A top-like tool to measure cgroup-based workspaces (docker, k8s, etc.), focused
on usability and process control.

Features:
- CGroup-relevant accounting for processes
- Side-by-side cgroup and system-wide RAM/CPU/Swap meters in the top bar
- Surfaces the cgroup's OOM-kill count, when available, so a kernel-reaped
  process shows up even after the RAM bar drops back under 100%
- Usability
  - Mouse support for sorting, scrolling and selection
  - Colorblind mode via `--colorblind`
  - Non-interactive mode (single snapshot) - auto-enabled when stdin or stdout
    isn't a terminal (piped/redirected), or forced with `--non-interactive`/`-n`
- Tools
  - Filtering via `/` or pre-apply with `--filter`/`-f`
  - Kill processes with `k`
  - Process details vai `Enter`
- CGroups
  - In container, limited to container's cgroups
  - Select at launch with `--cgroup <name-or-path>`
  - List via CLI with `--list-cgroups`
  - Interactive switching via `g`
  - Hide cgroup contraints if not in cgroup


## Building

See [CONTRIBUTING.md](CONTRIBUTING.md) for build, test, and release
instructions.

## TODOs
- Add 'niceness' adjustment
- **No cache-vs-anon breakdown.** `memory.stat`'s `anon`/`file` split would
  let users tell reclaimable page cache apart from a real leak, instead of
  just disclaiming the ambiguity in the footer line.
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
