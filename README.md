# boxtop 

A tool to measure cgroup-based workspaces (docker, k8s, etc.).

Features:
- Mouse support for column sorting and scrolling via wheel
- Falls back to host constraints if not constrained/in cgroup
- Colorblind mode via `--colorblind`



## Building

See [CONTRIBUTING.md](CONTRIBUTING.md) for build, test, and release
instructions.

## TODOs
- **No raw-mode fallback path.** This should detects a non-interactive
  stdin (piped/redirected) and fall back to a plain timer with no key
  handling. This scaffold always runs through tcell's event loop; add an
  `isatty`-style check up front if you need the non-interactive mode.
- **`golang.org/x/sys/unix` niceties omitted.** `runtimeNumCPU()` uses
  `runtime.NumCPU()` (host total) rather than
  `unix.SchedGetaffinity` (schedulable cores), and `CLK_TCK`/page size
  are hardcoded constants rather than read via `sysconf`. Noted inline
  in `util.go` — swap in `x/sys/unix` if exact parity matters for your
  containers.
- **Show CPU Make/Model and Clockspeed**
- **No OOM-kill visibility.** `memory.events` (cgroup v2) exposes
  `oom_kill`/`oom` counters. Surfacing a rising count would tell a user the
  kernel has already started reaping processes, instead of just showing a
  RAM bar pinned at 100%.
- **No swap accounting.** `memory.swap.current`/`memory.swap.max` are
  tracked separately from `memory.max` in cgroup v2. The RAM bar can look
  fine while swap is thrashing.
- **No cache-vs-anon breakdown.** `memory.stat`'s `anon`/`file` split would
  let users tell reclaimable page cache apart from a real leak, instead of
  just disclaiming the ambiguity in the footer line.
- **No help/keybinding screen.** All keys are packed into one footer line
  in `render.go`; a `h`/`?` overlay would scale better as more keys are
  added.
- **No filter/search.** An htop-`F4`-style incremental name filter would
  help once process counts grow.
- **No kill action.** Pressing a key (e.g. `k`) to send a signal to the
  selected process, gated behind a confirm step, is standard for this
  class of tool and not yet implemented.
- **No trend/history view.** A small sparkline of RAM%/CPU% over the last
  N samples would show a slow climb toward the limit, not just the
  instantaneous value.
- **Truncation is rune-count, not display-width.** The name/cmd truncation
  in `render.go` counts runes via `len([]rune(...))`, not terminal
  columns, so wide (e.g. CJK) characters desync column alignment.
  `go-runewidth` is already a transitive dependency via tcell — use
  `runewidth.Truncate`/`runewidth.Width` instead.
- **Flag parsing is hand-rolled.** `main.go` parses `os.Args[1]` directly
  with no `--help`/`--version`. Moving to the stdlib `flag` package would
  give free usage text and room for `--interval`, `--cgroup <path>`,
  `--no-color`, etc.
- **No version string.** Add a `var version = "dev"` set via
  `-ldflags "-X main.version=..."` in the release workflow, and print it
  on `--version`.
- **Hardcoded to the running process's own cgroup.** `cgroup.go` always
  reads `/sys/fs/cgroup/...` for wherever boxtop itself is running. A
  `--cgroup <name-or-path>` flag would let boxtop run on the host and
  watch a specific container without `docker exec`-ing into it.
- **CI only runs on tag push or manual dispatch.** `.github/workflows/
  release.yml` runs tests before a release build, but nothing runs
  `go test`/`go vet`/`gofmt -l` on every push or PR to `main`, so a broken
  commit can sit on `main` unnoticed until a release is cut. A separate
  `ci.yml` on `push`/`pull_request` (plus maybe `golangci-lint`) would
  close that gap.
- **No Dependabot config** for the tcell/x-sys dependency tree.

## AI Disclosure
Claude Code was used to help write this, including the original python
prototype. This is being used as an entry point into Go, a language
I haven't used much, so feedback is appreciated.
