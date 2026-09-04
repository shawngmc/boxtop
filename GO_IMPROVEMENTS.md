# Go improvements: performance, stability, features, ergonomics, accessibility, automation, and the bigger picture

Eight goals: (1) reap the perf wins `PORTING.md` attributed to a hypothetical
Rust rewrite, in Go, with no rewrite required; (2) close the stability/polish
gaps that separate "works" from best-in-class, found by reading the current
code rather than guessing; (3) identify the feature work that would move the
needle most, on top of that foundation; (4) fix the usability/ergonomics
friction that shows up in day-to-day use, independent of features or perf;
(5) assess accessibility honestly — what's structurally hard, what's already
solid, and what's actually worth building; (6) the genuinely fun,
distinctive feature — subjective by nature, but worth writing down; (7) make
boxtop a good citizen in scripts and pipelines, not just an interactive tool;
(8) step back — a cross-cutting risk the other parts create together, and
what would take boxtop from good to genuinely amazing.

## Part 1: Reaping Rust's likely wins, in Go

`PORTING.md` identified plausible Rust wins over the current Go build,
including startup/TTFO and baseline memory floor (see its "Performance
expectations" section). This part is the counter-proposal: concrete,
reversible Go-side changes aimed at the same wins, each validatable with the
`boxbench` harness that already exists — no Rust required to find out
whether they're worth it.

### 1. Steady-state allocation churn — mostly already done

`proc.go`'s hot `/proc` scan path is already deliberately allocation-light:

- `listPIDs` threads its buffer in/out for reuse (`proc.go:125-152`).
- `readProcFile`'s buffer is reused across pids by the poll loop.
  `readStatNameCPU`'s fresh `make([]byte, 4096)` is explicitly a
  "convenience wrapper" (`proc.go:208-213`), not what the hot loop actually
  calls.

This is already hand-doing, in Go, what PORTING.md credited Rust with getting
"for free." Before spending more effort here:

- Profile first — `GODEBUG=gctrace=1 ./boxtop` while running interactively,
  or `go tool pprof -memprofile` on a `boxbench` run.
- Check `render.go` / `state.go` / `cgroup.go` for anything rebuilt fresh
  every tick (sorted process slice, formatted strings, meter structs) rather
  than reused.
- Fix what profiling actually shows, not what seems likely.

### 2. Startup / TTFO

`scripts/build.sh` already builds with `CGO_ENABLED=0 -trimpath
-ldflags="-s -w"` — the standard fast-start/small-binary recipe. Importantly,
`CGO_ENABLED=0` means **the current Go binary is already fully static**, at a
measured 3.0MB stripped — no glibc-dynamic-linking gap to close. That's
exactly the constraint PORTING.md flagged as needing extra musl-target work
in Rust; Go already has it for free.

Remaining levers:

- **PGO** (`go build -pgo=auto`, available since Go 1.21; repo is on 1.25):
  feed it a profile from a `boxbench` run. No code changes, improves inlining
  across startup and hot paths.
- Confirm where TTFO actually goes before optimizing further: it may be
  dominated by `tcell.Screen.Init()`'s terminfo lookup rather than Go runtime
  init, in which case none of the Go-runtime knobs above touch it, and
  PORTING.md's "Rust removes startup cost" framing was assuming the wrong
  bottleneck. Check with `boxbench`'s `timeToFirstOutputMs` plus a trace.

### Suggested order

1. Profile allocation churn and TTFO first — `GODEBUG=gctrace=1`, `go tool
   pprof`, and a `boxbench` trace — rather than assuming where the cost is.
2. Decide whether PGO or further allocation work is worth chasing based on
   what profiling actually shows.

If the resulting numbers land close to what PORTING.md estimated for Rust,
that's a stronger argument against the port than PORTING.md itself makes.

## Part 2: Stability and further performance hardening

Most of the obvious perf traps in this codebase are already handled
carefully — buffer reuse, string-aliasing safety around the reused `/proc`
read buffer, the getdents fast path — each with a comment explaining why.
The remaining value is elsewhere: a couple of real, verified gaps, plus
tooling that would catch the next one automatically.

### Stability

**The interactive path has no signal handling — this is the headline
finding.** `runNonInteractive` registers `signal.Notify` for
`SIGTERM`/interrupt (`noninteractive.go:64`), but the interactive `run()` in
`main.go` never does. `defer screen.Fini()` at `main.go:101` only runs on a
normal return or a panic on that same goroutine — an unhandled `SIGTERM`
(docker stop, systemd, a plain `kill`) terminates the process immediately
without running deferred functions at all. That leaves the terminal in raw
mode, alternate screen, and mouse-tracking-enabled — the user has to know to
run `reset` or `stty sane; tput rmcup`. Ctrl+C works fine today because tcell
intercepts it as a key event, not a signal, but anything sending the process
a real signal breaks the terminal. htop/btop both handle this explicitly.

Fix: register `signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGHUP)` in
`run()`, select on it alongside the ticker/events, and let it fall through
the same `return nil` path that already triggers `screen.Fini()`.

**No `recover()` anywhere in the repo.** Mostly fine — a panic on the main
goroutine (inside `handleEvent`/`collectFrame`/`drawFrame`) already unwinds
through `run()`'s own defers and restores the terminal correctly before the
crash. The one gap is the separate `go screen.ChannelEvents(events, quit)`
goroutine at `main.go:118`: a panic there runs *its own* defers only, not
`run()`'s, so terminal cleanup gets skipped. Worth wrapping it in a small
recover-then-`screen.Fini()`-then-repanic shim for defense in depth, though
this is secondary to the SIGTERM gap since tcell's own code is the unlikely
panic source.

**No fuzz tests.** The `/proc`-stat and cgroup-value parsers
(`proc.go:232`'s `parseStatNameCPU`, the cgroup readers) are exactly the kind
of hand-rolled string/byte parsing Go's built-in fuzzer is good at
hardening — process names can contain almost arbitrary bytes via
`prctl(PR_SET_NAME)`, and the parser already has a documented edge case
(nested parens) it handles correctly. A `func FuzzParseStatNameCPU` costs
little and would catch the next edge case before a weird process name on
someone's host does.

### Performance

The GC/TTFO angle is covered in Part 1. One additional idea, flagged as
*conditional*: the `/proc` scan in `buildProcesses` (`proc.go:409`) is
sequential — one open/read/close per pid. On a host with a very large
process count this is the dominant per-tick cost, and it's parallelizable (a
small worker pool, one scratch buffer per worker). Only worth pursuing after
profiling shows tick latency is actually a problem at realistic scale — it
adds real complexity (buffer-per-worker, ordering for stable sort) for a
cost that today is "likely a wash" per Part 1's analysis.

### Suggested order

1. SIGTERM/SIGHUP handler in `run()` — small, concrete, fixes a real
   user-visible bug.
2. Fuzz tests for the `/proc`/cgroup parsers.
3. Goroutine panic recovery shim — defense in depth, lower priority.
4. Parallel `/proc` scan — only if profiling on a high-process-count host
   shows it's actually needed.

## Part 3: Feature wins

Stability and perf work makes boxtop solid; these are the features that
would make it distinctive — chosen for fit with boxtop's actual niche
(cgroup/container-aware monitoring) rather than parity-copying htop/btop.

### 1. cgroup pressure (PSI) integration — recommended first

Surface `/sys/fs/cgroup/<path>/memory.pressure` and `cpu.pressure` (the
kernel's own "some"/"full" avg10/avg60/avg300 stall percentages) alongside
the existing RAM/CPU/Swap meters.

This is the natural next step from a feature boxtop already has: it surfaces
the cgroup's OOM-kill count specifically so a kernel-reaped process doesn't
vanish invisibly — a *reactive* signal, after the fact. PSI is the
*predictive* version of the same story: "this cgroup has spent 30% of the
last 10 seconds stalled on memory pressure" tells you an OOM kill or CPU
throttling is imminent, before it happens.

Nothing else in this space does this well — `htop`/`top` aren't cgroup-aware
at all, and `ctop`/`docker stats` mostly show usage-vs-limit, not pressure.
For boxtop's actual audience (someone watching a container/pod near its
memory or CPU limit), "am I about to get OOM-killed" beats one more usage
bar.

Also cheap to build: one more file read per tick, parsed with the same
helper pattern `readCgroupVal` already establishes, displayed as one more
line/meter — no new interaction model, no new screen.

### 2. Live multi-cgroup/container dashboard

A `ctop`/`docker stats`-style grid of every cgroup on the host at once,
sortable by RAM/CPU/limit%, with Enter drilling into the existing
per-process view.

`listCgroupsStatus` already exists for the one-shot `--list-cgroups` dump,
so the data layer is half-built; the work is a new live/interactive layout
mode plus scoping the per-tick `/proc` scan sensibly across many cgroups at
once. This is the more visible, "wow" feature and directly useful if the
real use case is watching a whole host's worth of containers rather than
going deep on one — but it's a genuinely new screen and interaction model,
not an incremental addition, so it's a bigger bet than PSI.

### Suggested order

1. PSI integration — smaller, sharper, and it's the thing boxtop is
   uniquely positioned to do well given the existing OOM-kill-count feature.
2. Multi-cgroup dashboard — bigger scope, pursue once PSI has proven out the
   "predictive cgroup health" angle, or if host-level fleet-watching turns
   out to be the more common use case than single-container deep-dives.

## Part 4: Usability and ergonomics

Found by reading the actual keybinding code and docs, not by assuming.
A few consistent friction points.

### 1. No persisted configuration

Every launch resets to defaults — colorblind palette, last-monitored cgroup,
active filter, `--narrow`, refresh interval all have to be re-specified via
flags each time. There's no config file and no env-var fallback. For a tool
someone runs repeatedly against the same target (their own dev container,
say), that's real recurring friction — `htop` persists exactly this kind of
state in `~/.config/htop/htoprc`. This is the one to prioritize for a
genuine ergonomics project, since it compounds every single invocation
rather than being a one-time surprise.

### 2. No live refresh-interval adjustment

The poll interval is fixed at launch (`boxtop 2` for a 2s interval) with no
in-app key to speed up or slow down polling — `htop` binds `s` for this.
Deciding mid-session you want faster updates means quitting and relaunching.

### 3. No `NO_COLOR` / true monochrome mode

`--colorblind` swaps to an alternate palette but there's no way to disable
color entirely — some terminals/setups rely on the `NO_COLOR` env var
convention, which boxtop doesn't check.

### 4. Positional refresh-interval arg is a discoverability wart

The refresh interval is a bare positional argument (`boxtop 2`) rather than
a flag, inconsistent with everything else being `--flag`-style. It's
documented in `--help`, but a first-time user has no reason to guess it
exists.

Filter (already matches both process name and full cmdline —
`state.go:349-358`), the sort-column indicator (▼/▲ on the active header —
`state.go:721-731`), and kill-error messages (`state.go:515-526`) are all
already well done; no changes needed there.

### Suggested order

1. Persisted configuration — the highest-leverage ergonomics investment,
   compounds on every run.
2. Live refresh-interval adjustment — small, self-contained.
3. `NO_COLOR` support — small, self-contained.
4. Promote the positional interval arg to a flag (keeping the positional
   form working for compatibility) — cosmetic, lowest priority.

## Part 5: Accessibility

The honest structural reality first, then what's already solid, then the
one thing actually worth building.

### The hard truth: full-screen TUIs and screen readers don't mix well

This isn't really fixable at boxtop's layer. tcell owns raw mode and the
alternate screen buffer, repainting arbitrary (x, y) cells every frame with
no semantic structure a screen reader can hook into — there's no
accessibility tree the way a GUI or web app has ARIA. Terminal screen
readers generally work by reading linear scrollback text, not by tracking a
redrawn grid. `htop`, `btop`, and `vim` all share this limitation; it's an
unsolved ecosystem-level problem, not something to promise to fix by tuning
boxtop's renderer.

### What's actually achievable: a streaming plain-text mode

`noninteractive.go` already does the structurally right thing — sequential,
linear stdout, no raw mode, no cell-grid redraw. Today it's a single static
snapshot, though. Extending it into a `--stream`/continuous plain-text mode
(print a fresh snapshot as new linear output each tick, like `vmstat`/`dstat`
do) would give screen-reader and braille-display users — and log-friendly
automation — a first-class way to use boxtop without ever touching the
raw-mode renderer. This is the single highest-value accessibility
investment here, and it's a small, scoped extension of code that already
exists.

### Already solid — worth noting, not "fixing"

- Color isn't the sole channel for meter severity — the numeric percentage
  (`m.pctText`) is always drawn next to the gradient bar
  (`render.go:173`), so a colorblind or no-color-perception user still gets
  the number, not just a hue.
- Everything is already keyboard-operable — no mouse-only actions, a real
  motor-accessibility win already banked.
- Non-ASCII process names (CJK, Cyrillic, emoji) render correctly:
  `sanitize()` ranges over runes, not bytes (`proc.go:38`, UTF-8-safe), and
  `go-runewidth` is already used throughout for column alignment with wide
  characters (`render.go:30` and others) — solid i18n-adjacent groundwork
  already in place.

### The one real gap, shared with Part 4

No `NO_COLOR`/true monochrome mode (see Part 4 item 3). Worth framing as an
accessibility concern too, not just ergonomics — this matters for no color
perception at all, not only colorblindness.

### On full i18n (translated UI strings): recommend against it

No peer tool in this space (`htop`, `top`, `btop`, `iotop`) ships
translations — the audience is sysadmins/SREs operating English-language
terminal tooling as a matter of course. Gettext-style message catalogs would
be an ongoing maintenance burden (every new string needs translation,
translations drift stale) for a benefit this specific tool's audience isn't
asking for, and RTL terminal rendering is itself largely unsolved at the
emulator level, so even doing the work wouldn't fully deliver. This is a
case where the honest answer is "don't," not "here's how."

### Suggested order

1. Streaming plain-text mode (`--stream`) — the one genuinely high-value,
   buildable accessibility feature.
2. `NO_COLOR` support — shared with Part 4 item 3; do it once, credit it to
   both.
3. Full UI i18n — deliberately not recommended; noted here so it isn't
   re-proposed without this context.

## Part 6: The cool feature

Subjective by nature — but worth writing down rather than losing to
conversation scrollback.

### Recommended: a black-box flight recorder, anchored to OOM kills

boxtop already samples every tick and keeps rolling history for the
sparklines (`history []float64` per meter). Extend that into a small ring
buffer of full process-table snapshots, and the moment the cgroup's
OOM-kill count increments, pin a snapshot of what the process table looked
like in the seconds before and after the kill. Give the user a key to scrub
backward through recent history — like a DVR — landing automatically on the
OOM moment.

Instead of "something got OOM-killed, good luck reconstructing what
happened," this gives "here's exactly what was running and how memory was
trending in the 30 seconds before it died." Nothing else in this space does
zero-config local forensic replay like this — Prometheus/Datadog need
scraping configured in advance; boxtop would just always be watching. It's
also a natural extension of infrastructure that already exists (the
sparkline history mechanism), not a moonshot — though it does mean
buffering more state per tick (a bounded ring buffer, so a manageable memory
cost) and a new "scrub through time" interaction layered onto the existing
UI.

### Smaller, complementary: sort by growth rate, not just size

Since per-meter history already exists, extend the same idea per-process
and let users sort by RSS growth rate instead of raw RSS — "what's
climbing," not just "what's big right now." This is the kind of thing that
catches a leak an hour before it would've triggered an OOM kill, and it's a
much smaller lift than the full recorder (no new UI, just a new sort column
built on data boxtop is already close to having).

### Suggested order

1. Growth-rate sort — small, self-contained, ships value immediately.
2. Black-box recorder — the bigger, more distinctive bet; natural follow-on
   once per-process history (built for growth-rate sort) already exists to
   snapshot from.

## Part 7: Automation

boxtop is currently interactive-first with a one-shot plain-text fallback
(`--non-interactive`). Scripts, cron jobs, alerting, and log pipelines
deserve a first-class path too — and most of the plumbing for it already
exists.

### 1. `--json` single-run output — recommended, cheap to build

`frameData` (`render.go:346-392`) already carries essentially the whole
schema a JSON snapshot would want: cgroup/system RAM/CPU/Swap meters, the
cgroup name and limit, the OOM-kill count, and the full `Process` list (PID,
RSS, name, cmdline, CPU%) — all assembled by `collectFrame()` every tick.
`cmd/boxbench/result.go` already establishes the pattern to follow (typed
structs with `json` tags, `json.MarshalIndent`), so this isn't a design
problem, just a few hours of plumbing: add `--json` alongside
`--non-interactive`, marshal `frameData` instead of calling
`writeNonInteractiveFrame`.

Why it matters: the current plain-text snapshot is formatted for human
eyes — aligned columns, `…`-truncated names, mixed units like `512.3MB` —
which is fragile to parse and actively hostile to `jq`/scripts/log
pipelines. A real automation consumer shouldn't have to regex a table meant
for a terminal.

One thing to decide up front: once `--json` ships, its shape becomes
something scripts depend on. Include a `schema_version` field from day
one — cheap insurance against a later field rename breaking someone's cron
job.

### 2. Unify with Part 5's streaming mode — build once, serve two audiences

The `--stream` idea from Part 5 (continuous plain-text snapshots instead of
one-shot, for screen-reader/braille-display use) and this JSON idea are the
same underlying feature pointed at two different consumers. `--stream
--format=json` producing NDJSON (one JSON object per tick, newline-
delimited) serves screen readers *and* `jq`/log pipelines *and* long-running
monitoring scripts with one implementation instead of two. Build it as one
feature, not two.

### 3. Threshold/exit-code check mode

Something like `boxtop -n --check ram=90,cpu=95` that exits non-zero if a
threshold's breached, printing nothing (or a one-line reason) otherwise.
This turns boxtop into a proper health-check probe usable directly in
cron/systemd/CI without any JSON parsing needed for the simple case — a
poor man's Nagios check, scoped specifically to cgroup limits, which is
exactly boxtop's niche and something generic like `top` can't do.

### 4. `--list-cgroups --json`

Smaller win: the existing one-shot cgroup listing gets the same JSON
treatment, for scripts that enumerate cgroups programmatically rather than
picking one to watch interactively.

### Suggested order

1. `--json` single-run output — the foundational piece, cheap, and
   everything else in this part builds on its schema.
2. Threshold/exit-code check mode — small, self-contained, high automation
   value on its own.
3. `--stream --format=json` (NDJSON) — build once schema/marshaling from
   item 1 exist; serves Part 5's accessibility goal at the same time.
4. `--list-cgroups --json` — smallest, lowest priority.

## Part 8: The bigger picture

Two different kinds of finding: a real gap created by combining earlier
ideas (fix this), and what would take boxtop from good to genuinely
amazing (aspirational, in priority order).

### A real gap: Parts 6 and 7 combine into a new secrets exposure

Process `cmdline` often contains secrets — `--db-password=hunter2`, tokens
passed as flags, connection strings — and boxtop already shows this in the
detail popup, which is fine today because it's ephemeral: on one person's
screen, gone once they scroll past. But the black-box recorder (Part 6,
persisting process-table snapshots to disk) and JSON/NDJSON export (Part 7,
shipped to log aggregators and observability pipelines by design) both turn
that same cmdline data from ephemeral into persisted-and-exported by
default. Individually, the recorder and JSON export both look clean;
combined, "briefly visible on a terminal" becomes "written to disk and
forwarded to a SIEM or a third-party log vendor."

Worth a redaction pass — masking argv patterns that look like
`--*password*=`, `--*token*=`, `--*secret*=`, common `KEY=value`-shaped
args — before anything gets written or serialized, or at minimum an opt-in
flag for including raw cmdlines in exports, off by default. This is
higher-priority than either Part 6 or Part 7's own suggested order implies,
since it needs to land before both features ship, not after.

### Amazing, tier 1: correlate the signals into one incident story

PSI (Part 3), the OOM-kill count, `memory.events` (max/high/oom counters —
not read anywhere in the codebase today), and the black-box recorder
(Part 6) are currently four separate signals a user has to mentally
cross-reference. The genuinely impressive version correlates them
automatically: the moment pressure starts climbing, boxtop narrates —
"memory pressure has been rising for 40s; process X grew 300MB in that
window; cgroup hit `memory.high` twice; OOM killer fired once at 14:32:07"
— one plain-English incident summary instead of four things to piece
together by hand. That's the difference between a dashboard and a
diagnostic assistant, and it's synthesis of data already being collected
for other reasons, not a new data source.

### Amazing, tier 2: make the data useful even when nobody's watching

A `--listen :9100` mode exposing the same cgroup-scoped numbers (RAM/CPU/
Swap, PSI, per-process breakdown, OOM count) as a Prometheus-format
`/metrics` endpoint turns boxtop from "a thing you run to look at one host"
into infrastructure other tools build on — feeding Grafana, existing
alerting stacks, whatever someone already runs — without them
reimplementing cgroup-v1-vs-v2 parsing themselves. `cAdvisor` is heavy for
this; `node_exporter` doesn't do per-process cgroup breakdowns well. This
multiplies every other feature's value for free rather than being one more
standalone feature.

### Explicitly out of scope: multi-host / Kubernetes fan-out

Tempting given the README already says "docker, k8s, etc.," but it's a
different product. The whole appeal right now is a zero-dependency static
binary that just reads local `/proc`/cgroup files. Adding cluster
discovery, auth, and multi-host state management is the kind of scope
creep that turns a sharp tool into a mediocre one. Someone who wants that
can run `boxtop --listen` on each node and point Prometheus at all of
them — which tier 2 above already gives them, without boxtop itself
becoming a distributed system.

### Suggested order

1. Cmdline redaction — do this before Parts 6/7 ship, not after; it's a
   prerequisite, not a follow-on.
2. Incident narration (tier 1) — the highest-leverage "amazing," and pure
   synthesis of data collected elsewhere in this doc.
3. Prometheus `/metrics` endpoint (tier 2) — bigger lift, multiplies
   everything else's value once built.
4. Multi-host/k8s fan-out — not recommended; noted so it isn't re-proposed
   without this context.
