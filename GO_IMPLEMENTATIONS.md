# Implementation plan

`GO_IMPROVEMENTS.md` is eight independent write-ups (perf, stability,
features, ergonomics, accessibility, the cool feature, automation, the
bigger picture) — each internally prioritized, but never weighed against
each other, and never checked for which ones fight over the same code.
This doc does that: one cross-domain backlog, sized by effort and value,
with tasks that touch the same files grouped into bundles — call for
whether to implement a bundle as one structured edit or as separate
patches — and a phased order to actually work through it in.

Effort (LoE) is relative, not calendar time: **XS** (well under an hour),
**S** (a focused session), **M** (a few sessions / a small feature arc),
**L** (a real feature, a week-ish of elapsed effort), **XL** (a new
subsystem — new screen, new interaction model, or both).

## The full backlog

| ID | Task | Source | LoE | Value | Files touched |
|---|---|---|---|---|---|
| F1 | SIGTERM/SIGHUP handler in `run()` | Part 2 stability | S | High (real user-visible bug) | `main.go` |
| F2 | Goroutine panic-recovery shim | Part 2 stability | XS–S | Low–Medium | `main.go` |
| F3 | Fuzz tests (`/proc`-stat, cgroup parsers) | Part 2 stability | S–M | Medium | `proc_test.go`, `cgroup_test.go` |
| F4 | Cmdline redaction (secrets in argv) | Part 8 gap | S–M | High (blocks C-series, D2) | `proc.go` or new `redact.go` |
| C1 | Persisted configuration (config file + precedence) | Part 4.1 | M–L | High (compounds every run) | `main.go`, new `config.go`, `state.go` |
| C2 | Live refresh-interval adjustment | Part 4.2 | S | Medium | `main.go`, `state.go`, help text |
| C3 | Promote positional interval arg to a flag | Part 4.4 | XS–S | Low | `main.go` |
| C4 | `NO_COLOR` / monochrome mode | Parts 4.3 & 5 | S | Medium (ergonomics + accessibility) | `colors.go`, `main.go` |
| G1 | PSI integration (`memory.pressure`/`cpu.pressure`) | Part 3.1 | M | High | `cgroup.go`, `render.go`, `noninteractive.go` |
| G2 | `memory.events` reader (max/high/oom) | Part 8 tier 1 prereq | S–M | Medium alone, High as D3 input | `cgroup.go` |
| D1 | Streaming plain-text mode (`--stream`) | Part 5 | M | High | `noninteractive.go`, `main.go` |
| D2 | `--json` single-run output | Part 7.1 | S–M | High | `render.go`, `proc.go`, new `json.go`, `main.go` |
| D3 | `--stream --format=json` (NDJSON) | Part 7.2 | S (once D1+D2 exist) | High | `noninteractive.go`, `json.go` |
| D4 | Threshold/exit-code check mode (`--check`) | Part 7.3 | S–M | High | `main.go`, `noninteractive.go` or new `check.go` |
| D5 | `--list-cgroups --json` | Part 7.4 | XS–S | Low–Medium | `cgroup.go`, `main.go` |
| H1 | Growth-rate sort (per-process history) | Part 6, smaller | M | Medium–High | `proc.go`, `state.go`, `render.go` |
| H2 | Black-box recorder anchored to OOM kills | Part 6, headline | XL | High (distinctive) | new `recorder.go`, `state.go`, `render.go`, `main.go` |
| B1 | Prometheus `/metrics` endpoint (`--listen`) | Part 8 tier 2 | L | Very High (multiplies everything) | new `metrics.go`, `main.go` |
| B1a | mTLS mode for `--listen` (sub-item of B1) | follow-up | S–M | Medium (hardening for anyone binding beyond loopback) | `metrics.go`, `main.go`, new key/cert-store helper |
| B2 | Incident narration | Part 8 tier 1 | L | Very High, hard dependencies | new `narrate.go`, `state.go`, `render.go` |
| X1 | Multi-cgroup live dashboard | Part 3.2 | XL | High, big standalone bet | new file(s), `main.go`, `state.go`, `render.go`, `cgroup.go` |
| X2 | Parallel `/proc` scan | Part 2, conditional | L | Low unless profiling says otherwise | `proc.go` |

## Where tasks collide: bundles

The backlog above sorted by *value* would have you bouncing between
`main.go`, `noninteractive.go`, and `render.go` all day, re-deriving the
same context each time and risking one patch's flag-parsing stepping on
another's. Five clusters are worth deliberately implementing together —
not because the individual tasks are complicated, but because doing them
separately means touching the same function three or four times instead of
once.

### Bundle A — CLI & startup consolidation (`main.go`)

**C1 (config file), C2 (live interval key), C3 (positional→flag).** Almost
every later task in this backlog (D1, D2, D4, B1) also adds a new flag to
`main.go`. Doing C1 first — and deciding the flag/config-file/default
precedence rule once — means every subsequent flag slots into an existing
pattern instead of main() accreting ad hoc flag handling five separate
times. **Implement as one structured edit**, and land it before D1/D2/D4/B1
rather than after.

### Bundle B — Non-interactive output modes (`noninteractive.go`)

**D1 (stream), D2 (json), D3 (NDJSON), D4 (check).** All four live in the
same file, all four restructure the same "collect → wait → render" loop,
and D3 is explicitly D1+D2 combined. This is the clearest case in the whole
backlog for **one coordinated epic** rather than four independent patches —
implementing D1 and D2 separately first, then bolting D3 on after, means
rewriting the loop twice. Design the loop once (one-shot vs. streaming,
plain-text vs. json output, both orthogonal flags) and D1/D2/D3/D4 fall out
as combinations of the same structure.

Apply F4 (cmdline redaction) here before D2 ships — the doc that produced
this backlog flagged this explicitly: redaction is a prerequisite, not a
follow-on, once cmdlines are being exported instead of just displayed.

### Bundle C — cgroup reader extensions (`cgroup.go`)

**G1 (PSI), G2 (memory.events).** Same file, same pattern
(`readCgroupVal`-style sysfs read + parse + wire into `frameData`). Doing
both in one pass avoids opening the same file twice for two separate
change sets. G1 is the higher-value, do-first half; G2 can ride along or
follow shortly after since B2 needs it eventually anyway.

**Sequencing note:** land G1 *before* D2 (`--json`). PSI is exactly the
kind of number an automation consumer wants in the JSON schema, and
retrofitting it after `--json` ships means either a breaking schema change
or an awkward `schema_version` bump in the first month. Cheaper to have it
in the schema from v1.

### Bundle D — Historical process tracking (`proc.go` + `state.go`)

**H1 (growth-rate sort), H2 (black-box recorder).** Both need the same
underlying capability: per-process state that survives across ticks with
bounded retention and eviction when a pid disappears. H1 is the minimal
version (a rolling RSS history per pid); H2 is the larger version (a ring
buffer of full table snapshots). **Design the retention/eviction mechanism
once, ship H1 first** as the smaller increment against it, then extend into
H2 rather than building two separate per-tick state-tracking mechanisms.

Apply F4 (redaction) to whatever H2 persists to disk, same reasoning as
Bundle B.

### Bundle E — Shared metrics extraction (`render.go` / new files)

**D2 (json), B1 (Prometheus endpoint).** Both need to turn `frameData` into
a flat set of named metrics — D2 as JSON, B1 as Prometheus text format.
Don't write this twice: factor a single internal "extract metrics from
`frameData`" function that both serializers call, so `--json` and
`--listen` can't silently drift apart on what a field means or how it's
computed. B1 also depends operationally on F1 (SIGTERM handling) — an HTTP
listener needs the same graceful-shutdown path a signal handler gives you,
so land F1 before B1, not after.

**B1a — mTLS mode, sized.** Go's stdlib `crypto/tls`/`net/http` does the
actual TLS/mTLS work — no CGO, no new dependency, fully compatible with the
static-binary constraint. On top of B1's own effort this is incremental,
sized **S–M**: it's config wiring around a handshake stdlib already
implements, not new engineering.

Design notes, folded in from discussion rather than left implicit:

- **HTTP stays the default.** `--listen` alone binds `127.0.0.1` only,
  plain HTTP, zero config — matching every other feature in boxtop.
  TLS/mTLS is additive flags, not a mode swap, and binding beyond loopback
  should require an explicit opt-in separate from enabling TLS.
- **CA optional, not absent.** The zero-config default path is pinned
  certs, not PKI: boxtop generates its own self-signed keypair on first use
  (Go's stdlib has straightforward self-signed cert generation) and
  persists it — naturally alongside C1's config directory once that
  exists. Client trust works the same way `authorized_keys` does: instead
  of validating against a CA pool, a custom
  `VerifyPeerCertificate`/`VerifyConnection` callback checks the presented
  client cert's fingerprint against a small trusted-fingerprint list boxtop
  reads from config/flag. This is what makes trying it require zero PKI —
  no CA, no issuance workflow, nothing to stand up just to test the
  feature.
  But this must not be the *only* path: anyone who already runs CA
  infrastructure (common in real deployments) needs to supply their own
  server cert/key and a CA bundle to validate client certs against, via
  the same `--tls-cert`/`--tls-key`/`--tls-client-ca` flags a
  conventional implementation would use. `tls.Config` supports both
  `ClientCAs` (pool-based) and a custom `VerifyPeerCertificate` (pinning)
  side by side without conflict, so this isn't an either/or in the
  implementation — it's two ways to populate trust, selected by which
  flags are set. Pinning is the friendly default; CA is a first-class
  option, not a workaround.
- **Ergonomics matter as much as the crypto.** The whole point of the
  pinned-cert design is that trying it shouldn't require the user to have
  ever run `openssl`. First run should print the server's own fingerprint
  plainly (the same UX SSH host keys use) so it can be pinned on the
  scraping side, and errors (untrusted client, missing/corrupt key file)
  need to say exactly what's wrong and what to do about it, not surface a
  raw TLS handshake error.

**Related: a `nolisten` build.** Worth keeping in mind while building B1 —
a security-conscious build that excludes all networking code (`--listen`,
TLS, the metrics HTTP server) entirely via a Go build tag, so the resulting
binary provably contains no listener regardless of flags. This is a strong
argument for keeping B1/B1a's code isolated to their own file(s) (already
the plan — `metrics.go`) rather than letting HTTP-serving logic leak into
`main.go` or `render.go`, since a clean build-tag boundary needs the code
it's excluding to live somewhere excludable. Not scheduled as its own
backlog item yet — a structural constraint to honor when B1 is built, not
a separate task.

## Phased order

Phases group work into what to actually pick up next, folding in the
bundle guidance above. Within a phase, order is low-risk-first; across
phases, later ones depend on earlier ones landing.

**Phase 1 — foundational hardening.** F1, F2, F3, F4. F1 is the one
genuine user-visible bug in the backlog; F4 (redaction) has no user-facing
effect yet but needs to exist before Phase 3/5 export or persist anything.
None of these are exciting, all of them de-risk later phases.

**Phase 2 — Bundle A (CLI & config consolidation).** C1, C2, C3. Do this
before any phase that adds more flags — it's the one place in the backlog
where sequencing before value-order actually matters, since skipping it
means redoing `main.go`'s flag handling later anyway.

**Phase 3 — Bundle C (cgroup signals).** G1 (PSI), then G2
(`memory.events`). G1 is independently valuable the moment it ships; G2
mainly pays off once B2 exists.

**Phase 4 — Bundle B (non-interactive output modes).** D1, D2, D3, D4 as
one coordinated epic, built on Phase 2's flag pattern and Phase 3's PSI
data already being in `frameData`. This phase is the single highest
value-per-effort block in the backlog — four "High" value items sharing
most of their implementation cost.

**Phase 5 — Bundle D (historical tracking).** H1 first (small, ships value
immediately), H2 second (the distinctive "wow" feature, built on H1's
retention mechanism). Apply F4's redaction to whatever H2 writes to disk.

**Phase 6 — capstones.** B1 (Prometheus endpoint, Bundle E, needs F1 +
ideally D2's extraction logic) and B2 (incident narration, needs G1 + H2 —
this is the one item in the backlog that's a pure synthesis of three
earlier phases, so it can't move earlier no matter how high its value is).
X1 (multi-cgroup dashboard) is large and independent of the rest — schedule
it based on appetite rather than dependency, since nothing else in the
backlog needs it to land first and it needs nothing else either.

**Phase 7 — conditional.** X2 (parallel `/proc` scan) only if profiling on
a high-process-count host actually shows tick latency is a problem. Don't
schedule it speculatively.

## Parking lot — deliberately not planned

Carried forward from `GO_IMPROVEMENTS.md` so they aren't re-proposed
without the context for why they were rejected:

- **Full UI i18n** (Part 5) — no peer tool in this space translates;
  ongoing maintenance burden without a real audience asking for it.
- **Multi-host / Kubernetes fan-out** (Part 8) — a different product; the
  static single-binary, single-host model is the appeal, not a limitation
  to engineer around. B1's Prometheus endpoint gets most of the real value
  without boxtop becoming a distributed system.
- **Memory bandwidth tracking** — no non-invasive data source exists;
  real measurement needs privileged PMU access that's a fundamentally
  different (and much larger) blast radius than anything else boxtop does.
