# Porting boxtop to Rust — exploration notes

This is a hypothetical-conversion writeup, not a plan of record. It captures what
a Go→Rust port of boxtop would actually involve, based on the code as of the
`rust-conversion-exploration` branch (cut from `main` at commit `a851bfa`). No
Rust code exists yet; this is groundwork for deciding whether to attempt one.

## Codebase snapshot

~8,600 lines total across the module (root package + `cmd/boxbench`, plus a
separate `integration/` test package). Of the root + boxbench code, roughly
4,950 lines are source and 3,200 are tests — a bit under 40% of the codebase
is tests, which prove intent but don't port; they'd need re-authoring against
Rust idioms.

Largest source files, in rough order of porting difficulty:

| File | Lines | Why it matters |
|---|---|---|
| `render.go` | 1,257 | UI-framework-coupled; can't be transliterated, needs a real port |
| `state.go` | 787 | Mutable UI + process-list state shared between render and input handling — where the borrow checker will bite hardest |
| `cgroup.go` | 691 | Domain logic (cgroup tree parsing/aggregation) — translates cleanly |
| `main.go` | 508 | Event loop and key handling — tcell-shaped, needs a real port |
| `proc.go` | 461 | Hand-rolled raw syscalls for `/proc` scanning — see below |
| `procdetail.go` | 248 | Domain logic — translates cleanly |
| `noninteractive.go` | 219 | Fallback plain-text renderer for non-tty runs |
| `colors.go` | 139 | Gradient/style helpers — translates cleanly |
| `cpuinfo.go` | 116 | Domain logic — translates cleanly |
| `util.go` | 82 | CPU affinity + page size helpers |

## Dependency mapping

| Go dependency | Used for | Rust replacement | Notes |
|---|---|---|---|
| `github.com/gdamore/tcell/v2` | Screen, raw mode, mouse/key/resize events, cell styling, RGB color (`main.go`, `render.go`, `colors.go`) | `ratatui` + `crossterm` backend | Biggest architectural piece — see below. |
| `github.com/mattn/go-runewidth` | `StringWidth`, `Truncate`, `FillRight` (`render.go`) | `unicode-width` crate | `StringWidth` maps directly. `Truncate`/`FillRight` and boxtop's hand-rolled ASCII fast-path around them (`render.go:1230-1240`) have no ready-made crate equivalent — re-hand-roll against `unicode-width`, same shape as today. |
| `golang.org/x/sys/unix` (`SchedGetaffinity`, `CPUSet`) | CPU affinity (`util.go`) | `nix::sched::sched_getaffinity` + `nix::sched::CpuSet` | Near-identical API, low friction. |
| `golang.org/x/term` (`IsTerminal`, `GetSize`) | tty detection, non-interactive fallback sizing (`noninteractive.go`) | `std::io::IsTerminal` (stable in std since 1.70) + `crossterm::terminal::size()` | Mostly disappears — `IsTerminal` needs no crate at all, `size()` comes free with the crossterm dependency the interactive path already needs. |
| `syscall` (`Getdents`, `Open`/`Read`/`Close`, raw `Dirent` via `unsafe.Offsetof`) | Fast `/proc` directory scanning, bypassing per-entry stat cost of a higher-level API (`proc.go:62-194`) | `nix::dir::Dir` (wraps `getdents64` behind a safe iterator) | Likely a net improvement — `libc`'s `dirent64` layout is already correct, so the `unsafe.Offsetof` pointer arithmetic goes away rather than getting reproduced. |
| `syscall.Kill`, `SIGTERM`/`SIGKILL`, `ESRCH`/`EPERM` | Process termination (`state.go:500-521`) | `nix::sys::signal::kill` + `nix::errno::Errno` | Direct mapping. |
| `cmd.ProcessState.SysUsage().(*syscall.Rusage)` | Peak RSS / CPU time for a child process (`cmd/boxbench/run.go:101`) | `libc::getrusage(RUSAGE_CHILDREN, ...)` called by hand after `wait()` | Real gap — Go's `os/exec` exposes rusage for free, Rust's `std::process::Child` doesn't. Small but non-drop-in. |
| Indirect: `uax29`, `go-colorful`, `gdamore/encoding`, `uniseg`, `golang.org/x/text` | tcell's internal grapheme/charset/color handling | N/A — not replaced individually | These vanish; superseded by whatever `ratatui`/`crossterm` pull in transitively (mainly `unicode-width`, `unicode-segmentation`). |

## Architectural considerations

**Render loop (tcell → ratatui).** boxtop's `render.go` draws by poking cells
directly at explicit (x, y) coordinates (`drawText`, `drawBar`, `drawMeter`)
rather than building a widget tree. That style maps reasonably well onto
ratatui's lower-level `Buffer::set_string`/`get_mut(x, y)` API, so a fairly
literal port is realistic without adopting ratatui's full `Widget` trait
system. tcell's manual cell-diffing — the mechanism behind the
`drawFrame`/`screen.Clear()` regression already on file in project memory
(removing `Clear()` doubles redraw cost due to tcell's `SetContent`
internals) — is replaced by ratatui's own automatic prev/next `Buffer` diff
inside `terminal.draw()`. That specific gotcha doesn't carry over as-is, but
expect a structurally similar class of "redrew more than necessary" bug while
learning ratatui's diffing behavior.

**State ownership (`state.go`).** This file holds mutable UI state (selection,
scroll position, process list) that both the renderer and the input handlers
need concurrent access to — exactly the shape that fights Rust's borrow
checker (`render(&state)` needing to coexist with an input handler wanting
`&mut state`). Expect this file to be restructured, not transliterated.

**Domain logic (`cgroup.go`, `procdetail.go`, `cpuinfo.go`, most of
`proc.go`'s parsing).** Straightforward data transformation with no Go-specific
idioms to fight — the closest thing to a mechanical port in the codebase.

## Rough estimate

- **Ports cleanly, low risk:** cgroup/proc/cpuinfo parsing and aggregation,
  `colors.go`, most of `boxbench` (excluding the rusage gap above).
- **Needs real design work, not transcription:** `render.go` (render loop
  architecture), `state.go` (ownership restructuring), `main.go` (event loop
  against crossterm instead of tcell's channel).
- **Small but non-obvious gaps:** `boxbench`'s rusage collection, the
  runewidth truncate/fill helpers, replicating the getdents fast path safely
  via `nix`.

Net: a Rust port could plausibly reuse most of the *logic* wholesale but would
require real engineering time on the render loop and state ownership — not a
mechanical transliteration end to end.

## Performance expectations

Not a blanket "Rust is faster" story — a chunk of the current hot path is
already hand-optimized in Go specifically to avoid the runtime overhead Rust
would otherwise get credit for. Expect real wins in specific, measurable
places rather than a fundamental step-change everywhere.

**Likely wins:**
- *Startup / time-to-first-output.* Go's runtime does goroutine-scheduler and
  GC initialization on every process start; a Rust binary has effectively
  none of that. This matters most for the non-interactive single-shot mode
  (`noninteractive.go`), which is exactly what `boxbench` already measures as
  `timeToFirstOutputMs` (`cmd/boxbench/run.go`).
- *Baseline memory floor.* Go's runtime carries minimum RSS overhead
  (goroutine stacks, GC bookkeeping) a Rust binary doesn't. `boxbench` also
  already measures `peakRSSKb`, so this is directly comparable once a POC
  exists rather than estimated.
- *Tail-latency jitter under load.* boxtop rebuilds process/cgroup state every
  tick, which is real allocation churn on a busy host with many containers.
  Go's GC is normally sub-millisecond, but under allocation pressure it can
  still cause an occasional visible frame hitch. Rust doesn't remove the
  allocation cost, but it removes stop-the-world GC pauses as a *class* of
  jitter — more consistent frame timing, not necessarily lower average CPU.

**Likely a wash:** the steady-state `/proc` scan loop in `proc.go`. It already
bypasses `os.ReadDir` in favor of raw `Getdents` specifically to avoid
per-entry allocation and stat overhead — it's syscall-bound, not GC-bound. A
`nix`-based Rust port of the same approach lands in the same ballpark, since
the syscalls dominate either way.

**Could initially be worse:** a first-pass ratatui port that naively rebuilds
a `Buffer` or widget state every frame (easy to do while still learning the
idioms) can cost more than tcell's persistent cell-diffing until it's tuned.
Not a fundamental limitation — just a reason not to assume the naive port is
automatically faster.

**How to actually know:** `boxbench` already captures wall clock, TTFO, peak
RSS, and CPU time in a comparable format (`cmd/boxbench/result.go`). A Rust
POC could be measured head-to-head against the same harness rather than
estimated from first principles.

### Binary size

Measured, not estimated: the current build is **4.5MB** with default flags,
**3.0MB** stripped (`go build -ldflags="-s -w"`). Most of that is fixed Go
runtime overhead (GC, goroutine scheduler, reflection metadata) baked into
every Go binary regardless of program size — Rust carries no equivalent
runtime, so binary weight scales more directly with actual code + deps. For
this dependency set (ratatui, crossterm, nix, unicode-width), a stripped
release build would plausibly land in the **1–2MB range** on a naive
comparison.

**Constraint: the port must produce a fully static binary**, matching the
current Go binary's behavior — no shared-library dependencies, so it stays
trivially copyable to another host or into a minimal container image. This is
not automatic in Rust: Cargo's default release build on glibc systems
dynamically links libc. Getting a real static binary means building against
the `x86_64-unknown-linux-musl` target rather than the default host target.
That bundles a static libc and pushes the size back up from the naive
glibc-dynamic estimate above — likely still under the Go figure, but the true
number should come from an actual `cargo build --release --target
x86_64-unknown-linux-musl` once a POC exists, not this estimate. musl-target
support should be verified for every native dependency in the stack (`nix`,
and whatever crossterm/ratatui use under the hood) before treating this as a
solved constraint.

## Open questions

- Is a literal cell-buffer port of `render.go` acceptable long-term, or is this
  the moment to redesign around ratatui's widget system properly?
- Is the `boxbench` rusage gap worth closing with `libc::getrusage`, or does
  boxbench stay Go-only (a small standalone tool) even if the main binary
  moves to Rust?
- Any appetite for a real POC (e.g., just the proc/cgroup parsing layer, or
  just the render loop against ratatui) before committing to a full port?
